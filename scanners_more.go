package main

// Поиск игр Xbox / Microsoft Store (Game Pass), EA app и Battle.net.

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ------------------------------------------------------------------ установленные программы

type uninstallEntry struct {
	Key, Name, Publisher, InstallLocation, DisplayIcon, UninstallString string
}

// uninstallEntries — «Программы и компоненты» (HKLM и HKCU, 32 и 64 бита). В тестах подменяется.
var uninstallEntries = func() []uninstallEntry {
	const un = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	var out []uninstallEntry
	seen := map[string]bool{}
	for _, hive := range []string{"HKLM", "HKCU"} {
		for _, sub := range regSubkeys(hive, un) {
			k := un + `\` + sub
			get := func(v string) string { s, _ := regGet(hive, k, v); return strings.TrimSpace(s) }
			e := uninstallEntry{Key: sub, Name: get("DisplayName"), Publisher: get("Publisher"),
				InstallLocation: strings.Trim(get("InstallLocation"), `"`), DisplayIcon: get("DisplayIcon"),
				UninstallString: get("UninstallString")}
			if e.Name == "" || seen[strings.ToLower(hive+`\`+sub)] {
				continue
			}
			seen[strings.ToLower(hive+`\`+sub)] = true
			out = append(out, e)
		}
	}
	return out
}

// iconExe: из DisplayIcon вида "C:\Games\x.exe,0" — путь к .exe, если файл есть.
func iconExe(displayIcon string) string {
	p, _ := splitIconLocation(displayIcon)
	p = expandWin(p)
	if strings.EqualFold(filepath.Ext(p), ".exe") && isFile(p) && !exeSkip.MatchString(baseName(p)) {
		return p
	}
	return ""
}

// ------------------------------------------------------------------ EA app

var eaSkipRe = regexp.MustCompile(`(?i)^(ea app|ea desktop|origin\b|ea anti ?cheat|ea javelin|ea background)`)

func scanEA(ctx *scanCtx) []*Found {
	var out []*Found
	for _, e := range uninstallEntries() {
		pub, un := strings.ToLower(e.Publisher), strings.ToLower(e.UninstallString)
		isEA := strings.Contains(pub, "electronic arts") || strings.Contains(un, "eainstaller") ||
			strings.Contains(un, `\origin games\`) || strings.Contains(un, `\ea games\`)
		if !isEA || eaSkipRe.MatchString(e.Name) {
			continue
		}
		dir := expandWin(e.InstallLocation)
		if dir == "" || !isDir(dir) {
			continue
		}
		exe := bestExe(dir)
		if exe == "" {
			continue
		}
		icon := iconExe(e.DisplayIcon)
		if icon == "" {
			icon = exe
		}
		out = append(out, (&Found{Name: e.Name, Target: exe, Workdir: filepath.Dir(exe), Platform: "ea", Online: true,
			Requires: []string{"ea"}, InstallDir: dir, IconSrc: icon, SourceID: "ea:" + norm(dir)}).finish())
	}
	return out
}

// ------------------------------------------------------------------ Battle.net

// Коды игр для команды Battle.net.exe --exec="launch <код>" по идентификатору из деинсталлятора (--uid=…).
var bnetLaunchCodes = map[string]string{
	"prometheus": "Pro", "wow": "WoW", "diablo3": "D3", "fenris": "Fen", "osi": "OSI", "s2": "S2", "s1": "S1",
	"heroes": "Hero", "hs_beta": "WTCG", "w3": "W3", "viper": "VIPR", "odin": "ODIN", "lazarus": "LAZR",
	"zeus": "ZEUS", "fore": "FORE", "auks": "AUKS", "anbs": "ANBS", "wlby": "WLBY", "rtro": "RTRO",
}

var bnetUIDRe = regexp.MustCompile(`--uid=([A-Za-z0-9_\-]+)`)

func scanBattlenet(ctx *scanCtx) []*Found {
	var out []*Found
	for _, e := range uninstallEntries() {
		un := strings.ToLower(e.UninstallString)
		pub := strings.ToLower(e.Publisher)
		if !strings.Contains(un, "battle.net") && !strings.Contains(pub, "blizzard") {
			continue
		}
		m := bnetUIDRe.FindStringSubmatch(e.UninstallString)
		uid := ""
		if m != nil {
			uid = strings.ToLower(m[1])
		}
		if uid == "battle.net" || strings.EqualFold(e.Name, "Battle.net") {
			continue // сам лаунчер
		}
		dir := expandWin(e.InstallLocation)
		if dir == "" || !isDir(dir) {
			continue
		}
		exe := bestExe(dir)
		icon := iconExe(e.DisplayIcon)
		if icon == "" {
			icon = exe
		}
		f := &Found{Name: e.Name, Platform: "battlenet", Online: true, Requires: []string{"battlenet"},
			InstallDir: dir, IconSrc: icon, SourceID: "battlenet:" + uid}
		if uid == "" {
			f.SourceID = "battlenet:" + norm(dir)
		}
		if code := bnetLaunchCodes[uid]; code != "" {
			// игры Battle.net запускаются только через сам клиент — так же, как кнопка «Играть» в нём
			f.Target, f.Args = "{battlenet}", `--exec="launch `+code+`"`
		} else if exe != "" {
			f.Target, f.Workdir = exe, filepath.Dir(exe)
		} else {
			continue
		}
		out = append(out, f.finish())
	}
	return out
}

// ------------------------------------------------------------------ Xbox и Microsoft Store (Game Pass)

// xboxRoots — папки, куда приложение Xbox ставит игры: XboxGames на каждом диске
// и папки из файла .GamingRoot (если пользователь выбрал свою).
func xboxRoots() []string {
	var roots []string
	roots = append(roots, expand(`{drive}\XboxGames`)...)
	for _, gr := range expand(`{drive}\.GamingRoot`) {
		raw, err := os.ReadFile(gr)
		if err != nil || len(raw) < 10 || string(raw[:4]) != "RGBX" {
			continue
		}
		drive := filepath.Dir(gr)
		for _, part := range strings.Split(decodeUTF16(raw[8:]), "\x00") {
			part = strings.Trim(strings.TrimSpace(part), `\/`)
			if part != "" {
				roots = append(roots, filepath.Join(drive, part))
			}
		}
	}
	return uniq(roots)
}

var msResourceRe = regexp.MustCompile(`(?i)^ms-resource:`)

func scanXbox(ctx *scanCtx) []*Found {
	var out []*Found
	for _, root := range xboxRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			content := filepath.Join(root, e.Name(), "Content")
			name, publisher, appID, display := xboxIdentity(content)
			if name == "" || publisher == "" {
				continue
			}
			if display == "" || msResourceRe.MatchString(display) {
				display = e.Name()
			}
			family := name + "_" + publisherID(publisher)
			out = append(out, (&Found{Name: display, Target: `shell:AppsFolder\` + family + "!" + appID, Platform: "xbox",
				InstallDir: content, SourceID: "xbox:" + strings.ToLower(family)}).finish())
		}
	}
	return out
}

// xboxIdentity читает имя пакета, издателя и id приложения из AppxManifest.xml или MicrosoftGame.config.
func xboxIdentity(content string) (name, publisher, appID, display string) {
	if f, err := os.Open(filepath.Join(content, "MicrosoftGame.config")); err == nil {
		g := parseGameConfig(io.LimitReader(f, 4<<20))
		f.Close()
		name, publisher, display = g.Identity.Name, g.Identity.Publisher, g.Visuals.DisplayName
		for _, x := range g.Executables {
			if x.ID != "" {
				appID = x.ID
				break
			}
		}
	}
	if f, err := os.Open(filepath.Join(content, "AppxManifest.xml")); err == nil {
		n, p, id, d := parseAppxIdentity(io.LimitReader(f, 4<<20))
		f.Close()
		if n != "" {
			name, publisher = n, p
		}
		if id != "" {
			appID = id
		}
		if display == "" {
			display = d
		}
	}
	if appID == "" {
		appID = "Game"
	}
	return
}
