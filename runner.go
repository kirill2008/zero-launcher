package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	gtaExes       = map[string]bool{"gta5.exe": true, "gta5_enhanced.exe": true, "playgtav.exe": true, "gtavlauncher.exe": true, "ragemp_v.exe": true}
	gtaNameRe     = regexp.MustCompile(`(?i)\bgta\b|grand theft auto|(^|[^\p{L}\p{N}])гта([^\p{L}\p{N}]|$)|gta\s*(5|v)\b|gta5|gtav`)
	gtaSources    = map[string]bool{"steam:271590": true, "steam:3240220": true, "epic:9d2d0eb64d5c44529cece33fe2a46482": true}
	placeholderRe = regexp.MustCompile(`^\{([A-Za-z0-9_]+)\}$`)
)

// LaunchError — понятная причина, по которой запуск остановился.
type LaunchError struct {
	Msg, Code, LauncherID string
}

func (e *LaunchError) Error() string { return e.Msg }

// ------------------------------------------------------------------ правила

func isGTA(g *Game) bool {
	if g.Platform == "rage" || gtaSources[g.SourceID] || gtaNameRe.MatchString(g.Name) {
		return true
	}
	t := strings.TrimSpace(g.Target)
	if t == "{rage}" || gtaExes[strings.ToLower(baseName(t))] {
		return true
	}
	return strings.Contains(strings.ToLower(t), "ragemp")
}

// resolveTarget подставляет путь лаунчера вместо {rage}, {riot} и т.п.
func resolveTarget(g *Game, ls map[string]*Launcher) (string, string) {
	t := strings.Trim(strings.TrimSpace(g.Target), `"`)
	if m := placeholderRe.FindStringSubmatch(t); m != nil {
		if l := ls[m[1]]; l != nil {
			return l.Path, m[1]
		}
		return "", m[1]
	}
	return t, ""
}

// ------------------------------------------------------------------ Riot: игра, а не только клиент

var (
	riotClientExes = map[string]bool{"riotclientservices.exe": true, "riot client.exe": true, "riotclientux.exe": true}
	riotGameExes   = map[string]string{"valorant.exe": "valorant", "valorant-win64-shipping.exe": "valorant",
		"leagueclient.exe": "league_of_legends", "league of legends.exe": "league_of_legends", "lor.exe": "bacon"}
	riotNameRes = []struct {
		re      *regexp.Regexp
		product string
	}{
		{regexp.MustCompile(`(?i)valorant|валорант`), "valorant"},
		{regexp.MustCompile(`(?i)league of legends|\blol\b|лига легенд|(^|[^\p{L}])лол([^\p{L}]|$)`), "league_of_legends"},
		{regexp.MustCompile(`(?i)runeterra|\blor\b`), "bacon"},
	}
)

// Процессы игр Riot: по ним видно, что стартовала игра, а не только клиент.
var riotGameProcs = map[string][]string{
	"valorant":          {"VALORANT-Win64-Shipping.exe", "VALORANT.exe"},
	"league_of_legends": {"LeagueClient.exe", "LeagueClientUx.exe", "League of Legends.exe"},
	"bacon":             {"LoR.exe"},
}

var launchProductRe = regexp.MustCompile(`--launch-product=([A-Za-z0-9_]+)`)

// Plan — что именно запускать на шаге игры.
type Plan struct {
	Target string // путь к .exe или ссылка
	Args   string
	Via    string   // id лаунчера, который сам и есть игра (rage, riot, gta5rp) — его не открываем отдельно
	Rage   bool     // GTA 5 RP: можно выбрать сервер
	Direct bool     // запуск через RAGE MP с прямым подключением к серверу
	Watch  []string // процессы игры, появления которых стоит дождаться (Riot)
}

// launchPlan подставляет пути лаунчеров и исправляет частые ошибки:
// VALORANT.exe или Riot Client без аргументов открывают только клиент, а не игру.
func launchPlan(g *Game, ls map[string]*Launcher) Plan {
	t, via := resolveTarget(g, ls)
	p := Plan{Target: t, Args: strings.TrimSpace(g.Args), Via: via}
	base := strings.ToLower(baseName(t))
	isRageExe := base == "ragemp_v.exe" || (base == "updater.exe" && strings.Contains(strings.ToLower(t), "ragemp"))
	isRPLauncher := via == "gta5rp" || (t != "" && !isURI(t) && gta5rpRe.MatchString(base))
	p.Rage = via == "rage" || isRageExe || isRPLauncher || (g.Platform == "rage" && t == "" && via == "")
	if p.Rage {
		return gta5rpPlan(g, ls, p, isRageExe, isRPLauncher)
	}

	if m := launchProductRe.FindStringSubmatch(p.Args); m != nil {
		p.Watch = riotGameProcs[m[1]]
		if riotClientExes[base] {
			p.Target, p.Via = riotServices(p.Target), "riot"
		}
		if !strings.Contains(p.Args, "--launch-patchline") {
			p.Args += " --launch-patchline=live"
		}
		return p
	}
	isClient := riotClientExes[base] || via == "riot"
	product := riotGameExes[base]
	if product == "" && (isClient || g.Platform == "riot") {
		for _, r := range riotNameRes {
			if r.re.MatchString(g.Name) {
				product = r.product
				break
			}
		}
	}
	if product == "" {
		return p
	}
	client := ""
	if isClient {
		client = t
	}
	if client == "" {
		if l := ls["riot"]; l != nil && l.Exists {
			client = l.Path
		}
	}
	client = riotServices(client)
	if client == "" && t != "" && !isURI(t) { // ...\Riot Games\VALORANT\live\VALORANT.exe → ...\Riot Games\Riot Client
		dir := filepath.Dir(t)
		for i := 0; i < 4 && dir != filepath.Dir(dir); i++ {
			if c := filepath.Join(dir, "Riot Client", "RiotClientServices.exe"); isFile(c) {
				client = c
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	if client == "" {
		if via == "riot" || g.Platform == "riot" {
			p.Target, p.Via = "", "riot" // попросим указать путь к Riot Client
		}
		return p
	}
	p.Target, p.Via = client, "riot"
	p.Args = strings.TrimSpace("--launch-product=" + product + " --launch-patchline=live " + p.Args)
	p.Watch = riotGameProcs[product]
	return p
}

// riotServices: команды запуска игры понимает только RiotClientServices.exe, а не «Riot Client.exe».
func riotServices(path string) string {
	if path == "" || strings.EqualFold(baseName(path), "RiotClientServices.exe") {
		return path
	}
	dir := filepath.Dir(path)
	for i := 0; i < 3 && dir != filepath.Dir(dir); i++ {
		if c := filepath.Join(dir, "RiotClientServices.exe"); isFile(c) {
			return c
		}
		dir = filepath.Dir(dir)
	}
	return path
}

// gta5rpPlan — запуск GTA 5 RP. Выбранный сервер пишется туда же, куда его пишет официальный
// лаунчер GTA5RP, и RAGE MP (updater.exe) сразу подключается к нему. Если выбран вход через
// официальный лаунчер или RAGE MP не найден, открывается лаунчер GTA5RP.
func gta5rpPlan(g *Game, ls map[string]*Launcher, p Plan, isRageExe, isRPLauncher bool) Plan {
	rage, rp := ls["rage"], ls["gta5rp"]
	ragePath := ""
	if isRageExe && isFile(p.Target) {
		ragePath = p.Target
	} else if rage != nil && rage.Exists {
		ragePath = rage.Path
	}
	if strings.EqualFold(baseName(ragePath), "ragemp_v.exe") { // подключение по серверу умеет только updater.exe
		if u := filepath.Join(filepath.Dir(ragePath), "updater.exe"); isFile(u) {
			ragePath = u
		}
	}
	rpPath := ""
	if isRPLauncher && p.Via != "gta5rp" && isFile(p.Target) {
		rpPath = p.Target
	} else if rp != nil && rp.Exists {
		rpPath = rp.Path
	}

	useOfficial := g.Server == serverOfficial || (isRPLauncher && !isServerAddr(g.Server))
	if !useOfficial && ragePath == "" && rpPath != "" {
		useOfficial = true // RAGE MP не нашёлся, но есть лаунчер GTA5RP — открываем его
	}
	if useOfficial {
		return Plan{Target: rpPath, Via: "gta5rp", Rage: true}
	}
	return Plan{Target: ragePath, Via: "rage", Rage: true, Direct: isServerAddr(g.Server)}
}

func targetReady(g *Game, ls map[string]*Launcher) (bool, string) {
	pl := launchPlan(g, ls)
	t, ph := pl.Target, pl.Via
	if t == "" {
		if l := ls[ph]; ph != "" && l != nil {
			return false, fmt.Sprintf("Укажите путь к лаунчеру «%s»", l.Name)
		}
		return false, "Укажите путь к игре"
	}
	if isURI(t) {
		return true, ""
	}
	if !isFile(t) {
		return false, "Файл игры не найден — проверьте путь"
	}
	return true, ""
}

type chainItem struct {
	L      *Launcher
	Forced bool
}

// planChain — какие лаунчеры и в каком порядке открыть перед игрой.
func planChain(g *Game, ls map[string]*Launcher) []chainItem {
	var ids []string
	if g.Online {
		ids = append(ids, g.Requires...)
	}
	gta := isGTA(g)
	if gta && !contains(ids, "socialclub") {
		ids = append(ids, "socialclub")
	}
	pl := launchPlan(g, ls)
	target, ph := pl.Target, pl.Via
	type entry struct {
		group, idx int
		item       chainItem
	}
	var list []entry
	seenPaths := map[string]bool{}
	for idx, id := range uniq(ids) {
		l := ls[id]
		if l == nil || id == ph { // лаунчер сам и есть игра (RAGE MP, Riot Client)
			continue
		}
		p := norm(l.Path)
		if p != "" && (seenPaths[p] || (target != "" && p == norm(target))) {
			continue
		}
		if p != "" {
			seenPaths[p] = true
		}
		grp, ok := groupOrder[l.Group]
		if !ok {
			grp = 3
		}
		list = append(list, entry{grp, idx, chainItem{l, gta && id == "socialclub"}})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].group != list[j].group {
			return list[i].group < list[j].group
		}
		return list[i].idx < list[j].idx
	})
	out := make([]chainItem, 0, len(list))
	for _, e := range list {
		out = append(out, e.item)
	}
	return out
}

// ------------------------------------------------------------------ задача запуска

type Starter interface {
	Start(path, args, cwd string, admin bool) error
	OpenURI(uri string) error
}

type Step struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Forced  bool   `json:"forced"`
	State   string `json:"state"`
	Message string `json:"message"`
}

type JobError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	LauncherID string `json:"launcher_id"`
}

type JobView struct {
	ID      string    `json:"id"`
	GameID  string    `json:"game_id"`
	State   string    `json:"state"`
	Steps   []Step    `json:"steps"`
	Error   *JobError `json:"error"`
	CloseUI bool      `json:"close_ui"`
}

type LaunchJob struct {
	ID          string
	GameID      string
	SkipMissing bool
	store       *Store
	probe       *ProcessProbe
	starter     Starter
	poll        time.Duration
	rage        func(addr string) error               // запись сервера для RAGE MP (в тестах подменяется)
	riotRetry   time.Duration                         // через сколько повторить команду Riot Client, если игры нет
	riotWait    time.Duration                         // сколько всего ждать процесс игры Riot
	riotAPI     func(product, patchline string) error // «Играть» через локальный API Riot Client
	riotAPIWait time.Duration                         // через сколько после команды нажимать «Играть» через API
	riotAPIStep time.Duration                         // как часто повторять, пока клиент не готов или идёт вход
	riotLogin   time.Duration                         // сколько ждать, если клиент открыт, но вход ещё не выполнен

	mu       sync.Mutex
	state    string
	steps    []Step
	err      *JobError
	closeUI  bool
	started  time.Time
	finished time.Time
	onDone   func(gameID string) // например, перепроверить обложку: игра могла обновиться
	cancel   chan struct{}
	once     sync.Once
}

func NewLaunchJob(st *Store, gameID string, skip bool, probe *ProcessProbe, starter Starter) *LaunchJob {
	return &LaunchJob{ID: newID("j"), GameID: gameID, SkipMissing: skip, store: st, probe: probe, starter: starter,
		poll: time.Second, state: "running", steps: []Step{}, cancel: make(chan struct{}), rage: rageConnect, started: time.Now(),
		riotRetry: 15 * time.Second, riotWait: 60 * time.Second,
		riotAPI: riotLaunchViaAPI, riotAPIWait: 4 * time.Second, riotAPIStep: 3 * time.Second, riotLogin: 3 * time.Minute}
}

// serverOfficial — вместо прямого подключения открывать официальный лаунчер GTA5RP.
const serverOfficial = "official"

// isServerAddr: «downtown.gta5rp.com:22005» или «1.2.3.4:22005».
func isServerAddr(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && s != "list" && s != serverOfficial && !strings.ContainsAny(s, " /\\")
}

// splitServer делит «хост:порт»; порт по умолчанию 22005.
func splitServer(s string) (string, string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ":"); i > 0 {
		return s[:i], s[i+1:]
	}
	return s, "22005"
}

// rpServers — серверы GTA5RP, к которым RAGE MP разрешает прямое подключение.
var rpServers = []pair{
	{"Blaine", "blaine.gta5rp.com:22005"},
	{"Chiliad", "chiliad.gta5rp.com:22005"},
	{"Downtown", "downtown.gta5rp.com:22005"},
	{"Eclipse", "eclipse.gta5rp.com:22005"},
	{"Mirror", "mirror.gta5rp.com:22005"},
	{"Murrieta", "murrieta.gta5rp.com:22005"},
	{"Redwood", "redwood.gta5rp.com:22005"},
	{"Rockford", "rockford.gta5rp.com:22005"},
}

func (j *LaunchJob) View() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := JobView{ID: j.ID, GameID: j.GameID, State: j.state, Steps: append([]Step{}, j.steps...), CloseUI: j.closeUI}
	if j.err != nil {
		e := *j.err
		v.Error = &e
	}
	return v
}

func (j *LaunchJob) State() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

func (j *LaunchJob) FinishedAt() time.Time {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.finished
}

func (j *LaunchJob) Cancel() { j.once.Do(func() { close(j.cancel) }) }

func (j *LaunchJob) set(i int, state, msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if state != "" {
		j.steps[i].State = state
	}
	if msg != "" {
		j.steps[i].Message = msg
	}
}

func (j *LaunchJob) sleep(d time.Duration) error {
	select {
	case <-j.cancel:
		return &LaunchError{Msg: "Запуск отменён", Code: "cancelled"}
	case <-time.After(d):
		return nil
	}
}

func (j *LaunchJob) Run() {
	err := j.safeRun()
	j.finish(err)
	j.record()
	if j.onDone != nil {
		j.onDone(j.GameID)
	}
}

// record заносит итог запуска в журнал «Последние действия».
func (j *LaunchJob) record() {
	v := j.View()
	e := HistoryEntry{Time: time.Now().Unix(), GameID: j.GameID, State: v.State}
	if g, ok := j.store.Game(j.GameID); ok {
		e.GameName = g.Name
	} else if n := len(v.Steps); n > 0 {
		e.GameName = v.Steps[n-1].Label
	}
	if e.GameName == "" {
		return // игры нет в библиотеке — записывать нечего
	}
	if n := len(v.Steps); n > 0 {
		e.Message = v.Steps[n-1].Message
		if v.State == "done" && v.Steps[n-1].State == "warn" {
			e.State = "warn"
		}
	}
	if v.Error != nil && v.Error.Message != "" {
		e.Message = v.Error.Message
	}
	if v.State == "done" {
		if f := j.FinishedAt(); !f.IsZero() && f.After(j.started) {
			e.Duration = f.Sub(j.started).Milliseconds()
		}
	}
	j.store.AddHistory(e)
}

func (j *LaunchJob) finish(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.finished = time.Now()
	if err == nil {
		j.state = "done"
		return
	}
	le, ok := err.(*LaunchError)
	if !ok {
		le = &LaunchError{Msg: "Неожиданная ошибка: " + err.Error(), Code: "error"}
	}
	if le.Code == "cancelled" {
		j.state = "cancelled"
	} else {
		j.state = "error"
	}
	j.err = &JobError{Code: le.Code, Message: le.Msg, LauncherID: le.LauncherID}
	for i := range j.steps {
		if j.steps[i].State == "running" || j.steps[i].State == "pending" {
			if le.Code == "cancelled" {
				j.steps[i].State = "cancelled"
			} else {
				j.steps[i].State = "error"
				if j.steps[i].Message == "" {
					j.steps[i].Message = le.Msg
				}
			}
			break
		}
	}
}

func (j *LaunchJob) safeRun() (err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Printf("Ошибка запуска: %v", r)
			err = fmt.Errorf("%v", r)
		}
	}()
	return j.run()
}

func (j *LaunchJob) run() error {
	g, ok := j.store.Game(j.GameID)
	if !ok {
		return &LaunchError{Msg: "Игра не найдена в библиотеке", Code: "no_game"}
	}
	ls := launchersByID(j.store)
	chain := planChain(&g, ls)
	timeout := j.store.SettingsCopy().LauncherTimeout
	if timeout <= 0 {
		timeout = 90
	}

	j.mu.Lock()
	for _, c := range chain {
		j.steps = append(j.steps, Step{ID: c.L.ID, Kind: "launcher", Label: c.L.Name, Forced: c.Forced, State: "pending"})
	}
	j.steps = append(j.steps, Step{ID: g.ID, Kind: "game", Label: g.Name, State: "pending"})
	gi := len(j.steps) - 1
	j.mu.Unlock()

	if ok, why := targetReady(&g, ls); !ok { // проверяем заранее, чтобы не открывать лаунчеры зря
		j.set(gi, "error", why)
		return &LaunchError{Msg: why, Code: "game_missing"}
	}

	for i, c := range chain {
		if err := j.launcherStep(i, c.L, time.Duration(timeout)*time.Second); err != nil {
			return err
		}
	}

	j.set(gi, "running", "Запускаем игру")
	pl := launchPlan(&g, ls)
	workdir := g.Workdir
	if pl.Via == "rage" || pl.Via == "gta5rp" {
		workdir = filepath.Dir(pl.Target) // updater.exe ищет файлы RAGE MP рядом с собой
	}
	doneMsg := "Игра запущена"
	if pl.Via == "rage" {
		// Как официальный лаунчер GTA5RP: адрес сервера в HKCU\Software\RAGE-MP, затем updater.exe.
		addr := ""
		if pl.Direct {
			addr = g.Server
			name := g.ServerName
			if name == "" {
				name = g.Server
			}
			j.set(gi, "", "Подключаемся к серверу "+name)
			doneMsg = "RAGE MP подключается к серверу " + name
		}
		if err := j.rage(addr); err != nil {
			logger.Printf("Не удалось записать сервер для RAGE MP: %v", err)
			if addr != "" {
				doneMsg = "RAGE MP открыт, но сервер записать не удалось — выберите его в списке"
			}
		}
	} else if pl.Via == "gta5rp" {
		j.set(gi, "", "Открываем лаунчер GTA5RP")
		doneMsg = "Лаунчер GTA5RP открыт — нажмите в нём «Играть»"
	} else if pl.Via == "battlenet" {
		doneMsg = "Battle.net получил команду запуска игры"
	}

	var riotWasUp bool
	if len(pl.Watch) > 0 {
		if l := ls["riot"]; l != nil {
			j.probe.Invalidate()
			riotWasUp = j.probe.IsRunning(l, nil)
		}
	}
	if err := j.startGame(pl, workdir, g.Admin); err != nil {
		return err
	}
	if len(pl.Watch) > 0 {
		if err := j.waitRiotGame(gi, pl, workdir, g.Admin, riotWasUp); err != nil {
			return err
		}
	} else {
		j.set(gi, "done", doneMsg)
	}
	j.store.MarkPlayed(g.ID)
	j.mu.Lock()
	j.closeUI = j.store.SettingsCopy().AfterLaunch == "close"
	j.mu.Unlock()
	return nil
}

func (j *LaunchJob) startGame(pl Plan, workdir string, admin bool) error {
	var err error
	if isURI(pl.Target) {
		err = j.starter.OpenURI(pl.Target)
	} else {
		err = j.starter.Start(pl.Target, pl.Args, workdir, admin)
	}
	if err == nil {
		return nil
	}
	if le, ok := err.(*LaunchError); ok {
		return le
	}
	return &LaunchError{Msg: fmt.Sprintf("Не удалось запустить игру: %v", err), Code: "start_failed"}
}

// waitRiotGame ждёт, пока после команды Riot Client появится процесс самой игры.
// Команда из ярлыка (--launch-product) иногда только открывает клиент на странице игры. Тогда Zero Launcher
// нажимает «Играть» сам — тем же запросом к локальному API клиента, что и кнопка. Пока клиент загружается
// или ждёт входа в аккаунт, запрос повторяется; запуск считается успешным, только когда появился процесс игры.
func (j *LaunchJob) waitRiotGame(gi int, pl Plan, workdir string, admin, riotWasUp bool) error {
	gameUp := func() bool {
		j.probe.Invalidate()
		return j.probe.IsRunning(&Launcher{Processes: pl.Watch}, nil)
	}
	product, patch := "", "live"
	if m := launchProductRe.FindStringSubmatch(pl.Args); m != nil {
		product = m[1]
	}
	if m := riotPatchRe.FindStringSubmatch(pl.Args); m != nil {
		patch = m[1]
	}
	start := time.Now()
	deadline := j.riotWait
	retried := false
	accepted := 0     // сколько раз клиент принял «Играть»
	var lastAPI error // последний ответ API (nil — ещё не спрашивали или принял)
	nextAPI := j.riotAPIWait
	for {
		if gameUp() {
			j.set(gi, "done", "Игра запущена")
			return nil
		}
		elapsed := time.Since(start)

		if j.riotAPI != nil && product != "" && elapsed >= nextAPI && accepted < 3 {
			err := j.riotAPI(product, patch)
			lastAPI = err
			switch {
			case err == nil:
				accepted++
				j.set(gi, "", "Riot Client получил команду «Играть» — ждём игру")
				nextAPI = elapsed + 20*time.Second // игре нужно время: VALORANT стартует вместе с Vanguard
				if d := elapsed + 45*time.Second; d > deadline {
					deadline = d
				}
			case errors.Is(err, errRiotNotReady):
				nextAPI = elapsed + j.riotAPIStep
			default: // клиент открыт, но игру не запускает — скорее всего, ещё не выполнен вход
				j.set(gi, "", "Войдите в Riot Client — игра запустится сразу после входа")
				nextAPI = elapsed + j.riotAPIStep
				if j.riotLogin > deadline {
					deadline = j.riotLogin
				}
			}
		}

		if !retried && accepted == 0 && elapsed >= j.riotRetry {
			retried = true
			if lastAPI == nil || errors.Is(lastAPI, errRiotNotReady) {
				j.set(gi, "", "Riot Client открылся — повторяем команду запуска игры")
			}
			if err := j.startGame(pl, workdir, admin); err != nil { // повтор — с теми же правами, что и первый запуск
				return err
			}
		}
		if elapsed >= deadline {
			var ae *riotAPIError
			msg := "Riot Client открыт, но игра пока не стартовала. Если клиент просит войти — войдите и нажмите «Запустить игру» ещё раз"
			switch {
			case accepted > 0:
				msg = "Riot Client принял команду «Играть», но игра пока не появилась — посмотрите окно клиента: возможно, идёт обновление или Vanguard просит перезагрузку"
			case errors.As(lastAPI, &ae):
				msg = fmt.Sprintf("Riot Client не запускает игру (ответ %d). Войдите в аккаунт или проверьте окно клиента и нажмите «Запустить игру» ещё раз", ae.status)
			case riotWasUp:
				msg = "Команда отправлена в Riot Client, но игра пока не стартовала — проверьте окно клиента"
			}
			j.set(gi, "warn", msg)
			return nil
		}
		if !retried && accepted == 0 && lastAPI == nil {
			j.set(gi, "", fmt.Sprintf("Riot Client запускает игру (до %d с)", int((deadline-elapsed).Seconds())))
		}
		if err := j.sleep(j.poll); err != nil {
			return err
		}
	}
}

func (j *LaunchJob) launcherStep(i int, l *Launcher, timeout time.Duration) error {
	j.set(i, "running", "Проверяем, запущен ли")
	if l.Path == "" || !l.Exists {
		why := "путь не указан"
		if l.Path != "" {
			why = "файл не найден"
		}
		if j.SkipMissing {
			j.set(i, "skipped", "Пропущен: "+why)
			return nil
		}
		j.set(i, "error", capitalize(why))
		return &LaunchError{Msg: fmt.Sprintf("%s: %s. Укажите путь в разделе «Лаунчеры».", l.Name, why),
			Code: "launcher_missing", LauncherID: l.ID}
	}

	j.probe.Invalidate()
	if j.probe.IsRunning(l, nil) {
		j.set(i, "done", "Уже запущен")
		return nil
	}

	j.set(i, "", "Открываем")
	if err := j.starter.Start(l.Path, l.Args, filepath.Dir(l.Path), false); err != nil {
		return &LaunchError{Msg: fmt.Sprintf("Не удалось открыть %s: %v", l.Name, err), Code: "start_failed", LauncherID: l.ID}
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := j.sleep(j.poll); err != nil {
			return err
		}
		j.probe.Invalidate()
		if j.probe.IsRunning(l, nil) {
			break
		}
		left := int(time.Until(deadline).Seconds())
		if left <= 0 {
			j.set(i, "warn", "Не дождались окна лаунчера, продолжаем")
			return nil
		}
		j.set(i, "", fmt.Sprintf("Ждём, пока откроется (ещё до %d с)", left))
	}
	for left := l.Delay; left > 0; left-- {
		j.set(i, "", fmt.Sprintf("Даём загрузиться: %d с", left))
		if err := j.sleep(time.Second); err != nil {
			return err
		}
	}
	j.set(i, "done", "Запущен")
	return nil
}

// titleWords: «league of legends» → «League Of Legends» (замена устаревшему strings.Title).
func titleWords(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = capitalize(w)
	}
	return strings.Join(words, " ")
}

func capitalize(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
