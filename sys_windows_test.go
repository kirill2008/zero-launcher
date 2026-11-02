//go:build windows

package main

// Проверки, которые имеют смысл только на настоящей Windows: реестр, запись сервера RAGE MP,
// процессы, ярлыки .lnk и иконки системных программ. Имена начинаются с TestWin —
// в CI на windows-latest запускаются именно они: go test -run ^TestWin ./...

import (
	"bytes"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

var (
	procRegDeleteTreeW = advapi32.NewProc("RegDeleteTreeW")
	procRegDeleteKeyW  = advapi32.NewProc("RegDeleteKeyW")
)

// winTestKey — временный ключ HKCU\Software\ZeroLauncherTest\…, удаляется после теста.
func winTestKey(t *testing.T) string {
	key := `Software\ZeroLauncherTest\` + newID("t")
	t.Cleanup(func() {
		k, _ := syscall.UTF16PtrFromString(key)
		procRegDeleteTreeW.Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(k)))
		p, _ := syscall.UTF16PtrFromString(`Software\ZeroLauncherTest`)
		procRegDeleteKeyW.Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(p))) // удалится, только если пустой
	})
	return key
}

func winCreateKey(t *testing.T, key string) syscall.Handle {
	k, _ := syscall.UTF16PtrFromString(key)
	var h syscall.Handle
	r, _, _ := procRegCreateKeyExW.Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(k)), 0, 0, 0,
		uintptr(syscall.KEY_READ|0x0002|0x0004), 0, uintptr(unsafe.Pointer(&h)), 0)
	if r != 0 {
		t.Fatalf("ключ %s: %v", key, syscall.Errno(r))
	}
	return h
}

func TestWinRageConnectRegistry(t *testing.T) {
	key := winTestKey(t)
	old := rageRegKey
	rageRegKey = key
	t.Cleanup(func() { rageRegKey = old })

	if err := rageConnect("blaine.gta5rp.com:22005"); err != nil {
		t.Fatal(err)
	}
	ip, _ := regGet("HKCU", key, "launch2.ip")
	port, _ := regGet("HKCU", key, "launch2.port")
	if ip != "blaine.gta5rp.com" || port != "22005" {
		t.Fatalf("записано: %q %q", ip, port)
	}

	// официальный лаунчер хранит порт числом — тип значения сохраняется
	h := winCreateKey(t, key)
	if err := regSetDWORD(h, "launch2.port", 1); err != nil {
		t.Fatal(err)
	}
	syscall.RegCloseKey(h)
	if err := rageConnect("redwood.gta5rp.com:22006"); err != nil {
		t.Fatal(err)
	}
	h = winCreateKey(t, key)
	defer syscall.RegCloseKey(h)
	var typ, val, size uint32 = 0, 0, 4
	n, _ := syscall.UTF16PtrFromString("launch2.port")
	if err := syscall.RegQueryValueEx(h, n, nil, &typ, (*byte)(unsafe.Pointer(&val)), &size); err != nil || typ != syscall.REG_DWORD || val != 22006 {
		t.Errorf("порт числом: тип %d, значение %d, ошибка %v", typ, val, err)
	}

	// пустой адрес убирает автоподключение
	if err := rageConnect(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := regGet("HKCU", key, "launch2.ip"); ok {
		t.Error("launch2.ip не удалён")
	}
}

func TestWinRegSubkeys(t *testing.T) {
	key := winTestKey(t)
	for _, sub := range []string{"Alpha", "Beta"} {
		syscall.RegCloseKey(winCreateKey(t, key+`\`+sub))
	}
	got := strings.Join(regSubkeys("HKCU", key), ",")
	if !strings.Contains(got, "Alpha") || !strings.Contains(got, "Beta") {
		t.Errorf("подключи: %s", got)
	}
}

func TestWinDrivesAndProcesses(t *testing.T) {
	drives := fixedDrives()
	if len(drives) == 0 {
		t.Fatal("нет ни одного диска")
	}
	for _, d := range drives {
		if len(d) != 2 || d[1] != ':' {
			t.Errorf("диск %q", d)
		}
	}
	self := filepath.Base(os.Args[0])
	if !NewProbe(nil).IsRunning(&Launcher{Processes: []string{self}}, nil) {
		t.Errorf("текущий процесс %s не найден в списке процессов", self)
	}
}

func TestWinShortcutAndDllIcon(t *testing.T) {
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32")
	notepad := filepath.Join(sys, "notepad.exe")
	if !isFile(notepad) {
		notepad = filepath.Join(os.Getenv("SystemRoot"), "notepad.exe")
	}
	if raw, err := exeIconPNG(filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")); err != nil {
		t.Errorf("иконка explorer.exe: %v", err)
	} else if _, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil {
		t.Errorf("иконка explorer.exe не PNG: %v", err)
	}

	lnk := filepath.Join(t.TempDir(), "Тест.lnk")
	script := `$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:IGN_LNK)
$s.TargetPath = $env:IGN_TARGET
$s.Arguments = '-test'
$s.IconLocation = '%SystemRoot%\System32\shell32.dll,3'
$s.Save()`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "IGN_LNK="+lnk, "IGN_TARGET="+notepad)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("PowerShell не создал ярлык: %v %s", err, out)
	}
	l := parseLnk(lnk)
	if l == nil || !strings.EqualFold(l.Target, notepad) || l.Args != "-test" {
		t.Fatalf("ярлык: %+v", l)
	}
	if !strings.EqualFold(filepath.Base(l.IconPath), "shell32.dll") || l.IconIndex != 3 {
		t.Fatalf("значок ярлыка: %q,%d", l.IconPath, l.IconIndex)
	}
	src := iconSourcesForPath(lnk, 0)
	if len(src) == 0 || src[0].index != 3 {
		t.Fatalf("источники: %+v", src)
	}
	if _, _, err := loadArtFile(src[0]); err != nil {
		t.Errorf("значок shell32.dll,3: %v", err)
	}
}

func TestWinStoreAppIcon(t *testing.T) {
	// Калькулятор есть почти в каждой Windows 10/11; на серверных сборках без Store тест пропускается.
	root := appxRootLookup("Microsoft.WindowsCalculator_8wekyb3d8bbwe")
	if root == "" {
		t.Skip("Калькулятор из Microsoft Store не установлен")
	}
	src := appxSources(`shell:AppsFolder\Microsoft.WindowsCalculator_8wekyb3d8bbwe!App`, "icon")
	if len(src) == 0 {
		t.Fatalf("нет картинок в пакете %s", root)
	}
	if _, _, err := loadArtFile(src[0]); err != nil {
		t.Errorf("картинка %s: %v", src[0].path, err)
	}
}
