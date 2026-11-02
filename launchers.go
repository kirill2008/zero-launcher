package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Имена .exe, которые встречаются у разных программ. Для них сверяем ещё и папку.
var genericExe = map[string]bool{
	"launcher.exe": true, "updater.exe": true, "launch.exe": true, "start.exe": true, "game.exe": true,
	"client.exe": true, "play.exe": true, "run.exe": true, "app.exe": true,
}

// Порядок в цепочке: магазины → Social Club → клиенты модов → свои лаунчеры.
var groupOrder = map[string]int{"store": 0, "rockstar": 1, "mod": 2, "custom": 3}

type regEntry struct {
	Hive, Key, Value string
	Suffix           []string // имена .exe внутри найденной папки; пусто — значение уже путь к .exe
}

type launcherDef struct {
	ID, Name, Group, Desc string
	Delay                 int
	Processes             []string
	Reg                   []regEntry
	Protocol              string
	Special               string
	Paths                 []string
}

var builtinLaunchers = []launcherDef{
	{
		ID: "steam", Name: "Steam", Group: "store", Delay: 5,
		Processes: []string{"steam.exe"},
		Desc:      "Игры из Steam: PUBG, CS2, Dota 2, GTA V из Steam.",
		Reg: []regEntry{{"HKCU", `Software\Valve\Steam`, "SteamExe", nil},
			{"HKLM", `SOFTWARE\Valve\Steam`, "InstallPath", []string{"steam.exe"}}},
		Protocol: "steam",
		Paths: []string{`%ProgramFiles(x86)%\Steam\steam.exe`, `%ProgramFiles%\Steam\steam.exe`,
			`{drive}\Steam\steam.exe`, `{drive}\Program Files (x86)\Steam\steam.exe`},
	},
	{
		ID: "epic", Name: "Epic Games", Group: "store", Delay: 6,
		Processes: []string{"EpicGamesLauncher.exe"},
		Desc:      "Игры из Epic Games Store: Fortnite, GTA V из Epic.",
		Protocol:  "com.epicgames.launcher",
		Paths: []string{`%ProgramFiles(x86)%\Epic Games\Launcher\Portal\Binaries\Win64\EpicGamesLauncher.exe`,
			`%ProgramFiles(x86)%\Epic Games\Launcher\Portal\Binaries\Win32\EpicGamesLauncher.exe`,
			`%ProgramFiles%\Epic Games\Launcher\Portal\Binaries\Win64\EpicGamesLauncher.exe`},
	},
	{
		ID: "socialclub", Name: "Social Club", Group: "rockstar", Delay: 10,
		Processes: []string{"SocialClubHelper.exe", "RockstarService.exe", "Launcher.exe"},
		Desc:      "Вход в Rockstar Social Club через Rockstar Games Launcher. Для GTA запускается всегда.",
		Reg:       []regEntry{{"HKLM", `SOFTWARE\Rockstar Games\Launcher`, "InstallFolder", []string{"Launcher.exe"}}},
		Paths: []string{`%ProgramFiles%\Rockstar Games\Launcher\Launcher.exe`,
			`{drive}\Program Files\Rockstar Games\Launcher\Launcher.exe`, `{drive}\Rockstar Games\Launcher\Launcher.exe`},
	},
	{
		ID: "rage", Name: "RAGE Multiplayer", Group: "mod", Delay: 0,
		Processes: []string{"ragemp_v.exe", "updater.exe"},
		Desc:      "Клиент для серверов GTA 5 RP. Zero Launcher передаёт ему выбранный сервер, и игра сразу подключается.",
		Reg:       []regEntry{{"HKCU", `Software\RAGE-MP`, "rage_path", []string{"updater.exe"}}},
		Paths: []string{`{drive}\RAGEMP\updater.exe`, `{drive}\Games\RAGEMP\updater.exe`,
			`%ProgramFiles%\RAGEMP\updater.exe`},
	},
	{
		ID: "gta5rp", Name: "Лаунчер GTA5RP", Group: "mod", Delay: 0,
		Processes: []string{"GTA5RPLauncher.exe", "GTA5RP Launcher.exe"},
		Desc:      "Официальный лаунчер проекта GTA5RP. Нужен, если GTA 5 RP запускается через него.",
		Special:   "gta5rp",
		Paths: []string{`%LOCALAPPDATA%\Programs\GTA5RP Launcher\GTA5RPLauncher.exe`,
			`%LOCALAPPDATA%\Programs\gta5rp-launcher\GTA5RPLauncher.exe`,
			`%LOCALAPPDATA%\GTA5RP Launcher\GTA5RPLauncher.exe`,
			`%ProgramFiles%\GTA5RP Launcher\GTA5RPLauncher.exe`, `%ProgramFiles(x86)%\GTA5RP Launcher\GTA5RPLauncher.exe`,
			`{drive}\GTA5RP Launcher\GTA5RPLauncher.exe`, `{drive}\GTA5RP\GTA5RPLauncher.exe`,
			`{drive}\Games\GTA5RP Launcher\GTA5RPLauncher.exe`, `{drive}\Games\GTA5RP\GTA5RPLauncher.exe`},
	},
	{
		ID: "riot", Name: "Riot Client", Group: "store", Delay: 3,
		Processes: []string{"RiotClientServices.exe", "Riot Client.exe", "RiotClientUx.exe"},
		Desc:      "VALORANT и League of Legends.",
		Special:   "riot",
		Paths: []string{`{drive}\Riot Games\Riot Client\RiotClientServices.exe`,
			`%ProgramFiles%\Riot Games\Riot Client\RiotClientServices.exe`},
	},
	{
		ID: "ea", Name: "EA app", Group: "store", Delay: 6,
		Processes: []string{"EADesktop.exe"},
		Desc:      "Игры EA: Apex Legends из EA, Battlefield, FC.",
		Reg:       []regEntry{{"HKLM", `SOFTWARE\Electronic Arts\EA Desktop`, "DesktopAppPath", nil}},
		Paths: []string{`%ProgramFiles%\Electronic Arts\EA Desktop\EA Desktop\EADesktop.exe`,
			`%ProgramFiles(x86)%\Electronic Arts\EA Desktop\EA Desktop\EADesktop.exe`},
	},
	{
		ID: "ubisoft", Name: "Ubisoft Connect", Group: "store", Delay: 5,
		Processes: []string{"UbisoftConnect.exe", "upc.exe"},
		Desc:      "Игры Ubisoft.",
		Reg:       []regEntry{{"HKLM", `SOFTWARE\Ubisoft\Launcher`, "InstallDir", []string{"UbisoftConnect.exe", "upc.exe"}}},
		Protocol:  "uplay",
		Paths: []string{`%ProgramFiles(x86)%\Ubisoft\Ubisoft Game Launcher\UbisoftConnect.exe`,
			`%ProgramFiles(x86)%\Ubisoft\Ubisoft Game Launcher\upc.exe`},
	},
	{
		ID: "battlenet", Name: "Battle.net", Group: "store", Delay: 6,
		Processes: []string{"Battle.net.exe"},
		Desc:      "Игры Blizzard и Call of Duty.",
		Protocol:  "battlenet",
		Paths: []string{`%ProgramFiles(x86)%\Battle.net\Battle.net.exe`,
			`%ProgramFiles(x86)%\Battle.net\Battle.net Launcher.exe`},
	},
	{
		ID: "gog", Name: "GOG Galaxy", Group: "store", Delay: 4,
		Processes: []string{"GalaxyClient.exe"},
		Desc:      "Игры из GOG, если нужен онлайн через Galaxy.",
		Reg:       []regEntry{{"HKLM", `SOFTWARE\GOG.com\GalaxyClient\paths`, "client", []string{"GalaxyClient.exe"}}},
		Paths:     []string{`%ProgramFiles(x86)%\GOG Galaxy\GalaxyClient.exe`},
	},
}

func builtinByID(id string) *launcherDef {
	for i := range builtinLaunchers {
		if builtinLaunchers[i].ID == id {
			return &builtinLaunchers[i]
		}
	}
	return nil
}

// ------------------------------------------------------------------ поиск

func riotClientPath() string {
	for _, p := range expand(`%ProgramData%\Riot Games\RiotClientInstalls.json`) {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var data map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(string(raw), bom)), &data) != nil {
			continue
		}
		for _, k := range []string{"rc_default", "rc_live", "rc_beta"} {
			if c := toStr(data[k]); c != "" {
				c = filepath.Clean(c)
				if isFile(c) {
					return c
				}
			}
		}
	}
	return ""
}

var gta5rpRe = regexp.MustCompile(`(?i)gta\s*5\s*rp|gta5rp`)

// gta5rpLauncherPath ищет официальный лаунчер GTA5RP: список установленных программ,
// затем ярлыки в меню «Пуск» (ярлык тоже подходит — Windows сама его откроет).
func gta5rpLauncherPath() string {
	const un = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	for _, hive := range []string{"HKCU", "HKLM"} {
		for _, sub := range regSubkeys(hive, un) {
			name, _ := regGet(hive, un+`\`+sub, "DisplayName")
			if !gta5rpRe.MatchString(name) && !gta5rpRe.MatchString(sub) {
				continue
			}
			if icon, ok := regGet(hive, un+`\`+sub, "DisplayIcon"); ok {
				icon = strings.Trim(strings.TrimSpace(icon), `"`)
				if i := strings.LastIndex(icon, ","); i > 0 && !strings.HasSuffix(strings.ToLower(icon), ".exe") {
					icon = icon[:i]
				}
				if strings.HasSuffix(strings.ToLower(icon), ".exe") && isFile(icon) && !strings.Contains(strings.ToLower(baseName(icon)), "unins") {
					return icon
				}
			}
			if dir, ok := regGet(hive, un+`\`+sub, "InstallLocation"); ok && isDir(dir) {
				if p := launcherExeIn(dir); p != "" {
					return p
				}
			}
		}
	}
	for _, root := range append(expand(`%APPDATA%\Microsoft\Windows\Start Menu\Programs`),
		expand(`%ProgramData%\Microsoft\Windows\Start Menu\Programs`)...) {
		hit := ""
		depth := strings.Count(filepath.Clean(root), string(filepath.Separator))
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && strings.Count(filepath.Clean(p), string(filepath.Separator))-depth > 2 {
				return fs.SkipDir
			}
			n := strings.ToLower(d.Name())
			if !d.IsDir() && strings.HasSuffix(n, ".lnk") && gta5rpRe.MatchString(n) && !strings.Contains(n, "unins") {
				hit = p
				return fs.SkipAll
			}
			return nil
		})
		if hit != "" {
			return hit
		}
	}
	return ""
}

// launcherExeIn выбирает .exe лаунчера в папке установки.
func launcherExeIn(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	fallback := ""
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if e.IsDir() || !strings.HasSuffix(n, ".exe") || strings.Contains(n, "unins") || strings.Contains(n, "update") {
			continue
		}
		if strings.Contains(n, "launcher") || gta5rpRe.MatchString(n) {
			return filepath.Join(dir, e.Name())
		}
		if fallback == "" {
			fallback = filepath.Join(dir, e.Name())
		}
	}
	return fallback
}

// detect ищет .exe лаунчера: реестр → обработчик ссылок → типовые папки.
func detect(d *launcherDef) string {
	if d.Special == "riot" {
		if p := riotClientPath(); p != "" {
			return p
		}
	}
	if d.Special == "gta5rp" {
		if p := gta5rpLauncherPath(); p != "" {
			return p
		}
	}
	for _, r := range d.Reg {
		v, ok := regGet(r.Hive, r.Key, r.Value)
		if !ok || v == "" {
			continue
		}
		v = filepath.Clean(v)
		cands := []string{v}
		if len(r.Suffix) > 0 {
			cands = nil
			for _, s := range r.Suffix {
				cands = append(cands, filepath.Join(v, s))
			}
		}
		for _, c := range cands {
			if isFile(c) {
				return c
			}
		}
	}
	if d.Protocol != "" {
		if cmd, ok := regGet("HKCR", d.Protocol+`\shell\open\command`, ""); ok {
			if p := exeFromCommand(cmd); p != "" && isFile(p) {
				return filepath.Clean(p)
			}
		}
	}
	for _, pattern := range d.Paths {
		for _, c := range expand(pattern) {
			if isFile(c) {
				return filepath.Clean(c)
			}
		}
	}
	return ""
}

// detectMissing заполняет пути лаунчеров, которые пользователь не указывал сам.
func detectMissing(st *Store, force bool) []string {
	snap := st.Snapshot()
	found := map[string]string{}
	for i := range builtinLaunchers {
		d := &builtinLaunchers[i]
		o := snap.Launchers[d.ID]
		if o != nil && o.Path != "" {
			if !o.Auto {
				continue // путь указан руками — не трогаем
			}
			if !force && isFile(o.Path) {
				continue
			}
		}
		found[d.ID] = detect(d)
	}
	// Сначала сравниваем со снимком: config.json переписывается, только если путь действительно поменялся.
	type change struct{ id, path string }
	var todo []change
	for id, p := range found {
		cur, auto := "", false
		if o := snap.Launchers[id]; o != nil {
			cur, auto = o.Path, o.Auto
		}
		if p != "" && norm(p) != norm(cur) {
			todo = append(todo, change{id, p})
		} else if p == "" && cur != "" && auto && !isFile(cur) {
			todo = append(todo, change{id, ""}) // автоматически найденный лаунчер удалили
		}
	}
	changed := []string{}
	if len(todo) == 0 {
		return changed
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].id < todo[j].id })
	_ = st.Update(func(c *Config) error {
		for _, ch := range todo {
			o := c.Launchers[ch.id]
			if o == nil {
				o = &LauncherOverride{}
				c.Launchers[ch.id] = o
			}
			if o.Path != "" && !o.Auto {
				continue // пока искали, пользователь указал путь сам
			}
			o.Path, o.Auto = ch.path, true
			changed = append(changed, ch.id)
		}
		return nil
	})
	return changed
}

// ------------------------------------------------------------------ описание лаунчеров

type Launcher struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Group     string   `json:"group"`
	Builtin   bool     `json:"builtin"`
	Desc      string   `json:"desc"`
	Path      string   `json:"path"`
	Args      string   `json:"args"`
	Delay     int      `json:"delay"`
	Processes []string `json:"processes"`
	Auto      bool     `json:"auto"`
	Exists    bool     `json:"exists"`
	Running   bool     `json:"running"`
}

func resolveAll(st *Store) []*Launcher {
	snap := st.Snapshot()
	out := []*Launcher{}
	for i := range builtinLaunchers {
		d := &builtinLaunchers[i]
		o := snap.Launchers[d.ID]
		if o == nil {
			o = &LauncherOverride{}
		}
		delay := d.Delay
		if o.Delay != nil {
			delay = clamp(*o.Delay, 0, 120)
		}
		procs := append([]string{}, d.Processes...)
		for _, p := range o.ExtraProcesses {
			if !containsFold(procs, p) {
				procs = append(procs, p)
			}
		}
		path := strings.TrimSpace(o.Path)
		out = append(out, &Launcher{ID: d.ID, Name: d.Name, Group: d.Group, Builtin: true, Desc: d.Desc,
			Path: path, Args: strings.TrimSpace(o.Args), Delay: delay, Processes: procs, Auto: o.Auto,
			Exists: isFile(path)})
	}
	for _, c := range snap.CustomLaunchers {
		name := c.Name
		if name == "" {
			name = "Свой лаунчер"
		}
		procs := append([]string{}, c.Processes...)
		out = append(out, &Launcher{ID: c.ID, Name: name, Group: "custom", Desc: "Добавлен вами.",
			Path: strings.TrimSpace(c.Path), Args: strings.TrimSpace(c.Args), Delay: clamp(c.Delay, 0, 120),
			Processes: procs, Exists: isFile(strings.TrimSpace(c.Path))})
	}
	return out
}

func launchersByID(st *Store) map[string]*Launcher {
	m := map[string]*Launcher{}
	for _, l := range resolveAll(st) {
		m[l.ID] = l
	}
	return m
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func updateLauncher(st *Store, id string, fields map[string]any) bool {
	ok := false
	_ = st.Update(func(c *Config) error {
		if builtinByID(id) != nil {
			o := c.Launchers[id]
			if o == nil {
				o = &LauncherOverride{}
				c.Launchers[id] = o
			}
			if v, has := fields["path"]; has {
				o.Path = strings.Trim(strings.TrimSpace(toStr(v)), `"`)
				o.Auto = false
			}
			if v, has := fields["args"]; has {
				o.Args = strings.TrimSpace(toStr(v))
			}
			if v, has := fields["delay"]; has {
				if n, good := toInt(v); good {
					n = clamp(n, 0, 120)
					o.Delay = &n
				}
			}
			if v, has := fields["processes"]; has {
				o.ExtraProcesses = toStrSlice(v)
			}
			ok = true
			return nil
		}
		for _, cl := range c.CustomLaunchers {
			if cl.ID != id {
				continue
			}
			if v, has := fields["name"]; has {
				cl.Name = strings.TrimSpace(toStr(v))
			}
			if v, has := fields["path"]; has {
				cl.Path = strings.Trim(strings.TrimSpace(toStr(v)), `"`)
			}
			if v, has := fields["args"]; has {
				cl.Args = strings.TrimSpace(toStr(v))
			}
			if v, has := fields["delay"]; has {
				if n, good := toInt(v); good {
					cl.Delay = clamp(n, 0, 120)
				}
			}
			if v, has := fields["processes"]; has {
				cl.Processes = toStrSlice(v)
			}
			ok = true
		}
		return nil
	})
	return ok
}

func addCustomLauncher(st *Store, fields map[string]any) *CustomLauncher {
	path := strings.Trim(strings.TrimSpace(toStr(fields["path"])), `"`)
	name := strings.TrimSpace(toStr(fields["name"]))
	if name == "" {
		name = strings.TrimSuffix(baseName(path), filepath.Ext(baseName(path)))
	}
	if name == "" {
		name = "Свой лаунчер"
	}
	delay, ok := toInt(fields["delay"])
	if !ok {
		delay = 3
	}
	procs := toStrSlice(fields["processes"])
	if len(procs) == 0 && path != "" {
		procs = []string{baseName(path)}
	}
	cl := &CustomLauncher{ID: newID("l"), Name: name, Path: path, Args: strings.TrimSpace(toStr(fields["args"])),
		Delay: clamp(delay, 0, 120), Processes: procs}
	_ = st.Update(func(c *Config) error {
		c.CustomLaunchers = append(c.CustomLaunchers, cl)
		return nil
	})
	return cl
}

func deleteCustomLauncher(st *Store, id string) bool {
	removed := false
	_ = st.Update(func(c *Config) error {
		keep := []*CustomLauncher{}
		for _, cl := range c.CustomLaunchers {
			if cl.ID == id {
				removed = true
				continue
			}
			keep = append(keep, cl)
		}
		c.CustomLaunchers = keep
		for _, g := range c.Games {
			req := []string{}
			for _, r := range g.Requires {
				if r != id {
					req = append(req, r)
				}
			}
			g.Requires = req
		}
		return nil
	})
	return removed
}

// ------------------------------------------------------------------ процессы

type procInfo struct {
	Name string // в нижнем регистре
	Exe  string // полный путь — только для «общих» имён, иначе пусто
}

// ProcessProbe проверяет, запущен ли лаунчер. Список процессов кэшируется примерно на секунду.
type ProcessProbe struct {
	fn    func() []procInfo
	mu    sync.Mutex
	at    time.Time
	cache []procInfo
}

func NewProbe(fn func() []procInfo) *ProcessProbe {
	if fn == nil {
		fn = func() []procInfo {
			p, err := snapshotProcs()
			if err != nil {
				logger.Printf("Список процессов: %v", err)
			}
			return p
		}
	}
	return &ProcessProbe{fn: fn}
}

func (p *ProcessProbe) Snapshot() []procInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.at) < 900*time.Millisecond && p.cache != nil {
		return p.cache
	}
	p.cache = p.fn()
	if p.cache == nil {
		p.cache = []procInfo{}
	}
	p.at = time.Now()
	return p.cache
}

func (p *ProcessProbe) Invalidate() {
	p.mu.Lock()
	p.at = time.Time{}
	p.mu.Unlock()
}

func (p *ProcessProbe) IsRunning(l *Launcher, snap []procInfo) bool {
	if snap == nil {
		snap = p.Snapshot()
	}
	base, folder := "", ""
	if l.Path != "" {
		base = strings.ToLower(baseName(l.Path))
		folder = norm(filepath.Dir(l.Path))
	}
	names := map[string]bool{}
	for _, n := range l.Processes {
		names[strings.ToLower(n)] = true
	}
	if base != "" {
		names[base] = true
	}
	for _, pr := range snap {
		if !names[pr.Name] {
			continue
		}
		if !genericExe[pr.Name] {
			return true
		}
		// «Launcher.exe» есть у многих программ — сверяем папку.
		if pr.Exe != "" && folder != "" && norm(filepath.Dir(pr.Exe)) == folder {
			return true
		}
	}
	return false
}
