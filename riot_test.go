package main

// VALORANT: если ярлык Riot только открыл клиент, Zero Launcher нажимает «Играть» через локальный API клиента.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeRiotClient — локальный сервер Riot Client: принимает «Играть» и запускает процесс игры.
type fakeRiotClient struct {
	mu       sync.Mutex
	procs    *fakeProcs
	hits     int
	refuse   int // сколько первых запросов отклонить (вход ещё не выполнен)
	status   int // чем отвечать при отказе
	lastPath string
	authOK   bool
}

func (f *fakeRiotClient) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits++
	f.lastPath = r.Method + " " + r.URL.Path
	user, pass, ok := r.BasicAuth()
	f.authOK = ok && user == "riot" && pass == "s3cret"
	if !f.authOK {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.hits <= f.refuse {
		w.WriteHeader(f.status)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/product-launcher/v1/products/valorant/patchlines/live" {
		f.procs.add("valorant-win64-shipping.exe")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, r)
}

func riotSetup(t *testing.T, refuse, status int) (*Store, Game, *fakeProcs, *fakeStarter, *fakeRiotClient) {
	fw, st := newTestStore(t)
	g := st.AddGame(map[string]any{"name": "VALORANT", "target": "{riot}", "platform": "riot",
		"args": "--launch-product=valorant --launch-patchline=live"})
	p, s := newFake()
	// команда из ярлыка открывает только клиент — как у пользователя
	s.hook = func(n int, base, args string) []string { return []string{"riotclientservices.exe"} }
	rc := &fakeRiotClient{procs: p, refuse: refuse, status: status}
	srv := httptest.NewTLSServer(rc)
	t.Cleanup(srv.Close)
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	touch(t, fw.root+"/Local/Riot Games/Riot Client/Config/lockfile", fmt.Sprintf("Riot Client:4242:%s:s3cret:https", port))
	return st, g, p, s, rc
}

func TestRiotPlayViaClientAPI(t *testing.T) {
	st, g, p, s, rc := riotSetup(t, 0, 0)
	v := runJob(st, g.ID, p, s, false)
	if v.State != "done" || v.Steps[len(v.Steps)-1].Message != "Игра запущена" {
		t.Fatalf("игра должна запуститься через «Играть»: %+v", v)
	}
	if rc.hits == 0 || !rc.authOK || rc.lastPath != "POST /product-launcher/v1/products/valorant/patchlines/live" {
		t.Errorf("запрос к клиенту: %d %v %s", rc.hits, rc.authOK, rc.lastPath)
	}
	if h := st.Snapshot().History; len(h) == 0 || h[len(h)-1].State != "done" {
		t.Errorf("журнал: %+v", h)
	}
}

func TestRiotWaitsForLogin(t *testing.T) {
	// первые запросы клиент отклоняет (идёт вход) — Zero Launcher ждёт дольше обычного и запускает после входа
	st, g, p, s, rc := riotSetup(t, 30, http.StatusConflict)
	v := runJob(st, g.ID, p, s, false)
	if v.State != "done" || v.Steps[len(v.Steps)-1].Message != "Игра запущена" {
		t.Fatalf("после входа игра должна запуститься: %+v", v.Steps[len(v.Steps)-1])
	}
	if rc.hits <= 30 {
		t.Errorf("запросов: %d", rc.hits)
	}
}

func TestRiotClientRefuses(t *testing.T) {
	st, g, p, s, _ := riotSetup(t, 1<<30, http.StatusConflict)
	v := runJob(st, g.ID, p, s, false)
	last := v.Steps[len(v.Steps)-1]
	if v.State != "done" || last.State != "warn" || !strings.Contains(last.Message, "409") {
		t.Fatalf("клиент не запускает игру — честное предупреждение: %+v", last)
	}
	for _, x := range st.Snapshot().History {
		if x.State == "done" {
			t.Error("в журнале не должно быть «Запущено», игра не появилась")
		}
	}
}

func TestRiotLockfile(t *testing.T) {
	fw, _ := newTestStore(t)
	path := fw.root + "/Local/Riot Games/Riot Client/Config/lockfile"
	if _, ok := readRiotLockfile(); ok {
		t.Fatal("lockfile нет — клиент не запущен")
	}
	for raw, ok := range map[string]bool{
		"Riot Client:123:54321:pass:https":    true,
		"Riot Client:123:54321:pass:ftp":      false,
		"Riot Client:123:notaport:pass:https": false,
		"garbage":                             false,
	} {
		touch(t, path, raw)
		if _, got := readRiotLockfile(); got != ok {
			t.Errorf("%q: %v", raw, got)
		}
	}
	// неверный пароль (старый lockfile) — «клиент не готов», а не ошибка запуска
	rc := &fakeRiotClient{procs: &fakeProcs{running: map[string]bool{}}}
	srv := httptest.NewTLSServer(rc)
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	touch(t, path, "Riot Client:1:"+port+":wrong:https")
	if err := riotLaunchViaAPI("valorant", "live"); err != errRiotNotReady {
		t.Errorf("старый пароль: %v", err)
	}
}
