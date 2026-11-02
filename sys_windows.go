//go:build windows

package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	shell32                        = syscall.NewLazyDLL("shell32.dll")
	user32                         = syscall.NewLazyDLL("user32.dll")
	procGetLogicalDrives           = kernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW              = kernel32.NewProc("GetDriveTypeW")
	procSetErrorMode               = kernel32.NewProc("SetErrorMode")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procShellExecuteW              = shell32.NewProc("ShellExecuteW")
	procMessageBoxW                = user32.NewProc("MessageBoxW")
)

const (
	createNoWindow         = 0x08000000
	detachedProcess        = 0x00000008
	createNewProcessGroup  = 0x00000200
	createBreakawayFromJob = 0x01000000
	keyWow6464             = 0x0100
	keyWow6432             = 0x0200
)

func init() {
	// Не показывать системные окна «Вставьте диск» при проверке пустых дисководов.
	procSetErrorMode.Call(0x0001 | 0x8000)
}

// ------------------------------------------------------------------ реестр

var hives = map[string]syscall.Handle{
	"HKCU": syscall.HKEY_CURRENT_USER,
	"HKLM": syscall.HKEY_LOCAL_MACHINE,
	"HKCR": syscall.HKEY_CLASSES_ROOT,
}

func regOpen(hive, key string, view uint32) (syscall.Handle, error) {
	var h syscall.Handle
	k, err := syscall.UTF16PtrFromString(key)
	if err != nil {
		return 0, err
	}
	err = syscall.RegOpenKeyEx(hives[hive], k, 0, syscall.KEY_READ|view, &h)
	return h, err
}

func regQueryString(h syscall.Handle, name string) (string, bool) {
	var np *uint16
	if name != "" {
		np, _ = syscall.UTF16PtrFromString(name)
	}
	var typ, n uint32
	if err := syscall.RegQueryValueEx(h, np, nil, &typ, nil, &n); err != nil {
		return "", false
	}
	if typ != syscall.REG_SZ && typ != syscall.REG_EXPAND_SZ {
		return "", false
	}
	if n == 0 {
		return "", true
	}
	buf := make([]uint16, n/2+2)
	n = uint32(len(buf) * 2)
	if err := syscall.RegQueryValueEx(h, np, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n); err != nil {
		return "", false
	}
	s := syscall.UTF16ToString(buf)
	if typ == syscall.REG_EXPAND_SZ {
		s, _ = expandPercent(s)
	}
	return s, true
}

// regGet читает строку из реестра (64- и 32-битная ветка).
func regGet(hive, key, value string) (string, bool) {
	for _, view := range []uint32{keyWow6464, keyWow6432} {
		h, err := regOpen(hive, key, view)
		if err != nil {
			continue
		}
		s, ok := regQueryString(h, value)
		syscall.RegCloseKey(h)
		if ok {
			return s, true
		}
	}
	return "", false
}

func regSubkeys(hive, key string) []string {
	seen := map[string]bool{}
	var out []string
	for _, view := range []uint32{keyWow6464, keyWow6432} {
		h, err := regOpen(hive, key, view)
		if err != nil {
			continue
		}
		for i := uint32(0); ; i++ {
			buf := make([]uint16, 512)
			n := uint32(len(buf))
			if err := syscall.RegEnumKeyEx(h, i, &buf[0], &n, nil, nil, nil, nil); err != nil {
				break
			}
			name := syscall.UTF16ToString(buf[:n])
			if !seen[strings.ToLower(name)] {
				seen[strings.ToLower(name)] = true
				out = append(out, name)
			}
		}
		syscall.RegCloseKey(h)
	}
	return out
}

var (
	advapi32            = syscall.NewLazyDLL("advapi32.dll")
	procRegCreateKeyExW = advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW = advapi32.NewProc("RegDeleteValueW")
)

func regSetString(h syscall.Handle, name, value string) error {
	n, _ := syscall.UTF16PtrFromString(name)
	v, _ := syscall.UTF16FromString(value) // с завершающим нулём
	r, _, _ := procRegSetValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(n)), 0, syscall.REG_SZ,
		uintptr(unsafe.Pointer(&v[0])), uintptr(len(v)*2))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func regSetDWORD(h syscall.Handle, name string, value uint32) error {
	n, _ := syscall.UTF16PtrFromString(name)
	r, _, _ := procRegSetValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(n)), 0, syscall.REG_DWORD,
		uintptr(unsafe.Pointer(&value)), 4)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// rageRegKey — ключ RAGE MP в HKCU (в тестах на Windows подменяется временным).
var rageRegKey = `Software\RAGE-MP`

// rageConnect делает то же, что официальный лаунчер GTA5RP: кладёт адрес сервера в
// HKCU\Software\RAGE-MP (launch2.ip / launch2.port), и updater.exe сразу подключается к нему.
// Пустой адрес убирает автоподключение — RAGE MP покажет список серверов.
func rageConnect(addr string) error {
	k, _ := syscall.UTF16PtrFromString(rageRegKey)
	var h syscall.Handle
	r, _, _ := procRegCreateKeyExW.Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(k)), 0, 0, 0,
		uintptr(syscall.KEY_READ|0x0002), 0, uintptr(unsafe.Pointer(&h)), 0) // KEY_SET_VALUE
	if r != 0 {
		return syscall.Errno(r)
	}
	defer syscall.RegCloseKey(h)
	if addr == "" {
		for _, name := range []string{"launch2.ip", "launch2.port"} {
			n, _ := syscall.UTF16PtrFromString(name)
			procRegDeleteValueW.Call(uintptr(h), uintptr(unsafe.Pointer(n)))
		}
		return nil
	}
	host, port := splitServer(addr)
	if err := regSetString(h, "launch2.ip", host); err != nil {
		return err
	}
	// Тип значения порта повторяем за официальным лаунчером, если он уже его записывал.
	var typ, size uint32
	pn, _ := syscall.UTF16PtrFromString("launch2.port")
	if syscall.RegQueryValueEx(h, pn, nil, &typ, nil, &size) == nil && typ == syscall.REG_DWORD {
		var n uint32
		fmt.Sscan(port, &n)
		return regSetDWORD(h, "launch2.port", n)
	}
	return regSetString(h, "launch2.port", port)
}

// ------------------------------------------------------------------ диски и процессы

func fixedDrives() []string {
	mask, _, _ := procGetLogicalDrives.Call()
	var out []string
	for i := 2; i < 26; i++ { // без A: и B:
		if mask>>uint(i)&1 == 0 {
			continue
		}
		root := fmt.Sprintf("%c:\\", 'A'+i)
		p, _ := syscall.UTF16PtrFromString(root)
		if t, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(p))); t == 3 { // DRIVE_FIXED
			out = append(out, root[:2])
		}
	}
	if len(out) == 0 {
		out = []string{"C:"}
	}
	return out
}

func processPath(pid uint32) string {
	h, err := syscall.OpenProcess(0x1000, false, pid) // PROCESS_QUERY_LIMITED_INFORMATION
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	r, _, _ := procQueryFullProcessImageNameW.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func snapshotProcs() ([]procInfo, error) {
	h, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(h)
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := syscall.Process32First(h, &e); err != nil {
		return nil, err
	}
	var out []procInfo
	for {
		name := strings.ToLower(syscall.UTF16ToString(e.ExeFile[:]))
		exe := ""
		if genericExe[name] { // путь нужен только для «общих» имён
			exe = processPath(e.ProcessID)
		}
		out = append(out, procInfo{name, exe})
		if err := syscall.Process32Next(h, &e); err != nil {
			break
		}
	}
	return out, nil
}

// ------------------------------------------------------------------ запуск программ

func shellExecute(verb, file, args, cwd string) error {
	v, _ := syscall.UTF16PtrFromString(verb)
	f, _ := syscall.UTF16PtrFromString(file)
	var a, d *uint16
	if args != "" {
		a, _ = syscall.UTF16PtrFromString(args)
	}
	if cwd != "" {
		d, _ = syscall.UTF16PtrFromString(cwd)
	}
	r, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(f)),
		uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(d)), 1) // SW_SHOWNORMAL
	if r <= 32 {
		switch r {
		case 2, 3:
			return fmt.Errorf("файл не найден: %s", file)
		case 5:
			return errors.New("отказано в доступе")
		case 31: // SE_ERR_NOASSOC
			return errors.New("Windows не знает, чем открыть эту ссылку — установлен ли нужный лаунчер?")
		}
		return fmt.Errorf("Windows вернул код %d", r)
	}
	return nil
}

type sysStarter struct{}

// Start запускает программу отдельно от лаунчера, чтобы она жила и после его закрытия.
func (sysStarter) Start(path, args, cwd string, admin bool) error {
	if !isDir(cwd) {
		cwd = filepath.Dir(path)
	}
	if admin {
		if err := shellExecute("runas", path, args, cwd); err != nil {
			return &LaunchError{Msg: "Windows не разрешил запуск от имени администратора: " + err.Error(), Code: "start_failed"}
		}
		return nil
	}
	if !strings.HasSuffix(strings.ToLower(path), ".exe") { // .lnk, .url, .bat — через оболочку
		return shellExecute("open", path, args, cwd)
	}
	cmdline := syscall.EscapeArg(path)
	if args != "" {
		cmdline += " " + args
	}
	var last error
	for _, flags := range []uint32{detachedProcess | createNewProcessGroup | createBreakawayFromJob, detachedProcess | createNewProcessGroup} {
		cmd := exec.Command(path)
		cmd.Dir = cwd
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdline, CreationFlags: flags}
		err := cmd.Start()
		if err == nil {
			_ = cmd.Process.Release()
			return nil
		}
		last = err
		var errno syscall.Errno
		if errors.As(err, &errno) && errno == 740 { // нужны права администратора
			return sysStarter{}.Start(path, args, cwd, true)
		}
	}
	return fmt.Errorf("не удалось запустить %s: %v", baseName(path), last)
}

func (sysStarter) OpenURI(uri string) error { return shellExecute("open", uri, "", "") }

// ------------------------------------------------------------------ PowerShell: диалоги, иконки, ярлыки

func powershell(script string, env map[string]string) (string, error) {
	head := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8\n$ErrorActionPreference = 'Stop'\n"
	// Скрипт передаётся в -EncodedCommand (UTF-16LE + base64): так не нужно экранировать кавычки и переносы.
	u := utf16.Encode([]rune(head + script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		logger.Printf("PowerShell: %v %s", err, stderr.String())
		return strings.TrimSpace(string(out)), fmt.Errorf("PowerShell: %v", err)
	}
	return strings.TrimSpace(string(out)), nil
}

var fileFilters = map[string]string{
	"game":  "Игры и ярлыки (*.exe;*.lnk;*.url;*.bat)|*.exe;*.lnk;*.url;*.bat|Все файлы (*.*)|*.*",
	"exe":   "Программы (*.exe)|*.exe|Все файлы (*.*)|*.*",
	"image": "Картинки (*.jpg;*.jpeg;*.png;*.webp;*.bmp;*.gif)|*.jpg;*.jpeg;*.png;*.webp;*.bmp;*.gif",
}

const psOpenFile = `
Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.Application]::EnableVisualStyles()
$owner = New-Object System.Windows.Forms.Form -Property @{ TopMost = $true; ShowInTaskbar = $false }
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Title = $env:IGN_TITLE
$d.Filter = $env:IGN_FILTER
$d.DereferenceLinks = $false
if ($env:IGN_INIT -and (Test-Path -LiteralPath $env:IGN_INIT)) { $d.InitialDirectory = $env:IGN_INIT }
if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output $d.FileName }
$owner.Dispose()
`

const psOpenFolder = `
Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.Application]::EnableVisualStyles()
$owner = New-Object System.Windows.Forms.Form -Property @{ TopMost = $true; ShowInTaskbar = $false }
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = $env:IGN_TITLE
$d.ShowNewFolderButton = $false
if ($env:IGN_INIT -and (Test-Path -LiteralPath $env:IGN_INIT)) { $d.SelectedPath = $env:IGN_INIT }
if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output $d.SelectedPath }
$owner.Dispose()
`

func browseDialog(kind, title, initial string) (string, error) {
	if isFile(initial) {
		initial = filepath.Dir(initial)
	}
	if title == "" {
		title = "Выберите файл"
	}
	env := map[string]string{"IGN_TITLE": title, "IGN_INIT": initial}
	if kind == "folder" {
		return powershell(psOpenFolder, env)
	}
	f := fileFilters[kind]
	if f == "" {
		f = fileFilters["game"]
	}
	env["IGN_FILTER"] = f
	return powershell(psOpenFile, env)
}

const psShortcut = `
$desk = [Environment]::GetFolderPath('Desktop')
$path = Join-Path $desk $env:IGN_FILE
$s = (New-Object -ComObject WScript.Shell).CreateShortcut($path)
$s.TargetPath = $env:IGN_TARGET
$s.Arguments = $env:IGN_ARGS
$s.WorkingDirectory = $env:IGN_WD
if ($env:IGN_ICON) { $s.IconLocation = $env:IGN_ICON }
$s.Description = $env:IGN_DESC
$s.Save()
Write-Output $path
`

var badFileChars = regexp.MustCompile(`[\\/:*?"<>|]+`)

func createShortcut(g *Game, iconExe string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(badFileChars.ReplaceAllString(g.Name, " "))
	if name == "" {
		name = "Игра"
	}
	icon := self + ",0"
	if strings.HasSuffix(strings.ToLower(iconExe), ".exe") && isFile(iconExe) {
		icon = iconExe + ",0"
	}
	p, err := powershell(psShortcut, map[string]string{
		"IGN_FILE": name + ".lnk", "IGN_TARGET": self, "IGN_ARGS": "--launch " + g.ID,
		"IGN_WD": filepath.Dir(self), "IGN_ICON": icon, "IGN_DESC": "Запуск " + g.Name + " через Zero Launcher",
	})
	if err == nil && p == "" {
		err = errors.New("не удалось создать ярлык")
	}
	return p, err
}

// ------------------------------------------------------------------ прочее

func openInExplorer(path string) error {
	if isFile(path) {
		cmd := exec.Command("explorer.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + filepath.Clean(path) + `"`}
		return cmd.Start()
	}
	if isDir(path) {
		return shellExecute("open", path, "", "")
	}
	return errors.New("нет такого пути")
}

func messageBox(text string, isError bool) {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString("Zero Launcher")
	flags := uintptr(0x40)
	if isError {
		flags = 0x10
	}
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), flags|0x40000)
}

func findAppBrowser() string {
	var cands []string
	for _, exe := range []string{"msedge.exe", "chrome.exe"} {
		for _, hive := range []string{"HKLM", "HKCU"} {
			if p, ok := regGet(hive, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`+exe, ""); ok && p != "" {
				cands = append(cands, strings.Trim(p, `"`))
			}
		}
	}
	for _, pattern := range []string{`%ProgramFiles(x86)%\Microsoft\Edge\Application\msedge.exe`,
		`%ProgramFiles%\Microsoft\Edge\Application\msedge.exe`, `%ProgramFiles%\Google\Chrome\Application\chrome.exe`,
		`%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe`, `%LOCALAPPDATA%\Google\Chrome\Application\chrome.exe`} {
		cands = append(cands, expand(pattern)...)
	}
	for _, c := range cands {
		if isFile(c) {
			return c
		}
	}
	return ""
}

// openAppWindow открывает интерфейс отдельным окном Edge без вкладок и адресной строки.
var (
	procSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	procGetDpiForSystem       = user32.NewProc("GetDpiForSystem")
)

// windowSize — размер окна в логических пикселях: 90% рабочей области экрана,
// не меньше 1100×700 (если экран позволяет) и не больше 1680×1000.
func windowSize() (int, int) {
	var r struct{ Left, Top, Right, Bottom int32 }
	if ok, _, _ := procSystemParametersInfoW.Call(0x0030, 0, uintptr(unsafe.Pointer(&r)), 0); ok == 0 { // SPI_GETWORKAREA
		return 1440, 900
	}
	dpi := uintptr(96)
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d >= 96 {
			dpi = d
		}
	}
	workW := int(r.Right-r.Left) * 96 / int(dpi)
	workH := int(r.Bottom-r.Top) * 96 / int(dpi)
	if workW < 400 || workH < 300 {
		return 1440, 900
	}
	fit := func(work, share, lo, hi int) int {
		v := work * share / 100
		if v > hi {
			v = hi
		}
		if v < lo {
			v = lo
		}
		if v > work {
			v = work
		}
		return v
	}
	return fit(workW, 90, 1100, 1680), fit(workH, 92, 700, 1000)
}

func openAppWindow(url, profile string) *exec.Cmd {
	browser := findAppBrowser()
	if browser == "" {
		_ = shellExecute("open", url, "", "")
		return nil
	}
	w, h := windowSize()
	cmd := exec.Command(browser, "--app="+url, "--user-data-dir="+profile, fmt.Sprintf("--window-size=%d,%d", w, h),
		"--no-first-run", "--no-default-browser-check",
		// окно лаунчера, а не браузер: без перевода, боковой панели, жестов «назад» и масштабирования щипком
		"--disable-features=Translate,msEdgeSidebar,msEdgeMouseGestureSupported,msEdgeMouseGestureDefaultEnabled,msUndersideButton",
		"--disable-pinch", "--overscroll-history-navigation=0",
		"--disable-background-mode", "--disable-extensions")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		logger.Printf("Не удалось открыть окно: %v", err)
		_ = shellExecute("open", url, "", "")
		return nil
	}
	return cmd
}

// ------------------------------------------------------------------ приложения Microsoft Store

const psAppxLocation = `
$name = $env:IGN_APPX_NAME
$pkg = Get-AppxPackage -Name $name -ErrorAction SilentlyContinue | Sort-Object Version -Descending | Select-Object -First 1
if ($pkg) { [Console]::Out.Write($pkg.InstallLocation) }
`

var (
	appxMu    sync.Mutex
	appxCache = map[string]appxCacheEntry{}
)

type appxCacheEntry struct {
	dir string
	at  time.Time
}

// appxInstallLocation — папка пакета через PowerShell, если в реестре её не нашлось.
// Ответ (и промах) помнится 15 минут, чтобы не запускать PowerShell при каждой проверке картинок.
func appxInstallLocation(family string) string {
	name := family
	if i := strings.LastIndex(family, "_"); i > 0 {
		name = family[:i]
	}
	appxMu.Lock()
	if e, ok := appxCache[name]; ok && time.Since(e.at) < 15*time.Minute {
		appxMu.Unlock()
		return e.dir
	}
	appxMu.Unlock()
	out, err := powershell(psAppxLocation, map[string]string{"IGN_APPX_NAME": name})
	dir := strings.TrimSpace(out)
	if err != nil || !isDir(dir) {
		dir = ""
	}
	appxMu.Lock()
	appxCache[name] = appxCacheEntry{dir, time.Now()}
	appxMu.Unlock()
	return dir
}
