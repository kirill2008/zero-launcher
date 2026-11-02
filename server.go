package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

// Файлы интерфейса берутся только из встроенной папки web и только этих типов.
var staticTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".svg":  "image/svg+xml",
	".woff": "font/woff",
	".png":  "image/png",
	".txt":  "text/plain; charset=utf-8",
}

var staticPathRe = regexp.MustCompile(`^/[A-Za-z0-9_\-]+(/[A-Za-z0-9_\-]+)*\.[a-z0-9]+$`)

// staticFile сопоставляет адрес файлу во встроенной папке web (без выхода за её пределы).
func staticFile(path string) (string, string, bool) {
	if path == "/" {
		path = "/index.html"
	}
	if !staticPathRe.MatchString(path) || strings.Contains(path, "..") {
		return "", "", false
	}
	ctype, ok := staticTypes[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return "", "", false
	}
	return "web" + path, ctype, true
}

const csp = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; " +
	"connect-src 'self'; font-src 'self' data:; base-uri 'none'; frame-ancestors 'none'"

type apiError struct {
	msg    string
	status int
}

func (e *apiError) Error() string { return e.msg }
func bad(msg string) error        { return &apiError{msg, 400} }
func notFound(msg string) error   { return &apiError{msg, 404} }

type App struct {
	Store   *Store
	Probe   *ProcessProbe
	Starter Starter
	Token   string
	Port    int

	Art *ArtStore

	jobsMu   sync.Mutex
	jobs     map[string]*LaunchJob
	pingMu   sync.Mutex
	lastPing time.Time

	bg       sync.WaitGroup // фоновые задачи: запуски, проверка картинок
	stop     chan struct{}
	stopOnce sync.Once
	byeAt    time.Time
	srv      *http.Server
}

func NewApp(st *Store, probe *ProcessProbe, starter Starter) *App {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	if probe == nil {
		probe = NewProbe(nil)
	}
	if starter == nil {
		starter = sysStarter{}
	}
	a := &App{Store: st, Probe: probe, Starter: starter, Token: base64.RawURLEncoding.EncodeToString(b),
		jobs: map[string]*LaunchJob{}, lastPing: time.Now(), Art: NewArtStore(st.Base), stop: make(chan struct{})}
	migrateLegacyCovers(st)
	return a
}

func (a *App) URL() string { return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", a.Port, a.Token) }

// Listen занимает порт на 127.0.0.1 (0 — любой свободный) и начинает обслуживать запросы.
func (a *App) Listen(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	a.Port = ln.Addr().(*net.TCPAddr).Port
	a.srv = &http.Server{Handler: a, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = a.srv.Serve(ln) }()
	return nil
}

// Shutdown останавливает сервер и периодическую проверку картинок.
func (a *App) Shutdown() {
	a.stopOnce.Do(func() { close(a.stop) })
	if a.srv != nil {
		_ = a.srv.Close()
	}
}

// background запускает фоновую задачу так, чтобы её можно было дождаться (Wait).
func (a *App) background(fn func()) {
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		fn()
	}()
}

// Wait ждёт фоновые задачи — после Shutdown, чтобы ничего не работало с уже закрытым состоянием.
func (a *App) Wait() { a.bg.Wait() }

func (a *App) pingState() (time.Time, time.Time) {
	a.pingMu.Lock()
	defer a.pingMu.Unlock()
	return a.lastPing, a.byeAt
}

// ------------------------------------------------------------------ состояние для интерфейса

type GameView struct {
	Game
	Ready          bool        `json:"ready"`
	Problem        string      `json:"problem"`
	GTA            bool        `json:"gta"`
	TargetResolved string      `json:"target_resolved"`
	Placeholder    string      `json:"placeholder"`
	Rage           bool        `json:"rage"`
	Chain          []ChainView `json:"chain"`
	CoverURL       string      `json:"cover_url"`
	HeroURL        string      `json:"hero_url"`
	LogoURL        string      `json:"logo_url"`
	IconURL        string      `json:"icon_url"`
	ArtFrom        string      `json:"art_from"` // откуда обложка: Steam, Epic Games…
	Desc           string      `json:"desc"`
}

// LauncherView — лаунчер для интерфейса вместе с иконкой из его .exe.
type LauncherView struct {
	*Launcher
	IconURL string `json:"icon_url"`
}

type ChainView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Forced  bool   `json:"forced"`
	Missing bool   `json:"missing"`
}

// customCover — своя обложка игры, если она выбрана и файл на месте.
func customCover(g *Game) (string, time.Time, bool) {
	if !g.CoverCustom || g.Cover == "" {
		return "", time.Time{}, false
	}
	st, err := os.Stat(g.Cover)
	if err != nil || st.IsDir() {
		return "", time.Time{}, false
	}
	return g.Cover, st.ModTime(), true
}

func (a *App) artURLs(g *Game) (cover, hero, logo, icon, from string) {
	u := func(kind string) (string, string) {
		if it, ok := a.Art.Item(g.ID, kind); ok {
			return fmt.Sprintf("/img/%s/%s?v=%s", kind, g.ID, it.Ver), it.From
		}
		return "", ""
	}
	if _, mod, ok := customCover(g); ok {
		cover, from = fmt.Sprintf("/img/cover/%s?v=c%d", g.ID, mod.Unix()), "custom"
	} else {
		cover, from = u("cover")
	}
	hero, _ = u("hero")
	logo, _ = u("logo")
	icon, _ = u("icon")
	return
}

func (a *App) state() map[string]any {
	launchers := resolveAll(a.Store)
	ls := map[string]*Launcher{}
	snap := a.Probe.Snapshot()
	for _, l := range launchers {
		ls[l.ID] = l
		l.Running = l.Path != "" && a.Probe.IsRunning(l, snap)
	}
	cfg := a.Store.Snapshot()
	games := []GameView{}
	tpls := templateList()
	for _, g := range cfg.Games {
		ok, why := targetReady(g, ls)
		pl := launchPlan(g, ls)
		chain := []ChainView{}
		for _, c := range planChain(g, ls) {
			chain = append(chain, ChainView{c.L.ID, c.L.Name, c.Forced, !c.L.Exists})
		}
		if g.Requires == nil {
			g.Requires = []string{}
		}
		cover, hero, logo, icon, from := a.artURLs(g)
		games = append(games, GameView{Game: *g, Ready: ok, Problem: why, GTA: isGTA(g), TargetResolved: pl.Target,
			Placeholder: pl.Via, Rage: pl.Rage, Chain: chain, CoverURL: cover, HeroURL: hero, LogoURL: logo, IconURL: icon,
			ArtFrom: from, Desc: gameDesc(g, tpls)})
	}
	lviews := make([]LauncherView, 0, len(launchers))
	for _, l := range launchers {
		lv := LauncherView{Launcher: l}
		if it, ok := a.Art.LauncherIcon(l.ID); ok {
			lv.IconURL = fmt.Sprintf("/img/launcher/%s?v=%s", l.ID, it.Ver)
		}
		lviews = append(lviews, lv)
	}
	libUpdated := cfg.LibraryChanged
	if t := a.Art.Updated(); t > libUpdated {
		libUpdated = t
	}
	return map[string]any{
		"version":           appVersion,
		"is_windows":        isWindows,
		"games":             games,
		"launchers":         lviews,
		"settings":          cfg.Settings,
		"library_folders":   cfg.LibraryFolders,
		"platforms":         orderedMap(platforms),
		"platform_requires": platformRequires,
		"data_dir":          a.Store.Base,
		"rp_servers":        serverList(),
		"history":           newestFirst(cfg.History),
		"active_jobs":       a.activeJobs(),
		"art_rev":           a.Art.Rev(),
		"library_updated":   libUpdated,
		"avg_launch_ms":     avgLaunch(cfg.History),
	}
}

// avgLaunch — среднее время успешного запуска по последним десяти записям журнала, мс (0 — данных нет).
func avgLaunch(h []HistoryEntry) int64 {
	var sum, n int64
	for i := len(h) - 1; i >= 0 && n < 10; i-- {
		if h[i].Duration > 0 && (h[i].State == "done" || h[i].State == "warn") {
			sum += h[i].Duration
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

func newestFirst(h []HistoryEntry) []HistoryEntry {
	out := make([]HistoryEntry, 0, len(h))
	for i := len(h) - 1; i >= 0; i-- {
		out = append(out, h[i])
	}
	return out
}

// activeJobs — запуски, которые идут прямо сейчас (в том числе начатые в другом окне).
func (a *App) activeJobs() []map[string]string {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	var running []*LaunchJob
	for _, j := range a.jobs {
		if j.State() == "running" {
			running = append(running, j)
		}
	}
	// по времени начала: список одновременных запусков не прыгает между обновлениями
	sort.Slice(running, func(i, k int) bool {
		if !running[i].started.Equal(running[k].started) {
			return running[i].started.Before(running[k].started)
		}
		return running[i].ID < running[k].ID
	})
	out := []map[string]string{}
	for _, j := range running {
		out = append(out, map[string]string{"id": j.ID, "game_id": j.GameID})
	}
	return out
}

// ------------------------------------------------------------------ обложки и иконки

// StartArt сразу проверяет картинки, а потом сверяет источники каждые несколько минут.
func (a *App) StartArt() {
	a.background(func() {
		online := !a.Store.SettingsCopy().NoOnlineArt
		if n := a.Art.RefreshAll(a.Store, false, online); n > 0 {
			logger.Printf("Обложки и иконки: обновлено %d", n)
		}
		t := time.NewTicker(artPoll)
		defer t.Stop()
		for {
			select {
			case <-a.stop:
				return
			case <-t.C:
				if n := a.Art.RefreshAll(a.Store, false, !a.Store.SettingsCopy().NoOnlineArt); n > 0 {
					logger.Printf("Обложки и иконки: обновлено %d", n)
				}
			}
		}
	})
}

func (a *App) artGameAsync(id string) { a.background(func() { a.Art.RefreshGame(a.Store, id, false) }) }

func (a *App) artAllAsync(force bool) {
	a.background(func() { a.Art.RefreshAll(a.Store, force, !a.Store.SettingsCopy().NoOnlineArt) })
}

// ------------------------------------------------------------------ запуск

func (a *App) Launch(gid string, skip bool) *LaunchJob {
	a.jobsMu.Lock()
	for id, j := range a.jobs {
		if f := j.FinishedAt(); !f.IsZero() && time.Since(f) > 10*time.Minute {
			delete(a.jobs, id)
		}
	}
	for _, j := range a.jobs {
		if j.GameID == gid && j.State() == "running" {
			a.jobsMu.Unlock()
			return j
		}
	}
	j := NewLaunchJob(a.Store, gid, skip, a.Probe, a.Starter)
	j.onDone = func(id string) { a.Art.RefreshGame(a.Store, id, false) }
	a.jobs[j.ID] = j
	a.jobsMu.Unlock()
	a.background(j.Run)
	return j
}

// ------------------------------------------------------------------ HTTP

func (a *App) send(w http.ResponseWriter, status int, body any) {
	raw, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.Host)
	if host != fmt.Sprintf("127.0.0.1:%d", a.Port) && host != fmt.Sprintf("localhost:%d", a.Port) {
		a.send(w, 403, map[string]string{"error": "Неверный адрес"})
		return
	}
	path := r.URL.Path
	if r.Method == http.MethodGet && !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/img/") {
		file, ctype, ok := staticFile(path)
		body, err := webFS.ReadFile(file)
		if !ok || err != nil {
			a.send(w, 404, map[string]string{"error": "Файл интерфейса не найден"})
			return
		}
		w.Header().Set("Content-Type", ctype)
		if strings.HasPrefix(path, "/fonts/") {
			w.Header().Set("Cache-Control", "max-age=86400")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasSuffix(file, ".html") {
			w.Header().Set("Content-Security-Policy", csp)
		}
		_, _ = w.Write(body)
		return
	}
	tok := r.Header.Get("X-Zero-Token")
	if tok == "" {
		tok = r.URL.Query().Get("t")
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(a.Token)) != 1 {
		a.send(w, 403, map[string]string{"error": "Нет доступа"})
		return
	}
	if strings.HasPrefix(path, "/img/") {
		a.image(w, path)
		return
	}
	if !strings.HasPrefix(path, "/api/") {
		a.send(w, 404, map[string]string{"error": "Не найдено"})
		return
	}
	body := map[string]any{}
	if r.Method != http.MethodGet && r.ContentLength != 0 {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 5_000_000))
		if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				a.send(w, 400, map[string]string{"error": "Некорректный JSON"})
				return
			}
		}
	}
	res, err := a.safeAPI(r.Method, strings.TrimPrefix(path, "/api/"), body)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			a.send(w, ae.status, map[string]string{"error": ae.msg})
		} else {
			logger.Printf("Ошибка %s %s: %v", r.Method, path, err)
			a.send(w, 500, map[string]string{"error": "Внутренняя ошибка. Подробности — в журнале zero-launcher.log"})
		}
		return
	}
	if res == nil {
		res = map[string]any{"ok": true}
	}
	a.send(w, 200, res)
}

var imgRe = regexp.MustCompile(`^/img/(cover|hero|logo|icon|launcher)/([A-Za-z0-9_\-]+)$`)

func (a *App) image(w http.ResponseWriter, path string) {
	m := imgRe.FindStringSubmatch(path)
	if m == nil {
		a.send(w, 404, map[string]string{"error": "Не найдено"})
		return
	}
	kind, id := m[1], m[2]
	file := ""
	switch kind {
	case "launcher":
		if it, ok := a.Art.LauncherIcon(id); ok {
			file = a.Art.FilePath(it)
		}
	default:
		g, ok := a.Store.Game(id)
		if !ok {
			break
		}
		if p, _, custom := customCover(&g); kind == "cover" && custom {
			file = p
		} else if it, ok := a.Art.Item(id, kind); ok {
			file = a.Art.FilePath(it)
		}
	}
	ctype := artContentType(file)
	if file == "" || ctype == "" {
		if ext := strings.ToLower(filepath.Ext(file)); ext == ".jpeg" {
			ctype = "image/jpeg"
		} else if ext == ".bmp" {
			ctype = "image/bmp"
		}
	}
	if file == "" || ctype == "" {
		a.send(w, 404, map[string]string{"error": "Нет картинки"})
		return
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		a.send(w, 404, map[string]string{"error": "Нет картинки"})
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=604800") // в адресе есть версия картинки
	_, _ = w.Write(raw)
}

func (a *App) safeAPI(method, route string, body map[string]any) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return a.api(method, route, body)
}

func (a *App) api(method, route string, body map[string]any) (any, error) {
	parts := []string{}
	for _, p := range strings.Split(route, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	is := func(m string, p ...string) bool {
		if method != m || len(parts) != len(p) {
			return false
		}
		for i := range p {
			if p[i] != "*" && p[i] != parts[i] {
				return false
			}
		}
		return true
	}
	st := a.Store

	switch {
	case len(parts) == 1 && parts[0] == "ping":
		a.pingMu.Lock()
		a.lastPing, a.byeAt = time.Now(), time.Time{}
		a.pingMu.Unlock()
		return nil, nil
	case len(parts) == 1 && parts[0] == "bye":
		a.pingMu.Lock()
		a.byeAt = time.Now()
		a.pingMu.Unlock()
		return nil, nil
	case is("GET", "state"):
		return a.state(), nil
	case is("GET", "templates"):
		return map[string]any{"templates": buildTemplates(launchersByID(st))}, nil

	// ---- игры
	case is("POST", "games"):
		if strings.TrimSpace(toStr(body["name"])) == "" {
			return nil, bad("Введите название игры")
		}
		g := st.AddGame(body)
		a.artGameAsync(g.ID)
		return map[string]any{"game": g}, nil
	case is("PUT", "games", "*"):
		g, ok := st.UpdateGame(parts[1], body)
		if !ok {
			return nil, notFound("Игра не найдена")
		}
		if _, onlyFav := body["favorite"]; !onlyFav || len(body) > 1 {
			a.artGameAsync(g.ID)
		}
		return map[string]any{"game": g}, nil
	case is("DELETE", "games", "*"):
		if !st.DeleteGame(parts[1]) {
			return nil, notFound("Игра не найдена")
		}
		a.Art.Forget(parts[1])
		return nil, nil
	case is("POST", "games", "*", "*"):
		g, ok := st.Game(parts[1])
		if !ok {
			return nil, notFound("Игра не найдена")
		}
		ls := launchersByID(st)
		t, _ := resolveTarget(&g, ls)
		switch parts[2] {
		case "launch":
			return map[string]any{"job": a.Launch(g.ID, toBool(body["skip_missing"])).View()}, nil
		case "folder":
			where := g.InstallDir
			if where == "" && isFile(t) {
				where = t
			}
			if where == "" || openInExplorer(where) != nil {
				return nil, bad("Папка игры неизвестна: игра запускается по ссылке. Укажите путь к .exe, чтобы открыть папку.")
			}
			return nil, nil
		case "shortcut":
			icon := g.IconSrc
			if icon == "" {
				icon = t
			}
			p, err := createShortcut(&g, icon)
			if err != nil {
				return nil, bad(err.Error())
			}
			return map[string]any{"path": p}, nil
		case "art":
			n := a.Art.RefreshGame(st, g.ID, true)
			return map[string]any{"changed": n}, nil
		}

	// ---- задачи запуска
	case len(parts) >= 2 && parts[0] == "jobs":
		a.jobsMu.Lock()
		j := a.jobs[parts[1]]
		a.jobsMu.Unlock()
		if j == nil {
			return nil, notFound("Запуск не найден")
		}
		if len(parts) == 3 && parts[2] == "cancel" && method == "POST" {
			j.Cancel()
		}
		return map[string]any{"job": j.View()}, nil

	// ---- лаунчеры
	case is("POST", "launchers", "detect"):
		return map[string]any{"changed": detectMissing(st, true)}, nil
	case is("POST", "launchers"):
		if strings.TrimSpace(toStr(body["path"])) == "" {
			return nil, bad("Укажите путь к .exe лаунчера")
		}
		return map[string]any{"launcher": addCustomLauncher(st, body)}, nil
	case is("PUT", "launchers", "*"):
		if v, has := body["delay"]; has {
			if _, ok := toInt(v); !ok {
				return nil, bad("Пауза должна быть числом секунд")
			}
		}
		if !updateLauncher(st, parts[1], body) {
			return nil, notFound("Лаунчер не найден")
		}
		return nil, nil
	case is("DELETE", "launchers", "*"):
		if !deleteCustomLauncher(st, parts[1]) {
			return nil, bad("Встроенные лаунчеры удалить нельзя — можно просто очистить путь")
		}
		return nil, nil
	case is("POST", "launchers", "*", "start"):
		l := launchersByID(st)[parts[1]]
		if l == nil || !l.Exists {
			return nil, bad("Сначала укажите путь к лаунчеру")
		}
		if err := a.Starter.Start(l.Path, l.Args, filepath.Dir(l.Path), false); err != nil {
			return nil, bad(err.Error())
		}
		a.Probe.Invalidate()
		return nil, nil

	// ---- импорт
	case is("POST", "scan"):
		var sources []string
		if v, has := body["sources"]; has && v != nil {
			sources = toStrSlice(v)
		}
		folder := strings.TrimSpace(toStr(body["folder"]))
		var extra []string
		if folder != "" {
			if !isDir(folder) {
				return nil, bad("Папка не найдена")
			}
			extra = []string{folder}
		}
		return map[string]any{"items": scanAll(st, launchersByID(st), sources, extra)}, nil
	case is("POST", "import"):
		added := []string{}
		items, _ := body["items"].([]any)
		for _, raw := range items {
			it, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fields := map[string]any{}
			for _, k := range gameKeys {
				if v, has := it[k]; has {
					fields[k] = v
				}
			}
			delete(fields, "cover") // обложка подберётся автоматически и будет обновляться
			fields["icon_src"] = it["icon_src"]
			name := strings.TrimSpace(toStr(it["name"]))
			if name == "" || st.FindDuplicate(toStr(it["source_id"]), toStr(it["target"]), toStr(it["args"])) {
				continue
			}
			added = append(added, st.AddGame(fields).ID)
		}
		if len(added) > 0 {
			a.artAllAsync(false)
		}
		return map[string]any{"added": added}, nil

	// ---- обложки и проверка файлов
	case is("POST", "art", "refresh"):
		a.artAllAsync(true)
		return map[string]any{"started": true}, nil
	case is("POST", "check"):
		return a.checkFiles(), nil

	// ---- диалоги и настройки
	case is("POST", "browse"):
		if !isWindows {
			return nil, bad("Окно выбора файла работает только в Windows — впишите путь вручную")
		}
		kind := toStr(body["kind"])
		switch kind {
		case "game", "exe", "image", "folder":
		default:
			kind = "game"
		}
		p, err := browseDialog(kind, toStr(body["title"]), toStr(body["initial"]))
		if err != nil {
			return nil, bad("Не удалось открыть окно выбора: " + err.Error())
		}
		return map[string]any{"path": p}, nil
	case is("PUT", "settings"):
		var out Settings
		err := st.Update(func(c *Config) error {
			s := &c.Settings
			if v := toStr(body["after_launch"]); v == "none" || v == "close" {
				s.AfterLaunch = v
			}
			if v, has := body["launcher_timeout"]; has {
				n, ok := toInt(v)
				if !ok {
					return bad("Время ожидания должно быть числом")
				}
				s.LauncherTimeout = clamp(n, 10, 600)
			}
			if v, has := body["sort"]; has {
				if !sortModes[toStr(v)] {
					return bad("Неизвестная сортировка")
				}
				s.Sort = toStr(v)
			}
			if v, has := body["view"]; has {
				if x := toStr(v); x != "grid" && x != "list" {
					return bad("Вид библиотеки: grid или list")
				} else {
					s.View = x
				}
			}
			if v, has := body["accent"]; has {
				if !accents[toStr(v)] {
					return bad("Неизвестный цвет акцента")
				}
				s.Accent = toStr(v)
			}
			for key, dst := range map[string]*bool{"compact": &s.Compact, "reduce_motion": &s.ReduceMotion,
				"confirm_launch": &s.ConfirmLaunch, "hide_toasts": &s.HideToasts, "no_online_art": &s.NoOnlineArt} {
				if v, has := body[key]; has {
					b, ok := v.(bool)
					if !ok {
						return bad("Неверное значение настройки " + key)
					}
					*dst = b
				}
			}
			out = *s
			return nil
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"settings": out}, nil
	case len(parts) == 1 && parts[0] == "folders" && (method == "POST" || method == "DELETE"):
		folder := strings.TrimSpace(toStr(body["path"]))
		var out []string
		err := st.Update(func(c *Config) error {
			if method == "POST" {
				if !isDir(folder) {
					return bad("Такой папки нет")
				}
				exists := false
				for _, f := range c.LibraryFolders {
					if norm(f) == norm(folder) {
						exists = true
					}
				}
				if !exists {
					c.LibraryFolders = append(c.LibraryFolders, filepath.Clean(folder))
				}
			} else {
				keep := []string{}
				for _, f := range c.LibraryFolders {
					if norm(f) != norm(folder) {
						keep = append(keep, f)
					}
				}
				c.LibraryFolders = keep
			}
			out = append([]string{}, c.LibraryFolders...)
			return nil
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"library_folders": out}, nil
	case is("POST", "data-dir"):
		_ = openInExplorer(st.Base)
		return nil, nil
	}
	return nil, notFound("Неизвестный запрос")
}

func serverList() []map[string]string {
	out := []map[string]string{}
	for _, p := range rpServers {
		out = append(out, map[string]string{"name": p.K, "address": p.V})
	}
	return out
}

// checkFiles — «Проверить файлы»: пути игр и лаунчеров, поиск пропавших лаунчеров, обложки и иконки.
func (a *App) checkFiles() map[string]any {
	st := a.Store
	found := detectMissing(st, false)
	ls := launchersByID(st)
	cfg := st.Snapshot()
	problems := []map[string]string{}
	for _, g := range cfg.Games {
		if ok, why := targetReady(g, ls); !ok {
			problems = append(problems, map[string]string{"id": g.ID, "name": g.Name, "problem": why})
			continue
		}
		for _, c := range planChain(g, ls) {
			if !c.L.Exists {
				problems = append(problems, map[string]string{"id": g.ID, "name": g.Name,
					"problem": "Нет пути к " + c.L.Name, "launcher_id": c.L.ID})
				break
			}
		}
	}
	names := []string{}
	for _, id := range found {
		if l := ls[id]; l != nil && l.Exists {
			names = append(names, l.Name)
		}
	}
	changed, done := a.Art.TryRefreshAll(st, false, false) // локальные источники сразу
	a.artAllAsync(false)                                   // картинки из интернета — в фоне
	return map[string]any{"games": len(cfg.Games), "problems": problems, "launchers_found": names,
		"art_changed": changed, "art_busy": !done}
}

// migrateLegacyCovers: версии до 2.0 копировали обложку Steam в папку covers и больше её не обновляли.
// Такие копии убираем — дальше обложка подбирается и обновляется автоматически. Свои картинки остаются.
func migrateLegacyCovers(st *Store) {
	cfg := st.Snapshot()
	pending := false
	for _, g := range cfg.Games {
		if g.Cover != "" && !g.CoverCustom {
			pending = true
		}
	}
	if !pending {
		return
	}
	root := steamRoot(launchersByID(st))
	auto := map[string]bool{}
	for _, g := range cfg.Games {
		if g.Cover == "" || g.CoverCustom {
			continue
		}
		mine, err := os.ReadFile(g.Cover)
		if err != nil {
			continue
		}
		id := steamAppID(g)
		var cands []string
		if id != "" && root != "" {
			cache := filepath.Join(root, "appcache", "librarycache")
			cands = append(cands, steamCacheFile(root, id, steamCacheFiles["cover"]),
				filepath.Join(cache, id+"_header.jpg"), filepath.Join(cache, id, "header.jpg"))
		}
		for _, c := range cands {
			if c == "" {
				continue
			}
			if other, err := os.ReadFile(c); err == nil && len(other) == len(mine) && string(other) == string(mine) {
				auto[g.ID] = true
				break
			}
		}
	}
	_ = st.Update(func(c *Config) error {
		for _, g := range c.Games {
			if g.Cover == "" || g.CoverCustom {
				continue
			}
			if auto[g.ID] {
				if filepath.Dir(g.Cover) == st.CoversDir {
					_ = os.Remove(g.Cover)
				}
				g.Cover = ""
			} else {
				g.CoverCustom = true
			}
		}
		return nil
	})
}
