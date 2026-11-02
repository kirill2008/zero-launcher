package main

// Проверки того, что нужно интерфейсу NEXUS: настройки внешнего вида, журнал запусков, статика.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHistoryRecordsOutcomes(t *testing.T) {
	_, st := newTestStore(t)
	ok := st.AddGame(map[string]any{"name": "PUBG", "target": "steam://rungameid/578080", "platform": "steam",
		"online": true, "requires": []any{"steam"}})
	bad := st.AddGame(map[string]any{"name": "Fortnite", "target": "com.epicgames.launcher://apps/Fortnite",
		"platform": "epic", "online": true, "requires": []any{"epic"}})
	p, s := newFake()
	runJob(st, ok.ID, p, s, false)
	runJob(st, bad.ID, p, s, false)
	h := st.Snapshot().History
	if len(h) != 2 {
		t.Fatalf("записей: %d", len(h))
	}
	if h[0].GameName != "PUBG" || h[0].State != "done" {
		t.Errorf("успешный запуск: %+v", h[0])
	}
	if h[1].GameName != "Fortnite" || h[1].State != "error" || !strings.Contains(h[1].Message, "Epic Games") {
		t.Errorf("ошибка запуска: %+v", h[1])
	}
	for i := 0; i < historyLimit+5; i++ {
		st.AddHistory(HistoryEntry{Time: int64(i), GameName: "x", State: "done"})
	}
	if n := len(st.Snapshot().History); n != historyLimit {
		t.Errorf("лимит журнала: %d", n)
	}
	if st2, _ := NewStore(""); len(st2.Snapshot().History) != historyLimit {
		t.Error("журнал не сохраняется на диск")
	}
}

func TestNexusAPI(t *testing.T) {
	_, st := newTestStore(t)
	p, s := newFake()
	app := NewApp(st, NewProbe(p.snapshot), s)
	if err := app.Listen(0); err != nil {
		t.Fatal(err)
	}
	defer func() { app.Shutdown(); app.Wait() }()
	base := fmt.Sprintf("http://127.0.0.1:%d", app.Port)
	call := func(method, path string, body any) (int, map[string]any) {
		var r io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			r = strings.NewReader(string(raw))
		}
		req, _ := http.NewRequest(method, base+path, r)
		req.Header.Set("X-Zero-Token", app.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// Настройки интерфейса сохраняются в config.json
	code, res := call("PUT", "/api/settings", map[string]any{"accent": "cyan", "compact": true, "reduce_motion": true,
		"confirm_launch": true, "hide_toasts": true})
	if code != 200 {
		t.Fatalf("настройки: %d %v", code, res)
	}
	got := st.SettingsCopy()
	if got.Accent != "cyan" || !got.Compact || !got.ReduceMotion || !got.ConfirmLaunch || !got.HideToasts {
		t.Errorf("сохранено: %+v", got)
	}
	if code, _ = call("PUT", "/api/settings", map[string]any{"accent": "pink"}); code != 400 {
		t.Errorf("неизвестный акцент: %d", code)
	}
	if code, _ = call("PUT", "/api/settings", map[string]any{"compact": "yes"}); code != 400 {
		t.Errorf("не булево значение: %d", code)
	}
	if st2, _ := NewStore(""); st2.SettingsCopy().Accent != "cyan" {
		t.Error("акцент не сохранился на диск")
	}

	// Журнал и активные запуски приходят в состоянии
	_, res = call("POST", "/api/games", map[string]any{"name": "PUBG", "target": "steam://rungameid/578080",
		"platform": "steam", "online": true, "requires": []string{"steam"}})
	gid := res["game"].(map[string]any)["id"].(string)
	_, res = call("POST", "/api/games/"+gid+"/launch", map[string]any{})
	jid := res["job"].(map[string]any)["id"].(string)
	for i := 0; i < 100; i++ {
		_, res = call("GET", "/api/jobs/"+jid, nil)
		if res["job"].(map[string]any)["state"] != "running" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	_, state := call("GET", "/api/state", nil)
	hist := state["history"].([]any)
	if len(hist) != 1 || hist[0].(map[string]any)["game_name"] != "PUBG" || hist[0].(map[string]any)["state"] != "done" {
		t.Errorf("журнал в состоянии: %v", hist)
	}
	if jobs, ok := state["active_jobs"].([]any); !ok || len(jobs) != 0 {
		t.Errorf("активные запуски: %v", state["active_jobs"])
	}
	if state["settings"].(map[string]any)["accent"] != "cyan" {
		t.Errorf("настройки в состоянии: %v", state["settings"])
	}

	// Статика: шрифт и стили отдаются, выход за пределы web — нет
	for path, want := range map[string]string{"/": "text/html", "/styles.css": "text/css", "/app.js": "text/javascript",
		"/fonts/inter-400.woff": "font/woff", "/favicon.svg": "image/svg+xml"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), want) {
			t.Errorf("%s: %d %s", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	for _, path := range []string{"/../main.go", "/web/index.html", "/fonts/../../go.mod", "/main.go", "/%2e%2e/go.mod"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("%s не должен отдаваться", path)
		}
	}
}

func TestStaticFile(t *testing.T) {
	cases := map[string]bool{"/": true, "/app.js": true, "/fonts/inter-700.woff": true,
		"/../x.js": false, "/a/../b.js": false, "/x.exe": false, "/x": false, "//x.js": false}
	for p, want := range cases {
		if _, _, ok := staticFile(p); ok != want {
			t.Errorf("%s: %v", p, ok)
		}
	}
}
