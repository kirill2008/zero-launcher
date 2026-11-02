package main

// Проверки без Windows: поддельные папки Steam/Epic/Riot/RAGE MP и поддельные процессы.
// Запуск: go test ./...

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func touch(t *testing.T, path string, data string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if data == "" {
		data = "MZ" + strings.Repeat("\x00", 64)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type fakeWin struct {
	root, gamesFolder string
}

func newFakeWin(t *testing.T) *fakeWin {
	r := t.TempDir()
	env := map[string]string{
		"ProgramData": r + "/ProgramData", "ProgramFiles": r + "/PF", "ProgramFiles(x86)": r + "/PF86",
		"APPDATA": r + "/Roaming", "LOCALAPPDATA": r + "/Local", "ZERO_FAKE_DRIVES": r + "/C",
		"ZERO_HOME": r + "/IgniteData",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	steam := r + "/PF86/Steam"
	touch(t, steam+"/steam.exe", "")
	lib2 := r + "/D/SteamLibrary"
	touch(t, steam+"/steamapps/libraryfolders.vdf", fmt.Sprintf(`"libraryfolders"
{
  "0" { "path" "%s" "apps" { "578080" "1" } }
  "1" { "path" "%s" "apps" { "271590" "1" } }
}`, steam, lib2))
	_ = os.MkdirAll(lib2+"/steamapps", 0o755)
	acf := func(lib, id, name, dir string) {
		touch(t, fmt.Sprintf("%s/steamapps/appmanifest_%s.acf", lib, id),
			fmt.Sprintf("\"AppState\"\n{\n \"appid\" \"%s\"\n \"name\" \"%s\"\n \"installdir\" \"%s\"\n}\n", id, name, dir))
	}
	acf(steam, "578080", "PUBG: BATTLEGROUNDS", "PUBG")
	acf(steam, "228980", "Steamworks Common Redistributables", "Steamworks Shared")
	acf(lib2, "271590", "Grand Theft Auto V Legacy", "Grand Theft Auto V")
	touch(t, steam+"/appcache/librarycache/578080/abc123/library_600x900.jpg", "\xff\xd8\xff\xe0jpeg")

	man := r + "/ProgramData/Epic/EpicGamesLauncher/Data/Manifests"
	touch(t, r+"/PF/Epic Games/Fortnite/FortniteGame/Binaries/Win64/FortniteClient-Win64-Shipping.exe", "")
	item := func(name string, m map[string]any) {
		raw, _ := json.Marshal(m)
		touch(t, man+"/"+name, bom+string(raw))
	}
	item("A.item", map[string]any{"DisplayName": "Fortnite", "AppName": "Fortnite", "MainGameAppName": "Fortnite",
		"CatalogNamespace": "fn", "CatalogItemId": "4fe75bbc5a674f4f9b356b5c90567da5",
		"InstallLocation":  r + "/PF/Epic Games/Fortnite",
		"LaunchExecutable": "FortniteGame/Binaries/Win64/FortniteClient-Win64-Shipping.exe",
		"AppCategories":    []string{"public", "games", "applications"}})
	item("B.item", map[string]any{"DisplayName": "Some DLC", "AppName": "dlc1", "MainGameAppName": "Fortnite", "AppCategories": []string{"addons"}})
	item("C.item", map[string]any{"DisplayName": "Unreal Engine", "AppName": "UE_5.3", "MainGameAppName": "UE_5.3", "AppCategories": []string{"engines"}})

	rc := touch(t, r+"/C/Riot Games/Riot Client/RiotClientServices.exe", "")
	raw, _ := json.Marshal(map[string]string{"rc_default": rc})
	touch(t, r+"/ProgramData/Riot Games/RiotClientInstalls.json", string(raw))
	touch(t, r+"/ProgramData/Riot Games/Metadata/valorant.live/valorant.live.product_settings.yaml",
		fmt.Sprintf("product_install_full_path: \"%s/C/Riot Games/VALORANT/live\"\n", r))

	touch(t, r+"/C/RAGEMP/updater.exe", "")
	touch(t, r+"/PF/Rockstar Games/Launcher/Launcher.exe", "")
	touch(t, r+"/Roaming/.minecraft/TLauncher.exe", "")

	games := r + "/D/Games"
	touch(t, games+"/Hollow Knight/hollow_knight.exe", "MZ"+strings.Repeat("\x00", 5000))
	touch(t, games+"/Hollow Knight/UnityCrashHandler64.exe", "MZ"+strings.Repeat("\x00", 9000))
	touch(t, games+"/Hollow Knight/unins000.exe", "MZ"+strings.Repeat("\x00", 9000))
	touch(t, games+"/Witcher/bin/x64/witcher3.exe", "MZ"+strings.Repeat("\x00", 7000))
	touch(t, games+"/Dota.url", "[InternetShortcut]\nURL=steam://rungameid/570\n")
	return &fakeWin{root: r, gamesFolder: games}
}

func newTestStore(t *testing.T) (*fakeWin, *Store) {
	fw := newFakeWin(t)
	st, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	detectMissing(st, false)
	zero := 0
	_ = st.Update(func(c *Config) error {
		for _, o := range c.Launchers {
			o.Delay = &zero
		}
		return nil
	})
	return fw, st
}

type fakeProcs struct {
	mu      sync.Mutex
	running map[string]bool
}

func (f *fakeProcs) add(n string) { f.mu.Lock(); f.running[n] = true; f.mu.Unlock() }
func (f *fakeProcs) snapshot() []procInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []procInfo
	for n := range f.running {
		out = append(out, procInfo{n, ""})
	}
	return out
}

type call struct{ kind, what, args string }

type fakeStarter struct {
	procs  *fakeProcs
	spawn  map[string]string
	hook   func(n int, base, args string) []string // если задан — какие процессы «появятся» после n-го запуска
	mu     sync.Mutex
	calls  []call
	cwds   []string
	admins []bool
}

func (s *fakeStarter) Start(path, args, cwd string, admin bool) error {
	s.mu.Lock()
	s.calls = append(s.calls, call{"start", baseName(path), args})
	s.cwds = append(s.cwds, cwd)
	s.admins = append(s.admins, admin)
	n := len(s.calls)
	s.mu.Unlock()
	name := strings.ToLower(baseName(path))
	if s.hook != nil {
		for _, p := range s.hook(n, name, args) {
			s.procs.add(p)
		}
		return nil
	}
	if v, ok := s.spawn[name]; ok {
		name = v
	}
	if name != "" {
		s.procs.add(name)
	}
	if strings.Contains(args, "--launch-product=valorant") { // настоящий клиент запускает игру
		s.procs.add("valorant-win64-shipping.exe")
	}
	return nil
}

func (s *fakeStarter) OpenURI(uri string) error {
	s.mu.Lock()
	s.calls = append(s.calls, call{"uri", uri, ""})
	s.mu.Unlock()
	return nil
}

func runJob(st *Store, gid string, p *fakeProcs, s *fakeStarter, skip bool) JobView {
	j := NewLaunchJob(st, gid, skip, NewProbe(p.snapshot), s)
	j.poll = 10 * time.Millisecond
	j.riotRetry, j.riotWait = 40*time.Millisecond, 400*time.Millisecond
	j.riotAPIWait, j.riotAPIStep, j.riotLogin = 20*time.Millisecond, 20*time.Millisecond, 1500*time.Millisecond
	j.rage = func(string) error { return nil }
	j.Run()
	return j.View()
}

func newFake() (*fakeProcs, *fakeStarter) {
	p := &fakeProcs{running: map[string]bool{}}
	return p, &fakeStarter{procs: p, spawn: map[string]string{}}
}

// ------------------------------------------------------------------ тесты

func TestDetectLaunchers(t *testing.T) {
	_, st := newTestStore(t)
	ls := launchersByID(st)
	for _, id := range []string{"steam", "riot", "rage", "socialclub"} {
		if !ls[id].Exists {
			t.Errorf("%s не найден", id)
		}
	}
	if ls["epic"].Exists {
		t.Error("epic не должен находиться")
	}
	updateLauncher(st, "steam", map[string]any{"path": "/my/steam.exe"})
	detectMissing(st, true)
	if p := launchersByID(st)["steam"].Path; p != "/my/steam.exe" {
		t.Errorf("ручной путь перезаписан: %s", p)
	}
}

func TestScanAll(t *testing.T) {
	fw, st := newTestStore(t)
	_ = st.Update(func(c *Config) error { c.LibraryFolders = append(c.LibraryFolders, fw.gamesFolder); return nil })
	items := scanAll(st, launchersByID(st), nil, nil)
	by := map[string]*Found{}
	for _, it := range items {
		by[it.Name] = it
	}
	check := func(cond bool, msg string, args ...any) {
		t.Helper()
		if !cond {
			t.Errorf(msg, args...)
		}
	}
	check(by["PUBG: BATTLEGROUNDS"] != nil && by["PUBG: BATTLEGROUNDS"].Target == "steam://rungameid/578080", "PUBG")
	check(strings.HasSuffix(by["PUBG: BATTLEGROUNDS"].CoverSrc, "library_600x900.jpg"), "обложка PUBG: %q", by["PUBG: BATTLEGROUNDS"].CoverSrc)
	check(by["Steamworks Common Redistributables"] == nil, "служебные пакеты Steam")
	check(strings.Join(by["Grand Theft Auto V Legacy"].Requires, ",") == "steam,socialclub", "GTA requires")
	check(by["Fortnite"] != nil && strings.Contains(by["Fortnite"].Target, "fn%3A4fe75bbc5a674f4f9b356b5c90567da5%3AFortnite"), "Fortnite URI")
	check(by["Some DLC"] == nil && by["Unreal Engine"] == nil, "DLC и UE")
	check(by["VALORANT"] != nil && by["VALORANT"].Args == "--launch-product=valorant --launch-patchline=live", "VALORANT")
	check(by["GTA 5 RP"] != nil && by["GTA 5 RP"].Target == "{rage}", "GTA 5 RP")
	check(strings.Join(by["GTA 5 RP"].Requires, ",") == "steam,socialclub", "GTA 5 RP requires: %v", by["GTA 5 RP"].Requires)
	check(by["Minecraft (TLauncher)"] != nil, "TLauncher")
	check(by["Hollow Knight"] != nil && strings.HasSuffix(by["Hollow Knight"].Target, "hollow_knight.exe"), "Hollow Knight: %+v", by["Hollow Knight"])
	check(by["Witcher"] != nil && strings.HasSuffix(by["Witcher"].Target, "witcher3.exe"), "Witcher")
	check(by["Dota"] != nil && by["Dota"].SourceID == "steam:570", "Dota .url")

	st.AddGame(map[string]any{"name": "PUBG", "target": "steam://rungameid/578080", "source_id": "steam:578080"})
	for _, it := range scanAll(st, launchersByID(st), []string{"steam"}, nil) {
		if it.SourceID == "steam:578080" && !it.InLibrary {
			t.Error("PUBG должен быть отмечен как уже добавленный")
		}
	}
}

func TestGtaRpChain(t *testing.T) {
	_, st := newTestStore(t)
	g := st.AddGame(map[string]any{"name": "GTA 5 RP", "target": "{rage}", "platform": "rage", "online": true,
		"requires": []any{"socialclub", "steam", "rage"}})
	p, s := newFake()
	s.spawn["launcher.exe"] = "socialclubhelper.exe"
	v := runJob(st, g.ID, p, s, false)
	if v.State != "done" {
		t.Fatalf("state %s %+v", v.State, v.Error)
	}
	var got []string
	for _, c := range s.calls {
		got = append(got, c.what)
	}
	if strings.Join(got, ",") != "steam.exe,Launcher.exe,updater.exe" {
		t.Errorf("порядок запуска: %v", got)
	}
	if !v.Steps[1].Forced || v.Steps[1].Label != "Social Club" {
		t.Errorf("Social Club должен быть обязательным: %+v", v.Steps)
	}
}

func TestSocialClubForcedOffline(t *testing.T) {
	_, st := newTestStore(t)
	g := st.AddGame(map[string]any{"name": "Grand Theft Auto V", "target": "steam://rungameid/271590", "platform": "steam"})
	p, s := newFake()
	s.spawn["launcher.exe"] = "rockstarservice.exe"
	v := runJob(st, g.ID, p, s, false)
	if v.State != "done" || len(s.calls) != 2 || s.calls[0].what != "Launcher.exe" || s.calls[1].kind != "uri" {
		t.Errorf("%s %+v", v.State, s.calls)
	}
}

func TestRunningLauncherSkipped(t *testing.T) {
	_, st := newTestStore(t)
	g := st.AddGame(map[string]any{"name": "PUBG", "target": "steam://rungameid/578080", "platform": "steam",
		"online": true, "requires": []any{"steam"}})
	p, s := newFake()
	p.add("steam.exe")
	v := runJob(st, g.ID, p, s, false)
	if v.Steps[0].Message != "Уже запущен" || len(s.calls) != 1 {
		t.Errorf("%+v %+v", v.Steps, s.calls)
	}
	if gg, _ := st.Game(g.ID); gg.PlayCount != 1 {
		t.Error("счётчик запусков")
	}
}

func TestMissingLauncherStopsThenSkip(t *testing.T) {
	_, st := newTestStore(t)
	g := st.AddGame(map[string]any{"name": "Fortnite", "target": "com.epicgames.launcher://apps/Fortnite",
		"platform": "epic", "online": true, "requires": []any{"epic"}})
	p, s := newFake()
	v := runJob(st, g.ID, p, s, false)
	if v.State != "error" || v.Error.Code != "launcher_missing" || v.Error.LauncherID != "epic" || len(s.calls) != 0 {
		t.Fatalf("%+v %+v", v, s.calls)
	}
	if v.Steps[0].State != "error" || v.Steps[0].Message != "Путь не указан" {
		t.Errorf("шаг: %+v", v.Steps[0])
	}
	v = runJob(st, g.ID, p, s, true)
	if v.State != "done" || v.Steps[0].State != "skipped" {
		t.Errorf("%+v", v)
	}
}

func TestMissingGameCheckedFirst(t *testing.T) {
	_, st := newTestStore(t)
	g := st.AddGame(map[string]any{"name": "Broken", "target": "/nope/game.exe", "online": true, "requires": []any{"steam"}})
	p, s := newFake()
	v := runJob(st, g.ID, p, s, false)
	if v.Error == nil || v.Error.Code != "game_missing" || len(s.calls) != 0 {
		t.Errorf("%+v %+v", v, s.calls)
	}
}

func TestLauncherTimeoutAndDelay(t *testing.T) {
	_, st := newTestStore(t)
	one := 1
	_ = st.Update(func(c *Config) error {
		c.Launchers["steam"].Delay = &one
		c.Settings.LauncherTimeout = 1
		return nil
	})
	g := st.AddGame(map[string]any{"name": "CS2", "target": "steam://rungameid/730", "online": true, "requires": []any{"steam"}})
	p, s := newFake()
	s.spawn["steam.exe"] = "" // Steam «не поднимается»
	start := time.Now()
	v := runJob(st, g.ID, p, s, false)
	if v.Steps[0].State != "warn" || v.State != "done" || time.Since(start) > 3*time.Second {
		t.Errorf("%+v", v)
	}
}

func TestCancel(t *testing.T) {
	_, st := newTestStore(t)
	ten := 10
	_ = st.Update(func(c *Config) error { c.Launchers["steam"].Delay = &ten; return nil })
	g := st.AddGame(map[string]any{"name": "CS2", "target": "steam://rungameid/730", "online": true, "requires": []any{"steam"}})
	p, s := newFake()
	j := NewLaunchJob(st, g.ID, false, NewProbe(p.snapshot), s)
	j.poll = 10 * time.Millisecond
	go func() { time.Sleep(200 * time.Millisecond); j.Cancel() }()
	j.Run()
	if v := j.View(); v.State != "cancelled" {
		t.Errorf("%+v", v)
	}
	for _, c := range s.calls {
		if c.kind == "uri" {
			t.Error("игра не должна была запуститься")
		}
	}
}

func TestGenericExeNeedsFolder(t *testing.T) {
	l := &Launcher{Path: "/games/rockstar/Launcher.exe", Processes: []string{"Launcher.exe"}}
	if NewProbe(func() []procInfo { return []procInfo{{"launcher.exe", "/other/Launcher.exe"}} }).IsRunning(l, nil) {
		t.Error("чужой Launcher.exe принят за Rockstar")
	}
	if !NewProbe(func() []procInfo { return []procInfo{{"launcher.exe", l.Path}} }).IsRunning(l, nil) {
		t.Error("Rockstar Launcher.exe не распознан")
	}
}

func TestIsGTA(t *testing.T) {
	yes := []Game{{Name: "GTA 5 RP"}, {Name: "Majestic", Target: `C:\RAGEMP\updater.exe`}, {Name: "Моя гта"},
		{Name: "ГТА РП"}, {Name: "X", SourceID: "steam:271590"}, {Name: "x", Target: `D:\Games\GTAV\PlayGTAV.exe`}}
	no := []Game{{Name: "PUBG: BATTLEGROUNDS", Target: "steam://rungameid/578080"}, {Name: "Minecraft", Target: "TLauncher.exe"},
		{Name: "Agtastic"}}
	for _, g := range yes {
		if !isGTA(&g) {
			t.Errorf("должна быть GTA: %+v", g)
		}
	}
	for _, g := range no {
		if isGTA(&g) {
			t.Errorf("не GTA: %+v", g)
		}
	}
}

func TestConfigSurvivesRestartAndCorruption(t *testing.T) {
	_, st := newTestStore(t)
	st.AddGame(map[string]any{"name": "Rust", "target": "steam://rungameid/252490"})
	st2, _ := NewStore("")
	if len(st2.Snapshot().Games) != 1 {
		t.Fatal("игры не сохранились")
	}
	_ = os.WriteFile(filepath.Join(st.Base, "config.json"), []byte("{oops"), 0o644)
	st3, _ := NewStore("")
	if len(st3.Snapshot().Games) != 0 {
		t.Error("повреждённый файл должен заменяться пустым")
	}
	m, _ := filepath.Glob(filepath.Join(st.Base, "config.json.broken-*"))
	if len(m) != 1 {
		t.Error("копия повреждённого файла не сохранена")
	}
}

// ------------------------------------------------------------------ HTTP API, как его видит интерфейс

func TestAPI(t *testing.T) {
	_, st := newTestStore(t)
	p, s := newFake()
	app := NewApp(st, NewProbe(p.snapshot), s)
	if err := app.Listen(0); err != nil {
		t.Fatal(err)
	}
	defer func() { app.Shutdown(); app.Wait() }()
	base := fmt.Sprintf("http://127.0.0.1:%d", app.Port)
	do := func(method, path string, body any, token bool) (int, map[string]any) {
		var r io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			r = strings.NewReader(string(raw))
		}
		req, _ := http.NewRequest(method, base+path, r)
		req.Header.Set("Content-Type", "application/json")
		if token {
			req.Header.Set("X-Zero-Token", app.Token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	if code, _ := do("GET", "/api/state", nil, false); code != 403 {
		t.Errorf("без токена: %d", code)
	}
	resp, _ := http.Get(base + "/")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src") {
		t.Errorf("index: %d", resp.StatusCode)
	}
	resp.Body.Close()

	code, res := do("POST", "/api/games", map[string]any{"name": "GTA 5 RP", "target": "{rage}", "platform": "rage",
		"online": true, "requires": []string{"steam"}}, true)
	if code != 200 {
		t.Fatalf("добавление: %d %v", code, res)
	}
	_, state := do("GET", "/api/state", nil, true)
	g := state["games"].([]any)[0].(map[string]any)
	chain := g["chain"].([]any)
	if g["ready"] != true || len(chain) != 2 || chain[1].(map[string]any)["forced"] != true {
		t.Errorf("игра: %v", g)
	}
	if raw := mustJSON(orderedMap(platforms)); !strings.HasPrefix(string(raw), `{"steam":"Steam","epic":"Epic Games"`) {
		t.Errorf("порядок платформ: %s", raw)
	}
	if len(state["rp_servers"].([]any)) == 0 {
		t.Error("нет списка серверов")
	}
	for _, key := range []string{"launchers", "library_folders", "settings", "platform_requires"} {
		if state[key] == nil {
			t.Errorf("нет %s", key)
		}
	}

	_, res = do("POST", "/api/scan", map[string]any{"sources": []string{"steam", "riot"}}, true)
	items := res["items"].([]any)
	if len(items) < 3 {
		t.Fatalf("найдено: %d", len(items))
	}
	_, res = do("POST", "/api/import", map[string]any{"items": items}, true)
	if len(res["added"].([]any)) < 3 {
		t.Errorf("импорт: %v", res)
	}
	_, res = do("POST", "/api/launchers/detect", map[string]any{}, true)
	if _, ok := res["changed"].([]any); !ok {
		t.Errorf("changed должен быть массивом: %v", res)
	}
	_, res = do("GET", "/api/templates", nil, true)
	if tpl := res["templates"].([]any)[0].(map[string]any); tpl["target"] != "steam://rungameid/578080" || tpl["desc"] == "" {
		t.Errorf("шаблон PUBG: %v", tpl)
	}

	app.Art.RefreshAll(app.Store, false, false) // в программе это делается в фоне
	_, state = do("GET", "/api/state", nil, true)
	var pubg map[string]any
	for _, x := range state["games"].([]any) {
		if x.(map[string]any)["source_id"] == "steam:578080" {
			pubg = x.(map[string]any)
		}
	}
	if pubg == nil || !strings.HasPrefix(pubg["cover_url"].(string), "/img/cover/") {
		t.Fatalf("обложка: %v", pubg)
	}
	resp, _ = http.Get(base + pubg["cover_url"].(string) + "&t=" + app.Token)
	if resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("тип обложки: %s", resp.Header.Get("Content-Type"))
	}
	resp.Body.Close()

	// запуск через API и опрос задачи, как это делает интерфейс
	gid := pubg["id"].(string)
	_, res = do("POST", "/api/games/"+gid+"/launch", map[string]any{}, true)
	jid := res["job"].(map[string]any)["id"].(string)
	var job map[string]any
	for i := 0; i < 100; i++ {
		_, res = do("GET", "/api/jobs/"+jid, nil, true)
		job = res["job"].(map[string]any)
		if job["state"] != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if job["state"] != "done" {
		t.Errorf("запуск PUBG: %v", job)
	}

	code, res = do("PUT", "/api/launchers/steam", map[string]any{"delay": "7"}, true)
	if code != 200 || launchersByID(st)["steam"].Delay != 7 {
		t.Errorf("пауза: %d %v", code, res)
	}
	if code, _ = do("PUT", "/api/settings", map[string]any{"launcher_timeout": "abc"}, true); code != 400 {
		t.Errorf("неверное время ожидания: %d", code)
	}
	if code, _ = do("DELETE", "/api/launchers/steam", map[string]any{}, true); code != 400 {
		t.Errorf("удаление встроенного лаунчера: %d", code)
	}
	code, res = do("POST", "/api/launchers", map[string]any{"name": "Majestic", "path": "/x/majestic.exe"}, true)
	if code != 200 || res["launcher"].(map[string]any)["processes"].([]any)[0] != "majestic.exe" {
		t.Errorf("свой лаунчер: %v", res)
	}
	req, _ := http.NewRequest("GET", base+"/api/state", nil)
	req.Host = "evil.example:80"
	req.Header.Set("X-Zero-Token", app.Token)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 403 {
		t.Errorf("чужой Host: %d", resp.StatusCode)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// ------------------------------------------------------------------ Valorant и сервер GTA 5 RP

func TestValorantAlwaysLaunchesGame(t *testing.T) {
	fw, st := newTestStore(t)
	valExe := touch(t, fw.root+"/C/Riot Games/VALORANT/live/VALORANT.exe", "")
	client := launchersByID(st)["riot"].Path
	cases := []map[string]any{
		{"name": "Valorant", "target": valExe, "platform": "other"},                                           // указан VALORANT.exe
		{"name": "VALORANT", "target": client, "platform": "riot", "online": true, "requires": []any{"riot"}}, // Riot Client без аргументов
		{"name": "Валорант", "target": "{riot}", "platform": "riot"},                                          // шаблон без аргументов
	}
	for _, c := range cases {
		g := st.AddGame(c)
		p, s := newFake()
		v := runJob(st, g.ID, p, s, false)
		if v.State != "done" || len(s.calls) != 1 {
			t.Fatalf("%v: %+v %+v", c["target"], v, s.calls)
		}
		if s.calls[0].what != "RiotClientServices.exe" || !strings.Contains(s.calls[0].args, "--launch-product=valorant") {
			t.Errorf("%v: запущено %+v", c["target"], s.calls[0])
		}
	}
	// Riot Client не указан — ищем рядом с игрой
	_ = st.Update(func(c *Config) error { c.Launchers["riot"].Path = ""; c.Launchers["riot"].Auto = false; return nil })
	g := st.AddGame(map[string]any{"name": "Valorant 2", "target": valExe})
	p, s := newFake()
	if v := runJob(st, g.ID, p, s, false); v.State != "done" || s.calls[0].what != "RiotClientServices.exe" {
		t.Errorf("поиск Riot Client рядом с игрой: %+v %+v", v, s.calls)
	}
	// Обычная игра не трогается
	g = st.AddGame(map[string]any{"name": "Hollow Knight", "target": fw.gamesFolder + "/Hollow Knight/hollow_knight.exe"})
	p, s = newFake()
	if v := runJob(st, g.ID, p, s, false); v.State != "done" || s.calls[0].what != "hollow_knight.exe" || s.calls[0].args != "" {
		t.Errorf("обычная игра: %+v", s.calls)
	}
}

func TestGtaRpServerAutoConnect(t *testing.T) {
	_, st := newTestStore(t)
	g := st.AddGame(map[string]any{"name": "GTA 5 RP", "target": "{rage}", "platform": "rage",
		"server": "downtown.gta5rp.com:22005", "server_name": "Downtown"})
	p, s := newFake()
	s.spawn["launcher.exe"] = "socialclubhelper.exe"
	j := NewLaunchJob(st, g.ID, false, NewProbe(p.snapshot), s)
	j.poll = 10 * time.Millisecond
	var got []string
	j.rage = func(addr string) error { got = append(got, addr); return nil }
	j.Run()
	v := j.View()
	if v.State != "done" || len(got) != 1 || got[0] != "downtown.gta5rp.com:22005" {
		t.Fatalf("%+v %v", v, got)
	}
	last := v.Steps[len(v.Steps)-1]
	if !strings.Contains(last.Message, "Downtown") {
		t.Errorf("%+v", last)
	}
	if c := s.calls[len(s.calls)-1]; c.what != "updater.exe" || c.args != "" || !strings.HasSuffix(s.cwds[len(s.cwds)-1], "RAGEMP") {
		t.Errorf("updater.exe должен запускаться из папки RAGEMP: %+v %v", c, s.cwds)
	}
	// Сервер не выбран (например, ярлык) — автоподключение очищается, RAGE MP просто открывается
	st.UpdateGame(g.ID, map[string]any{"server": ""})
	got = nil
	j = NewLaunchJob(st, g.ID, false, NewProbe(p.snapshot), s)
	j.rage = func(addr string) error { got = append(got, addr); return nil }
	j.Run()
	if len(got) != 1 || got[0] != "" {
		t.Errorf("очистка: %v", got)
	}
	// Старое значение «list» превращается во вход через официальный лаунчер
	st.UpdateGame(g.ID, map[string]any{"server": "list"})
	if gg, _ := st.Game(g.ID); gg.Server != serverOfficial {
		t.Errorf("list → official: %q", gg.Server)
	}
	if h, port := splitServer("1.2.3.4:22006"); h != "1.2.3.4" || port != "22006" {
		t.Error("splitServer")
	}
	if h, port := splitServer("redwood.gta5rp.com"); h != "redwood.gta5rp.com" || port != "22005" {
		t.Error("splitServer по умолчанию")
	}
}

func TestGta5rpOfficialLauncher(t *testing.T) {
	fw, st := newTestStore(t)
	g := st.AddGame(map[string]any{"name": "GTA 5 RP", "target": "{rage}", "platform": "rage", "server": serverOfficial})
	ls := launchersByID(st)
	if ok, why := targetReady(&Game{Name: "GTA 5 RP", Target: "{rage}", Platform: "rage", Server: serverOfficial}, ls); ok || !strings.Contains(why, "GTA5RP") {
		t.Errorf("без лаунчера GTA5RP должна быть просьба указать путь: %v %q", ok, why)
	}
	rp := touch(t, fw.root+"/Local/Programs/GTA5RP Launcher/GTA5RPLauncher.exe", "")
	detectMissing(st, true)
	if got := launchersByID(st)["gta5rp"].Path; got != filepath.Clean(rp) {
		t.Fatalf("лаунчер GTA5RP не найден: %q", got)
	}
	p, s := newFake()
	s.spawn["launcher.exe"] = "socialclubhelper.exe"
	j := NewLaunchJob(st, g.ID, false, NewProbe(p.snapshot), s)
	j.poll = 10 * time.Millisecond
	called := false
	j.rage = func(string) error { called = true; return nil }
	j.Run()
	v := j.View()
	if v.State != "done" || called || s.calls[len(s.calls)-1].what != "GTA5RPLauncher.exe" {
		t.Fatalf("%+v %+v rage=%v", v, s.calls, called)
	}
	var names []string
	for _, st := range v.Steps {
		names = append(names, st.Label)
	}
	if strings.Join(names, ",") != "Social Club,GTA 5 RP" { // Social Club для GTA — всегда
		t.Errorf("шаги: %v", names)
	}
	// RAGE MP не найден, а сервер выбран — открываем лаунчер GTA5RP
	_ = st.Update(func(c *Config) error { c.Launchers["rage"].Path = ""; c.Launchers["rage"].Auto = false; return nil })
	st.UpdateGame(g.ID, map[string]any{"server": "blaine.gta5rp.com:22005"})
	gg, _ := st.Game(g.ID)
	if pl := launchPlan(&gg, launchersByID(st)); pl.Via != "gta5rp" || pl.Direct {
		t.Errorf("запасной вариант: %+v", pl)
	}
}

func TestValorantColdStartRetry(t *testing.T) {
	_, st := newTestStore(t)
	g := st.AddGame(map[string]any{"name": "VALORANT", "target": "{riot}", "platform": "riot",
		"args": "--launch-product=valorant --launch-patchline=live"})
	p, s := newFake()
	// Только что открытый клиент пропускает первую команду, вторую выполняет.
	s.hook = func(n int, base, args string) []string {
		if n == 1 {
			return []string{"riotclientservices.exe"}
		}
		return []string{"valorant-win64-shipping.exe"}
	}
	v := runJob(st, g.ID, p, s, false)
	if v.State != "done" || len(s.calls) != 2 || v.Steps[0].State != "done" {
		t.Fatalf("%+v %+v", v, s.calls)
	}
	// Игра так и не появилась — предупреждение, а не ошибка
	p, s = newFake()
	s.hook = func(int, string, string) []string { return []string{"riotclientservices.exe"} }
	v = runJob(st, g.ID, p, s, false)
	if v.State != "done" || v.Steps[0].State != "warn" || len(s.calls) != 2 {
		t.Errorf("%+v %+v", v, s.calls)
	}
	// Указан «Riot Client.exe» — команды понимает только RiotClientServices.exe
	ux := filepath.Join(filepath.Dir(launchersByID(st)["riot"].Path), "RiotClientElectron", "Riot Client.exe")
	touch(t, ux, "")
	_ = st.Update(func(c *Config) error { c.Launchers["riot"].Path = ux; c.Launchers["riot"].Auto = false; return nil })
	p, s = newFake()
	if v := runJob(st, g.ID, p, s, false); v.State != "done" || s.calls[0].what != "RiotClientServices.exe" {
		t.Errorf("Riot Client.exe → RiotClientServices.exe: %+v", s.calls)
	}
}

func TestMigrationAndLenientConfig(t *testing.T) {
	root := t.TempDir()
	oldDir, newDir := filepath.Join(root, "Ignite"), filepath.Join(root, "ZeroLauncher")
	cover := touch(t, filepath.Join(oldDir, "covers", "g_1_1.jpg"), "\xff\xd8")
	cfg := map[string]any{
		"games": []any{map[string]any{"id": "g_1", "name": "PUBG", "target": "steam://rungameid/578080",
			"cover": cover, "added": "1700000000", "online": "true"}},
		"launchers":        map[string]any{"steam": map[string]any{"path": "C:/Steam/steam.exe", "delay": "4", "auto": false}},
		"custom_launchers": []any{map[string]any{"id": "l_1", "name": "RP", "path": "C:/rp.exe", "delay": "2"}},
		"settings":         map[string]any{"launcher_timeout": "120"},
	}
	raw, _ := json.Marshal(cfg)
	touch(t, filepath.Join(oldDir, "config.json"), string(raw))
	if !migrateLegacy(newDir, oldDir) {
		t.Fatal("перенос не выполнен")
	}
	if migrateLegacy(newDir, oldDir) {
		t.Error("повторный перенос не нужен")
	}
	st, err := NewStore(newDir)
	if err != nil {
		t.Fatal(err)
	}
	c := st.Snapshot()
	if len(c.Games) != 1 || !c.Games[0].Online || c.Games[0].Added != 1700000000 {
		t.Fatalf("игра: %+v", c.Games)
	}
	if want := filepath.Join(newDir, "covers", "g_1_1.jpg"); c.Games[0].Cover != want || !isFile(want) {
		t.Errorf("обложка: %q", c.Games[0].Cover)
	}
	if c.Launchers["steam"].Delay == nil || *c.Launchers["steam"].Delay != 4 || c.Settings.LauncherTimeout != 120 ||
		c.CustomLaunchers[0].Delay != 2 {
		t.Errorf("числа из строк: %+v %+v", c.Launchers["steam"], c.Settings)
	}
	if m, _ := filepath.Glob(filepath.Join(newDir, "config.json.broken-*")); len(m) != 0 {
		t.Error("конфиг не должен считаться повреждённым")
	}
}
