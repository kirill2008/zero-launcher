package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Found — игра, найденная при сканировании.
type Found struct {
	Key        string   `json:"key"`
	Source     string   `json:"source"`
	Name       string   `json:"name"`
	Target     string   `json:"target"`
	Args       string   `json:"args"`
	Workdir    string   `json:"workdir"`
	Platform   string   `json:"platform"`
	Online     bool     `json:"online"`
	Requires   []string `json:"requires"`
	InstallDir string   `json:"install_dir"`
	CoverSrc   string   `json:"cover_src"`
	IconSrc    string   `json:"icon_src"`
	SourceID   string   `json:"source_id"`
	InLibrary  bool     `json:"in_library"`
}

func (f *Found) finish() *Found {
	if f.Requires == nil {
		f.Requires = []string{}
	}
	if f.Platform == "" {
		f.Platform = "other"
	}
	if f.SourceID != "" {
		f.Key = f.SourceID
	} else {
		f.Key = "path:" + norm(f.Target) + "|" + f.Args
	}
	return f
}

var (
	steamSkipIDs = map[string]bool{"228980": true, "1070560": true, "1391110": true, "1628350": true,
		"1493710": true, "2180100": true, "250820": true, "1826330": true}
	steamSkipName = regexp.MustCompile(`(?i)redistributable|proton|steam linux runtime|steamworks|steamvr|dedicated server|sdk\b`)
	gtaSteamIDs   = map[string]bool{"271590": true, "3240220": true}
	gtaEpicApps   = map[string]bool{"9d2d0eb64d5c44529cece33fe2a46482": true}
	riotNames     = map[string]string{"valorant": "VALORANT", "league_of_legends": "League of Legends", "bacon": "Legends of Runeterra"}

	exeSkip = regexp.MustCompile(`(?i)unins|setup|install|crash|report|redist|vc_?redist|dxsetup|directx|dotnet|helper|cefprocess|` +
		`easyanticheat|eac_|battleye|beservice|ue4prereq|prereq|patcher|unitycrash|webhelper|` +
		`notification|update|config|settings|benchmark|server|editor|uploader|7z|python|java|` +
		`vc_|dxweb|elevate|overlay|bugreport|cleanup|repair|register|activation`)
	dirSkip = map[string]bool{"_commonredist": true, "redist": true, "redistributables": true, "directx": true,
		"__installer": true, "support": true, "engine": true, "easyanticheat": true, "battleye": true, "_redist": true,
		"dotnet": true, "vcredist": true, "prereqs": true, "crashreporter": true, "logs": true, "saves": true,
		"screenshots": true, "mods": true, "shadercache": true}
)

// ------------------------------------------------------------------ Steam

var vdfToken = regexp.MustCompile(`"((?:\\.|[^"\\])*)"|(\{)|(\})`)
var vdfEscape = regexp.MustCompile(`\\(.)`)

// parseVDF — минимальный разбор Valve KeyValues (libraryfolders.vdf, appmanifest_*.acf).
func parseVDF(text string) map[string]any {
	root := map[string]any{}
	stack := []map[string]any{root}
	key, haveKey := "", false
	for _, m := range vdfToken.FindAllStringSubmatch(text, -1) {
		switch {
		case m[2] != "":
			d := map[string]any{}
			if haveKey {
				stack[len(stack)-1][key] = d
			}
			stack = append(stack, d)
			haveKey = false
		case m[3] != "":
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			haveKey = false
		default:
			s := vdfEscape.ReplaceAllString(m[1], "$1")
			if !haveKey {
				key, haveKey = s, true
			} else {
				stack[len(stack)-1][key] = s
				haveKey = false
			}
		}
	}
	return root
}

func ci(d any, name string) any {
	m, ok := d.(map[string]any)
	if !ok {
		return nil
	}
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return nil
}

func steamRoot(launchers map[string]*Launcher) string {
	path := ""
	if l := launchers["steam"]; l != nil && l.Exists {
		path = l.Path
	}
	if path == "" {
		path = detect(builtinByID("steam"))
	}
	if path == "" {
		return ""
	}
	return filepath.Dir(path)
}

func steamLibraries(root string) []string {
	libs := []string{root}
	for _, vdf := range []string{filepath.Join(root, "steamapps", "libraryfolders.vdf"), filepath.Join(root, "config", "libraryfolders.vdf")} {
		raw, err := os.ReadFile(vdf)
		if err != nil {
			continue
		}
		lf, _ := ci(parseVDF(string(raw)), "libraryfolders").(map[string]any)
		keys := make([]string, 0, len(lf))
		for k := range lf {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := ""
			switch v := lf[k].(type) {
			case map[string]any:
				p = toStr(ci(v, "path"))
			case string:
				if _, err := json.Number(k).Int64(); err == nil {
					p = v
				}
			}
			if p != "" {
				libs = append(libs, filepath.Clean(p))
			}
		}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, l := range libs {
		if !seen[norm(l)] && isDir(filepath.Join(l, "steamapps")) {
			seen[norm(l)] = true
			out = append(out, l)
		}
	}
	return out
}

func steamCover(root, appid string) string {
	if root == "" {
		return ""
	}
	cache := filepath.Join(root, "appcache", "librarycache")
	for _, c := range []string{filepath.Join(cache, appid+"_library_600x900.jpg"), filepath.Join(cache, appid, "library_600x900.jpg")} {
		if isFile(c) {
			return c
		}
	}
	hit := ""
	_ = filepath.WalkDir(filepath.Join(cache, appid), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasPrefix(strings.ToLower(d.Name()), "library_600x900") && strings.HasSuffix(strings.ToLower(d.Name()), ".jpg") {
			hit = p
			return fs.SkipAll
		}
		return nil
	})
	if hit != "" {
		return hit
	}
	for _, c := range []string{filepath.Join(cache, appid+"_header.jpg"), filepath.Join(cache, appid, "header.jpg")} {
		if isFile(c) {
			return c
		}
	}
	return ""
}

type scanCtx struct {
	launchers map[string]*Launcher
	gtaStore  string
}

// steamInstalled: в StateFlags стоит бит 4 (StateFullyInstalled). Игры, которые ещё качаются (например, 1026)
// или удаляются, в библиотеку не попадают. Старые манифесты без StateFlags не отбрасываем.
func steamInstalled(state any) bool {
	f := strings.TrimSpace(toStr(ci(state, "StateFlags")))
	if f == "" {
		return true
	}
	n, err := strconv.ParseUint(f, 10, 64)
	if err != nil {
		return true
	}
	return n&4 != 0
}

func scanSteam(ctx *scanCtx) []*Found {
	root := steamRoot(ctx.launchers)
	if root == "" {
		return nil
	}
	var out []*Found
	for _, lib := range steamLibraries(root) {
		dir := filepath.Join(lib, "steamapps")
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			n := e.Name()
			if !strings.HasPrefix(n, "appmanifest_") || !strings.HasSuffix(n, ".acf") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				continue
			}
			stt := ci(parseVDF(string(raw)), "AppState")
			appid, name := toStr(ci(stt, "appid")), toStr(ci(stt, "name"))
			if appid == "" || steamSkipIDs[appid] || steamSkipName.MatchString(name) || !steamInstalled(stt) {
				continue
			}
			if name == "" {
				name = "Steam " + appid
			}
			req := []string{"steam"}
			if gtaSteamIDs[appid] {
				req = append(req, "socialclub")
			}
			out = append(out, (&Found{Name: name, Target: "steam://rungameid/" + appid, Platform: "steam", Online: true,
				Requires: req, InstallDir: filepath.Join(dir, "common", toStr(ci(stt, "installdir"))),
				CoverSrc: steamCover(root, appid), SourceID: "steam:" + appid}).finish())
		}
	}
	return out
}

// ------------------------------------------------------------------ Epic Games

func scanEpic(ctx *scanCtx) []*Found {
	var out []*Found
	for _, mdir := range expand(`%ProgramData%\Epic\EpicGamesLauncher\Data\Manifests`) {
		entries, _ := os.ReadDir(mdir)
		for _, e := range entries {
			if !strings.HasSuffix(strings.ToLower(e.Name()), ".item") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(mdir, e.Name()))
			if err != nil {
				continue
			}
			var m map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(string(raw), bom)), &m) != nil {
				continue
			}
			app := toStr(m["AppName"])
			main := toStr(m["MainGameAppName"])
			if main == "" {
				main = app
			}
			cats := []string{}
			for _, c := range toStrSlice(m["AppCategories"]) {
				cats = append(cats, strings.ToLower(c))
			}
			if app == "" || app != main || strings.HasPrefix(app, "UE_") || (len(cats) > 0 && !contains(cats, "games")) {
				continue
			}
			if toBool(m["bIsIncompleteInstall"]) {
				continue
			}
			loc := toStr(m["InstallLocation"])
			exe := ""
			if loc != "" && toStr(m["LaunchExecutable"]) != "" {
				exe = filepath.Join(loc, toStr(m["LaunchExecutable"]))
			}
			if !isFile(exe) {
				exe = ""
			}
			ns, cid := toStr(m["CatalogNamespace"]), toStr(m["CatalogItemId"])
			uriID := app
			if ns != "" && cid != "" {
				uriID = ns + "%3A" + cid + "%3A" + app
			}
			req := []string{"epic"}
			if gtaEpicApps[app] {
				req = append(req, "socialclub")
			}
			name := toStr(m["DisplayName"])
			if name == "" {
				name = app
			}
			out = append(out, (&Found{Name: name, Target: "com.epicgames.launcher://apps/" + uriID + "?action=launch&silent=true",
				Platform: "epic", Online: true, Requires: req, InstallDir: loc, IconSrc: exe, SourceID: "epic:" + app}).finish())
		}
	}
	return out
}

// ------------------------------------------------------------------ Riot

var riotPathRe = regexp.MustCompile(`product_install_full_path:\s*"?([^"\r\n]+)"?`)

func scanRiot(ctx *scanCtx) []*Found {
	var out []*Found
	for _, meta := range expand(`%ProgramData%\Riot Games\Metadata`) {
		entries, err := os.ReadDir(meta)
		if err != nil {
			continue
		}
		for _, e := range entries {
			d := e.Name()
			i := strings.Index(d, ".")
			if i < 0 {
				continue
			}
			product, patch := d[:i], d[i+1:]
			if product == "riot_client" {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(meta, d, d+".product_settings.yaml"))
			if err != nil {
				continue
			}
			install := ""
			if m := riotPathRe.FindStringSubmatch(string(raw)); m != nil {
				install = strings.TrimSpace(m[1])
			}
			name := riotNames[product]
			if name == "" {
				name = titleWords(strings.ReplaceAll(product, "_", " "))
			}
			out = append(out, (&Found{Name: name, Target: "{riot}",
				Args:     "--launch-product=" + product + " --launch-patchline=" + patch,
				Platform: "riot", Online: true, Requires: []string{"riot"}, InstallDir: install,
				SourceID: "riot:" + product + "." + patch}).finish())
		}
	}
	return out
}

// ------------------------------------------------------------------ Rockstar

var rockstarSkip = map[string]bool{"launcher": true, "social club": true, "rockstar games social club": true, "rockstar games launcher": true}

func rockstarExe(folder string) string {
	for _, n := range []string{"PlayGTAV.exe", "GTA5_Enhanced.exe", "GTA5.exe", "PlayRDR2.exe", "RDR2.exe", "LaunchGTAIV.exe"} {
		if p := filepath.Join(folder, n); isFile(p) {
			return p
		}
	}
	if entries, err := os.ReadDir(folder); err == nil {
		for _, e := range entries {
			n := strings.ToLower(e.Name())
			if strings.HasPrefix(n, "play") && strings.HasSuffix(n, ".exe") {
				return filepath.Join(folder, e.Name())
			}
		}
	}
	return bestExe(folder)
}

func rockstarFound(name, exe, folder string) *Found {
	return (&Found{Name: name, Target: exe, Platform: "rockstar", Online: true, Requires: []string{"socialclub"},
		InstallDir: folder, Workdir: folder, IconSrc: exe, SourceID: "rockstar:" + norm(folder)}).finish()
}

func scanRockstar(ctx *scanCtx) []*Found {
	var out []*Found
	seen := map[string]bool{}
	for _, sub := range regSubkeys("HKLM", `SOFTWARE\Rockstar Games`) {
		if rockstarSkip[strings.ToLower(sub)] {
			continue
		}
		folder, ok := regGet("HKLM", `SOFTWARE\Rockstar Games\`+sub, "InstallFolder")
		if !ok || !isDir(folder) {
			continue
		}
		if exe := rockstarExe(folder); exe != "" {
			seen[norm(folder)] = true
			out = append(out, rockstarFound(sub, exe, folder))
		}
	}
	for _, pattern := range []string{`%ProgramFiles%\Rockstar Games\Grand Theft Auto V`,
		`%ProgramFiles%\Rockstar Games\Grand Theft Auto V Enhanced`, `{drive}\Rockstar Games\Grand Theft Auto V`} {
		for _, folder := range expand(pattern) {
			if seen[norm(folder)] || !isDir(folder) {
				continue
			}
			if exe := rockstarExe(folder); exe != "" {
				seen[norm(folder)] = true
				out = append(out, rockstarFound(baseName(folder), exe, folder))
			}
		}
	}
	return out
}

// ------------------------------------------------------------------ RAGE MP (GTA 5 RP)

func scanRage(ctx *scanCtx) []*Found {
	path := ""
	if l := ctx.launchers["rage"]; l != nil && l.Exists {
		path = l.Path
	}
	if path == "" {
		path = detect(builtinByID("rage"))
	}
	if path == "" {
		return nil
	}
	req := []string{"socialclub"}
	if ctx.gtaStore != "" {
		req = append([]string{ctx.gtaStore}, req...)
	}
	return []*Found{(&Found{Name: "GTA 5 RP", Target: "{rage}", Platform: "rage", Online: true, Requires: req,
		InstallDir: filepath.Dir(path), IconSrc: path, SourceID: "rage:client"}).finish()}
}

// ------------------------------------------------------------------ Minecraft

func scanMinecraft(ctx *scanCtx) []*Found {
	var out []*Found
	exes := []pair{
		{"Minecraft", `%ProgramFiles(x86)%\Minecraft Launcher\MinecraftLauncher.exe`},
		{"Minecraft", `%ProgramFiles%\Minecraft Launcher\MinecraftLauncher.exe`},
		{"Minecraft", `{drive}\XboxGames\Minecraft Launcher\Content\Minecraft.exe`},
		{"Minecraft (TLauncher)", `%APPDATA%\.minecraft\TLauncher.exe`},
		{"Minecraft (TLauncher)", `%APPDATA%\.tlauncher\TLauncher.exe`},
	}
	seen := map[string]bool{}
	for _, e := range exes {
		for _, p := range expand(e.V) {
			if isFile(p) && !seen[e.K] {
				seen[e.K] = true
				out = append(out, (&Found{Name: e.K, Target: filepath.Clean(p), Platform: "minecraft",
					InstallDir: filepath.Dir(p), IconSrc: p, SourceID: "minecraft:" + norm(p)}).finish())
			}
		}
	}
	uwp := []struct{ pkg, name, target string }{
		{"Microsoft.4297127D64EC6_8wekyb3d8bbwe", "Minecraft", `shell:AppsFolder\Microsoft.4297127D64EC6_8wekyb3d8bbwe!Minecraft`},
		{"Microsoft.MinecraftUWP_8wekyb3d8bbwe", "Minecraft for Windows", `shell:AppsFolder\Microsoft.MinecraftUWP_8wekyb3d8bbwe!App`},
	}
	for _, u := range uwp {
		if seen[u.name] {
			continue
		}
		for _, d := range expand(`%LOCALAPPDATA%\Packages\` + u.pkg) {
			if isDir(d) {
				seen[u.name] = true
				out = append(out, (&Found{Name: u.name, Target: u.target, Platform: "minecraft", SourceID: "minecraft:" + u.pkg}).finish())
			}
		}
	}
	return out
}

func minecraftTarget() string {
	if f := scanMinecraft(&scanCtx{}); len(f) > 0 {
		return f[0].Target
	}
	return ""
}

// ------------------------------------------------------------------ Ubisoft, GOG

func scanUbisoft(ctx *scanCtx) []*Found {
	var out []*Found
	base := `SOFTWARE\Ubisoft\Launcher\Installs`
	for _, id := range regSubkeys("HKLM", base) {
		d, ok := regGet("HKLM", base+`\`+id, "InstallDir")
		if !ok || !isDir(d) {
			continue
		}
		out = append(out, (&Found{Name: baseName(filepath.Clean(d)), Target: "uplay://launch/" + id + "/0", Platform: "ubisoft",
			Online: true, Requires: []string{"ubisoft"}, InstallDir: d, SourceID: "ubisoft:" + id}).finish())
	}
	return out
}

func scanGOG(ctx *scanCtx) []*Found {
	var out []*Found
	base := `SOFTWARE\GOG.com\Games`
	for _, id := range regSubkeys("HKLM", base) {
		get := func(v string) string { s, _ := regGet("HKLM", base+`\`+id, v); return s }
		exe, path := get("exe"), get("path")
		if exe != "" && !filepath.IsAbs(exe) {
			exe = filepath.Join(path, exe)
		}
		if !isFile(exe) {
			continue
		}
		name := get("gameName")
		if name == "" {
			name = baseName(path)
		}
		wd := get("workingDir")
		if wd == "" {
			wd = path
		}
		out = append(out, (&Found{Name: name, Target: exe, Args: get("launchParam"), Workdir: wd, Platform: "gog",
			InstallDir: path, IconSrc: exe, SourceID: "gog:" + id}).finish())
	}
	return out
}

// ------------------------------------------------------------------ папки с играми

var cleanRe = regexp.MustCompile(`[^a-z0-9а-яё]`)

func cleanName(s string) string { return cleanRe.ReplaceAllString(strings.ToLower(s), "") }

// bestExe угадывает главный .exe игры: похож на имя папки, крупный, неглубоко.
func bestExe(folder string) string {
	want := cleanName(baseName(filepath.Clean(folder)))
	best, bestScore, seen := "", -1e9, 0
	rootDepth := strings.Count(filepath.Clean(folder), string(filepath.Separator))
	_ = filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		depth := strings.Count(filepath.Clean(p), string(filepath.Separator)) - rootDepth
		if d.IsDir() {
			if p != folder && (dirSkip[strings.ToLower(d.Name())] || depth > 3) {
				return fs.SkipDir
			}
			return nil
		}
		seen++
		if seen > 4000 {
			return fs.SkipAll
		}
		n := d.Name()
		if !strings.HasSuffix(strings.ToLower(n), ".exe") || exeSkip.MatchString(n) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		dirDepth := depth - 1
		stem := cleanName(strings.TrimSuffix(n, filepath.Ext(n)))
		mb := float64(info.Size()) / 1e6
		if mb > 200 {
			mb = 200
		}
		score := mb*0.3 - float64(dirDepth)*8
		if stem != "" && want != "" && (strings.Contains(want, stem) || strings.Contains(stem, want)) {
			score += 60
		}
		if strings.HasSuffix(stem, "shipping") || strings.Contains(strings.ToLower(filepath.Dir(p)), "win64") {
			score += 10
		}
		if score > bestScore {
			best, bestScore = p, score
		}
		return nil
	})
	return best
}

func urlTarget(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "url=") {
			return line[4:]
		}
	}
	return ""
}

var steamRunRe = regexp.MustCompile(`(?i)^steam://rungameid/(\d+)`)

func guessFromURI(uri string) (string, []string, string) {
	if m := steamRunRe.FindStringSubmatch(uri); m != nil {
		return "steam", []string{"steam"}, "steam:" + m[1]
	}
	low := strings.ToLower(uri)
	if strings.HasPrefix(low, "com.epicgames.launcher://") {
		return "epic", []string{"epic"}, ""
	}
	if strings.HasPrefix(low, "uplay://") {
		return "ubisoft", []string{"ubisoft"}, ""
	}
	return "other", []string{}, ""
}

func scanFolder(folder string) []*Found {
	var out []*Found
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name()) })
	for _, e := range entries {
		name := e.Name()
		p := filepath.Join(folder, name)
		low := strings.ToLower(name)
		if !e.IsDir() {
			if !(strings.HasSuffix(low, ".lnk") || strings.HasSuffix(low, ".url") || strings.HasSuffix(low, ".exe")) || exeSkip.MatchString(name) {
				continue
			}
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			if strings.HasSuffix(low, ".url") {
				if uri := urlTarget(p); uri != "" {
					plat, req, sid := guessFromURI(uri)
					out = append(out, (&Found{Name: stem, Target: uri, Platform: plat, Online: len(req) > 0, Requires: req, SourceID: sid}).finish())
				}
				continue
			}
			icon := ""
			if strings.HasSuffix(low, ".exe") {
				icon = p
			}
			out = append(out, (&Found{Name: stem, Target: p, InstallDir: folder, IconSrc: icon}).finish())
		} else if !dirSkip[low] && !strings.HasPrefix(name, ".") {
			if exe := bestExe(p); exe != "" {
				out = append(out, (&Found{Name: name, Target: exe, Workdir: filepath.Dir(exe), InstallDir: p, IconSrc: exe}).finish())
			}
		}
	}
	return out
}

// ------------------------------------------------------------------ всё вместе

type scanSource struct {
	id, label string
	fn        func(*scanCtx) []*Found
}

var scanSources = []scanSource{
	{"steam", "Steam", scanSteam},
	{"epic", "Epic Games", scanEpic},
	{"riot", "Riot Games", scanRiot},
	{"rockstar", "Rockstar Games Launcher", scanRockstar},
	{"rage", "RAGE Multiplayer", scanRage},
	{"minecraft", "Minecraft", scanMinecraft},
	{"ubisoft", "Ubisoft Connect", scanUbisoft},
	{"gog", "GOG", scanGOG},
	{"ea", "EA app", scanEA},
	{"battlenet", "Battle.net", scanBattlenet},
	{"xbox", "Xbox и Microsoft Store", scanXbox},
}

// scanAll ищет игры. sources == nil — все источники и сохранённые папки.
func scanAll(st *Store, launchers map[string]*Launcher, sources []string, extraFolders []string) []*Found {
	ctx := &scanCtx{launchers: launchers}
	want := func(id string) bool { return sources == nil || contains(sources, id) }
	var results []*Found
	for _, src := range scanSources {
		if !want(src.id) {
			continue
		}
		items := safeScan(src.id, func() []*Found { return src.fn(ctx) })
		for _, it := range items {
			it.Source = src.label
			if ctx.gtaStore == "" && (src.id == "steam" || src.id == "epic") {
				if gtaSteamIDs[strings.TrimPrefix(it.SourceID, "steam:")] || gtaEpicApps[strings.TrimPrefix(it.SourceID, "epic:")] {
					ctx.gtaStore = src.id
				}
			}
		}
		results = append(results, items...)
	}

	var folders []string
	if want("folders") {
		folders = append(folders, st.Snapshot().LibraryFolders...)
	}
	folders = append(folders, extraFolders...)
	for _, f := range uniq(folders) {
		for _, it := range scanFolder(f) {
			it.Source = "Папка " + f
			results = append(results, it)
		}
	}

	// Убираем дубли: одна и та же игра в реестре Rockstar и в Steam и т.п.
	out := []*Found{}
	keys, dirs := map[string]bool{}, map[string]bool{}
	for _, it := range results {
		d := norm(it.InstallDir)
		if keys[it.Key] || (d != "" && dirs[d] && it.Platform != "rage" && it.Platform != "riot") {
			continue
		}
		keys[it.Key] = true
		if d != "" {
			dirs[d] = true
		}
		it.InLibrary = st.FindDuplicate(it.SourceID, it.Target, it.Args)
		out = append(out, it)
	}
	return out
}

func safeScan(id string, fn func() []*Found) (res []*Found) {
	defer func() {
		if r := recover(); r != nil {
			logger.Printf("Ошибка сканирования %s: %v", id, r)
			res = nil
		}
	}()
	return fn()
}
