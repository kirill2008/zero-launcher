package main

// Проверки правок из ревью: Steam StateFlags, права при повторе Riot, запись config.json,
// разбор команды из реестра, порядок запусков, копирование без обрезков.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSteamStateFlags(t *testing.T) {
	fw, st := newTestStore(t)
	lib := fw.root + "/PF86/Steam/steamapps"
	acf := func(id, name, flags string) {
		extra := ""
		if flags != "" {
			extra = fmt.Sprintf(" \"StateFlags\" \"%s\"\n", flags)
		}
		touch(t, fmt.Sprintf("%s/appmanifest_%s.acf", lib, id),
			fmt.Sprintf("\"AppState\"\n{\n \"appid\" \"%s\"\n \"name\" \"%s\"\n%s \"installdir\" \"%s\"\n}\n", id, name, extra, name))
	}
	acf("730", "Counter-Strike 2", "4")         // установлена
	acf("570", "Dota 2", "6")                   // установлена, ждёт обновления — играть можно
	acf("252490", "Rust", "1026")               // ещё качается
	acf("1172470", "Apex Legends", "2048")      // удаляется
	acf("440", "Team Fortress 2", "notanumber") // непонятное значение — не отбрасываем
	got := map[string]bool{}
	for _, it := range scanSteam(&scanCtx{launchers: launchersByID(st)}) {
		got[it.Name] = true
	}
	for name, want := range map[string]bool{"Counter-Strike 2": true, "Dota 2": true, "Rust": false, "Apex Legends": false,
		"Team Fortress 2": true, "PUBG: BATTLEGROUNDS": true /* манифест без StateFlags */} {
		if got[name] != want {
			t.Errorf("%s: в списке=%v, ожидалось %v", name, got[name], want)
		}
	}
}

func TestRiotRetryKeepsAdmin(t *testing.T) {
	_, st := newTestStore(t)
	g := st.AddGame(map[string]any{"name": "VALORANT", "target": "{riot}", "platform": "riot", "admin": true,
		"args": "--launch-product=valorant --launch-patchline=live"})
	p, s := newFake()
	s.hook = func(n int, base, args string) []string {
		if n == 1 {
			return []string{"riotclientservices.exe"} // первая команда пропущена
		}
		return []string{"valorant-win64-shipping.exe"}
	}
	v := runJob(st, g.ID, p, s, false)
	if v.State != "done" || len(s.admins) != 2 || !s.admins[0] || !s.admins[1] {
		t.Fatalf("повтор должен идти от администратора: %v %+v", s.admins, v)
	}
}

func TestDetectMissingWritesOnlyChanges(t *testing.T) {
	fw, st := newTestStore(t)
	cfgPath := filepath.Join(st.Base, "config.json")
	detectMissing(st, false)
	snap := st.Snapshot()
	if snap.Launchers["steam"] == nil || !snap.Launchers["steam"].Auto {
		t.Fatal("Steam должен найтись")
	}
	if o := snap.Launchers["ea"]; o != nil {
		t.Errorf("для ненайденного лаунчера не нужна пустая запись: %+v", o)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(cfgPath, old, old)
	before, _ := os.ReadFile(cfgPath)
	if changed := detectMissing(st, false); len(changed) != 0 {
		t.Errorf("ничего не поменялось, а изменено: %v", changed)
	}
	if st2, _ := os.Stat(cfgPath); !st2.ModTime().Equal(old) {
		t.Error("config.json переписан без изменений")
	}
	if changed := detectMissing(st, true); len(changed) != 0 { // «Найти автоматически»: те же пути
		t.Errorf("повторный поиск без изменений: %v", changed)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Error("содержимое config.json изменилось")
	}
	// автоматически найденный лаунчер удалили — путь очищается
	_ = os.Remove(fw.root + "/C/RAGEMP/updater.exe")
	if changed := detectMissing(st, false); len(changed) != 1 || changed[0] != "rage" {
		t.Errorf("пропавший RAGE MP: %v", changed)
	}
	if o := st.Snapshot().Launchers["rage"]; o == nil || o.Path != "" {
		t.Errorf("путь не очищен: %+v", o)
	}
	// путь, указанный руками, не трогается
	_ = st.Update(func(c *Config) error {
		c.Launchers["steam"] = &LauncherOverride{Path: fw.root + "/manual/steam.exe", Auto: false}
		return nil
	})
	detectMissing(st, true)
	if o := st.Snapshot().Launchers["steam"]; o.Path != fw.root+"/manual/steam.exe" {
		t.Errorf("ручной путь перезаписан: %+v", o)
	}
}

func TestExeFromCommand(t *testing.T) {
	t.Setenv("ProgramFiles", `C:\Program Files`)
	for in, want := range map[string]string{
		`"C:\Program Files (x86)\Steam\steam.exe" -- "%1"`:                  `C:\Program Files (x86)\Steam\steam.exe`,
		`C:\Program Files (x86)\Ubisoft\Ubisoft Game Launcher\upc.exe "%1"`: `C:\Program Files (x86)\Ubisoft\Ubisoft Game Launcher\upc.exe`,
		`%ProgramFiles%\Epic\EpicGamesLauncher.exe %1`:                      `C:\Program Files\Epic\EpicGamesLauncher.exe`,
		`"C:\Riot Games\Riot Client\RiotClientServices.exe`:                 `C:\Riot Games\Riot Client\RiotClientServices.exe`,
		`rundll32 url.dll,FileProtocolHandler %1`:                           `rundll32`,
		`   `: ``,
	} {
		if got := exeFromCommand(in); got != want {
			t.Errorf("%q → %q, ожидалось %q", in, got, want)
		}
	}
}

func TestActiveJobsOrderAndErrors(t *testing.T) {
	_, st := newTestStore(t)
	app := NewApp(st, nil, nil)
	t0 := time.Now()
	for i, d := range []int{2, 0, 1} {
		j := NewLaunchJob(st, fmt.Sprintf("g_%d", i), false, nil, nil)
		j.started = t0.Add(time.Duration(d) * time.Second)
		app.jobs[j.ID] = j
	}
	var first []string
	for n := 0; n < 20; n++ { // порядок одинаковый при каждом запросе
		var ids []string
		for _, j := range app.activeJobs() {
			ids = append(ids, j["game_id"])
		}
		if n == 0 {
			first = ids
		} else if strings.Join(ids, ",") != strings.Join(first, ",") {
			t.Fatalf("порядок прыгает: %v / %v", first, ids)
		}
	}
	if strings.Join(first, ",") != "g_1,g_2,g_0" {
		t.Errorf("по времени начала: %v", first)
	}
}

func TestCopyFileNoPartial(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.png")
	writeBytes(t, src, []byte("\x89PNG data"))
	dst := filepath.Join(dir, "b.png")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(dst); string(raw) != "\x89PNG data" {
		t.Error("копия не совпадает")
	}
	// читать папку как файл нельзя — копирование падает посередине, обрезков не остаётся
	bad := filepath.Join(dir, "bad.png")
	if err := copyFile(dir, bad); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	for _, p := range []string{bad, bad + ".part"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("остался файл %s", p)
		}
	}
}

func TestInternalErrorHidesDetails(t *testing.T) {
	_, app, call, _ := artApp(t)
	app.Store.path = filepath.Join(t.TempDir(), "нет-такой-папки", "config.json") // запись config.json упадёт
	code, res := call("PUT", "/api/settings", map[string]any{"compact": true})
	if code != 500 {
		t.Fatalf("ожидался ответ 500, а не %d", code)
	}
	if msg := fmt.Sprint(res["error"]); strings.Contains(msg, "нет-такой-папки") || !strings.Contains(msg, "журнале") {
		t.Errorf("в интерфейс ушли подробности ошибки: %q", msg)
	}
}
