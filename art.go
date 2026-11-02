package main

// Обложки, фоны, логотипы и иконки игр подбираются автоматически и обновляются сами.
//
// Источники по приоритету:
//   - картинки, которые вы сами поставили игре в Steam (userdata/…/config/grid);
//   - кэш библиотеки Steam (appcache/librarycache) — Steam обновляет его вместе с игрой;
//   - каталог Epic Games Launcher (catcache.bin) — там адреса актуальных обложек;
//   - метаданные Riot Client (иконки VALORANT и League of Legends);
//   - иконка из .exe игры или лаунчера;
//   - CDN Steam и Epic, если в настройках разрешена загрузка из интернета.
//
// Каждая картинка копируется в папку art. Локальные файлы сверяются по размеру и времени изменения
// каждые несколько минут, картинки из интернета перепроверяются раз в сутки запросом If-None-Match.
// Когда источник меняется (Steam скачал новый арт, игра обновила .exe), меняется и картинка в интерфейсе.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	artPoll       = 3 * time.Minute // как часто сверять локальные источники
	artRecheck    = 24 * 3600       // как часто перепроверять картинки из интернета, секунд
	artRetryMiss  = 6 * 3600        // как часто снова пробовать адрес, которого не было
	maxArtSize    = 15 << 20
	artFetchLimit = 12 * time.Second
)

var artKinds = []string{"cover", "hero", "logo", "icon"}

// Подписи источников для интерфейса.
var artFromNames = map[string]string{
	"steam-custom": "ваша картинка в Steam",
	"steam":        "Steam",
	"steam-cdn":    "Steam",
	"epic":         "Epic Games",
	"riot":         "Riot Games",
	"exe":          "иконка программы",
	"store":        "Microsoft Store",
}

// ArtItem — картинка в кэше и то, откуда она взята.
type ArtItem struct {
	File    string `json:"file"`
	From    string `json:"from"`
	Src     string `json:"src"`
	FP      string `json:"fp,omitempty"` // размер и время изменения локального источника
	ETag    string `json:"etag,omitempty"`
	LastMod string `json:"last_modified,omitempty"`
	Checked int64  `json:"checked,omitempty"` // когда последний раз спрашивали адрес в интернете
	Ver     string `json:"ver"`               // хеш содержимого — попадает в адрес картинки
	Updated int64  `json:"updated"`
}

// artSchema растёт, когда меняется подбор картинок: тогда забываются прошлые «не нашлось»,
// и новые источники (ярлыки, Microsoft Store, папка игры) проверяются заново.
const artSchema = 2

type artIndex struct {
	Schema    int                            `json:"schema"`
	Games     map[string]map[string]*ArtItem `json:"games"`
	Launchers map[string]*ArtItem            `json:"launchers"`
	Misses    map[string]int64               `json:"misses"` // адреса, которых нет (404), и когда проверяли
	Updated   int64                          `json:"updated"`
}

type ArtStore struct {
	dir    string
	mu     sync.Mutex
	idx    artIndex
	rev    int64
	dirty  bool
	run    sync.Mutex // одна проверка за раз
	client *http.Client

	// Удалённые игры. Проверка, начатая до удаления, могла успеть прочитать игру
	// и не должна вернуть её картинки. Номер — порядковый номер удаления (forgets).
	gone    map[string]uint64
	forgets uint64
	now     func() time.Time

	epicMu   sync.Mutex
	epicFP   string
	epicList []epicCatItem

	exeCache map[string]exeCacheEntry
}

var artFileRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+\.(jpg|png|webp|gif)$`)

// Адреса, с которых разрешено скачивать картинки. Всё остальное отклоняется, в том числе после редиректа.
var artHosts = []string{".steamstatic.com", ".akamaihd.net", ".epicgames.com", ".unrealengine.com", ".epicgamescdn.com"}

var artURLAllowed = func(u *url.URL) bool {
	if u == nil || u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, s := range artHosts {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// CDN Steam: первый адрес — новый формат магазина, второй — старый, он ещё работает для большинства игр.
var steamCDN = []string{
	"https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/%s/%s",
	"https://cdn.cloudflare.steamstatic.com/steam/apps/%s/%s",
}

var steamCDNFiles = map[string][]string{
	"cover": {"library_600x900_2x.jpg", "library_600x900.jpg"},
	"hero":  {"library_hero.jpg"},
	"logo":  {"logo.png"},
}

var steamCacheFiles = map[string][]string{
	"cover": {"library_600x900.jpg", "library_600x900_2x.jpg", "library_capsule.jpg"},
	"wide":  {"header.jpg", "library_header.jpg"},
	"hero":  {"library_hero.jpg", "library_hero_2x.jpg"},
	"logo":  {"logo.png", "logo_2x.png"},
}

var steamGridSuffix = map[string]string{"cover": "p", "hero": "_hero", "logo": "_logo", "icon": "_icon"}

var epicArtTypes = map[string][]string{
	"cover": {"DieselGameBoxTall", "OfferImageTall", "DieselStoreFrontTall"},
	"hero":  {"DieselGameBox", "OfferImageWide", "DieselStoreFrontWide", "DieselGameBoxWide"},
	"logo":  {"DieselGameBoxLogo"},
}

const gtaSteamArtID = "271590" // GTA V: обложка для GTA 5 RP и GTA из других магазинов

var gtaVNameRe = regexp.MustCompile(`(?i)gta\s*(5|v)\b|gta5|gtav|grand theft auto v\b|гта\s*(5|v)\b|\brp\b.*\b(gta|гта)`)

// looksGTAV: GTA V из любого магазина или GTA 5 RP — им подходит обложка GTA V.
func looksGTAV(g *Game) bool {
	t := strings.ToLower(strings.TrimSpace(g.Target))
	return g.Platform == "rage" || gtaSources[g.SourceID] || t == "{rage}" || gtaExes[strings.ToLower(baseName(t))] ||
		gtaVNameRe.MatchString(g.Name)
}

func NewArtStore(base string) *ArtStore {
	a := &ArtStore{dir: filepath.Join(base, "art"), now: time.Now,
		client: &http.Client{Timeout: artFetchLimit, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 4 || !artURLAllowed(req.URL) {
				return errors.New("переадресация на неразрешённый адрес")
			}
			return nil
		}}}
	_ = os.MkdirAll(a.dir, 0o755)
	a.load()
	return a
}

func (a *ArtStore) load() {
	a.idx = artIndex{}
	if raw, err := os.ReadFile(filepath.Join(a.dir, "index.json")); err == nil {
		_ = json.Unmarshal(raw, &a.idx)
	}
	if a.idx.Games == nil {
		a.idx.Games = map[string]map[string]*ArtItem{}
	}
	if a.idx.Launchers == nil {
		a.idx.Launchers = map[string]*ArtItem{}
	}
	if a.idx.Misses == nil || a.idx.Schema != artSchema {
		a.idx.Misses = map[string]int64{}
		a.idx.Schema = artSchema
		a.dirty = true
	}
	clean := func(it *ArtItem) bool { return it != nil && artFileRe.MatchString(it.File) }
	for gid, set := range a.idx.Games {
		for k, it := range set {
			if !clean(it) {
				delete(set, k)
			}
		}
		if len(set) == 0 {
			delete(a.idx.Games, gid)
		}
	}
	for id, it := range a.idx.Launchers {
		if !clean(it) {
			delete(a.idx.Launchers, id)
		}
	}
}

func (a *ArtStore) saveLocked() {
	if !a.dirty {
		return
	}
	raw, err := json.MarshalIndent(a.idx, "", " ")
	if err != nil {
		return
	}
	tmp := filepath.Join(a.dir, "index.json.tmp")
	if os.WriteFile(tmp, raw, 0o644) == nil && os.Rename(tmp, filepath.Join(a.dir, "index.json")) == nil {
		a.dirty = false
	}
}

// Rev растёт при каждой новой картинке — по нему интерфейс понимает, что пора перерисоваться.
func (a *ArtStore) Rev() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rev
}

// Updated — когда последний раз появлялась новая картинка (unix).
func (a *ArtStore) Updated() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.idx.Updated
}

func (a *ArtStore) Item(gid, kind string) (ArtItem, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if it := a.idx.Games[gid][kind]; it != nil {
		return *it, true
	}
	return ArtItem{}, false
}

func (a *ArtStore) LauncherIcon(id string) (ArtItem, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if it := a.idx.Launchers[id]; it != nil {
		return *it, true
	}
	return ArtItem{}, false
}

// FilePath — путь к картинке в кэше; пустой, если имя файла подозрительное.
func (a *ArtStore) FilePath(it ArtItem) string {
	if !artFileRe.MatchString(it.File) {
		return ""
	}
	return filepath.Join(a.dir, it.File)
}

// Forget удаляет картинки игры, которой больше нет в библиотеке.
func (a *ArtStore) Forget(gid string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forgets++
	if a.gone == nil {
		a.gone = map[string]uint64{}
	}
	a.gone[gid] = a.forgets
	for _, it := range a.idx.Games[gid] {
		_ = os.Remove(filepath.Join(a.dir, it.File))
	}
	if _, ok := a.idx.Games[gid]; ok {
		delete(a.idx.Games, gid)
		a.dirty = true
		a.rev++
		a.saveLocked()
	}
}

// ------------------------------------------------------------------ источники

type artSource struct {
	from  string
	path  string // локальный файл
	url   string // адрес в интернете
	conv  string // "" — картинка как есть, "ico" — файл .ico, "exe" — иконка из .exe или .dll
	index int    // номер иконки в .exe/.dll (как в ярлыке «файл,номер»)
}

func (s artSource) key() string {
	if s.url != "" {
		return s.url
	}
	if s.index != 0 {
		return fmt.Sprintf("%s#%d", s.path, s.index)
	}
	return s.path
}

type artCtx struct {
	steamRoot string
	launchers map[string]*Launcher
	epic      []epicCatItem
	online    bool
	force     bool
	now       int64
	forgets   uint64 // сколько игр было удалено к началу проверки
}

var steamAppRe = regexp.MustCompile(`^steam://(?:rungameid|run)/(\d+)`)

func steamAppID(g *Game) string {
	if id, ok := strings.CutPrefix(g.SourceID, "steam:"); ok && isDigits(id) {
		return id
	}
	if m := steamAppRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(g.Target))); m != nil {
		return m[1]
	}
	return ""
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// epicIDs: из ссылки com.epicgames.launcher://apps/ns%3Aid%3Aapp или из source_id.
func epicIDs(g *Game) (catalogID, app string) {
	if id, ok := strings.CutPrefix(g.SourceID, "epic:"); ok {
		app = id
	}
	t := strings.TrimSpace(g.Target)
	if rest, ok := strings.CutPrefix(strings.ToLower(t), "com.epicgames.launcher://apps/"); ok {
		rest = t[len(t)-len(rest):]
		if i := strings.IndexAny(rest, "?#"); i >= 0 {
			rest = rest[:i]
		}
		if dec, err := url.PathUnescape(rest); err == nil {
			rest = dec
		}
		parts := strings.Split(rest, ":")
		if len(parts) == 3 {
			catalogID = parts[1]
			if app == "" {
				app = parts[2]
			}
		} else if app == "" {
			app = rest
		}
	}
	return catalogID, app
}

var riotPatchRe = regexp.MustCompile(`--launch-patchline=([A-Za-z0-9_]+)`)

func riotProduct(g *Game, ls map[string]*Launcher) (string, string) {
	pl := launchPlan(g, ls)
	m := launchProductRe.FindStringSubmatch(pl.Args)
	if m == nil {
		return "", ""
	}
	patch := "live"
	if p := riotPatchRe.FindStringSubmatch(pl.Args); p != nil {
		patch = p[1]
	}
	return m[1], patch
}

// newestFile — самый свежий из существующих файлов.
func newestFile(paths []string) string {
	best, bestT := "", time.Time{}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Size() == 0 {
			continue
		}
		if best == "" || st.ModTime().After(bestT) {
			best, bestT = p, st.ModTime()
		}
	}
	return best
}

// steamCacheFile ищет картинку в кэше библиотеки Steam: старый формат (578080_library_600x900.jpg),
// новый (578080/library_600x900.jpg) и с хешем (578080/<hash>/library_600x900.jpg). Берётся самый свежий.
func steamCacheFile(root, appid string, names []string) string {
	if root == "" || appid == "" {
		return ""
	}
	cache := filepath.Join(root, "appcache", "librarycache")
	want := map[string]bool{}
	var hits []string
	for _, n := range names {
		want[n] = true
		hits = append(hits, filepath.Join(cache, appid+"_"+n), filepath.Join(cache, appid, n))
	}
	sub := filepath.Join(cache, appid)
	if isDir(sub) {
		_ = filepath.WalkDir(sub, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && p != sub && strings.Count(strings.TrimPrefix(p, sub), string(filepath.Separator)) > 2 {
				return fs.SkipDir
			}
			if !d.IsDir() && want[strings.ToLower(d.Name())] {
				hits = append(hits, p)
			}
			return nil
		})
	}
	// при равных датах предпочтительнее порядок из names: первая подходящая картинка — основная
	for _, n := range names {
		var group []string
		for _, h := range hits {
			if strings.EqualFold(filepath.Base(h), n) || strings.EqualFold(filepath.Base(h), appid+"_"+n) {
				group = append(group, h)
			}
		}
		if p := newestFile(group); p != "" {
			return p
		}
	}
	return ""
}

// steamGridFile — картинка, которую пользователь сам поставил игре в Steam.
func steamGridFile(root, appid, kind string) string {
	suffix, ok := steamGridSuffix[kind]
	if root == "" || appid == "" || !ok {
		return ""
	}
	users, err := os.ReadDir(filepath.Join(root, "userdata"))
	if err != nil {
		return ""
	}
	var hits []string
	for _, u := range users {
		if !u.IsDir() {
			continue
		}
		for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp"} {
			hits = append(hits, filepath.Join(root, "userdata", u.Name(), "config", "grid", appid+suffix+ext))
		}
	}
	return newestFile(hits)
}

func steamSources(ctx *artCtx, appid, kind string) []artSource {
	var out []artSource
	if p := steamGridFile(ctx.steamRoot, appid, kind); p != "" {
		out = append(out, artSource{from: "steam-custom", path: p})
	}
	if names := steamCacheFiles[kind]; names != nil {
		if p := steamCacheFile(ctx.steamRoot, appid, names); p != "" {
			out = append(out, artSource{from: "steam", path: p})
		}
	}
	for _, f := range steamCDNFiles[kind] {
		for _, tpl := range steamCDN {
			out = append(out, artSource{from: "steam-cdn", url: fmt.Sprintf(tpl, appid, f)})
		}
	}
	// у старых игр нет вертикальной обложки — тогда показываем шапку магазина целиком, без обрезки
	if kind == "cover" {
		if p := steamCacheFile(ctx.steamRoot, appid, steamCacheFiles["wide"]); p != "" {
			out = append(out, artSource{from: "steam", path: p})
		}
		for _, tpl := range steamCDN {
			out = append(out, artSource{from: "steam-cdn", url: fmt.Sprintf(tpl, appid, "header.jpg")})
		}
	}
	return out
}

// ------------------------------------------------------------------ каталог Epic Games

type epicCatItem struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	Title     string `json:"title"`
	KeyImages []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"keyImages"`
	ReleaseInfo []struct {
		AppID string `json:"appId"`
	} `json:"releaseInfo"`
}

func epicCatalogPath() string {
	for _, p := range expand(`%ProgramData%\Epic\EpicGamesLauncher\Data\Catalog\catcache.bin`) {
		if isFile(p) {
			return p
		}
	}
	return ""
}

// epicCatalog читает catcache.bin (base64 от JSON) и запоминает, пока файл не изменится.
func (a *ArtStore) epicCatalog() []epicCatItem {
	p := epicCatalogPath()
	if p == "" {
		return nil
	}
	st, err := os.Stat(p)
	if err != nil || st.Size() > 256<<20 {
		return nil
	}
	fp := fmt.Sprintf("%s|%d|%d", p, st.Size(), st.ModTime().UnixNano())
	a.epicMu.Lock()
	defer a.epicMu.Unlock()
	if fp == a.epicFP {
		return a.epicList
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	body := strings.TrimSpace(strings.TrimPrefix(string(raw), bom))
	if !strings.HasPrefix(body, "[") {
		dec, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(body), ""))
		if err != nil {
			logger.Printf("Каталог Epic не прочитан: %v", err)
			return nil
		}
		body = string(dec)
	}
	var list []epicCatItem
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		logger.Printf("Каталог Epic не разобран: %v", err)
		return nil
	}
	a.epicFP, a.epicList = fp, list
	return list
}

func epicSources(ctx *artCtx, g *Game, kind string) []artSource {
	cid, app := epicIDs(g)
	if (cid == "" && app == "") || len(ctx.epic) == 0 || len(epicArtTypes[kind]) == 0 {
		return nil
	}
	var item *epicCatItem
	for i := range ctx.epic {
		it := &ctx.epic[i]
		if cid != "" && strings.EqualFold(it.ID, cid) {
			item = it
			break
		}
		for _, r := range it.ReleaseInfo {
			if app != "" && strings.EqualFold(r.AppID, app) {
				item = it
			}
		}
		if item != nil {
			break
		}
	}
	if item == nil {
		return nil
	}
	var out []artSource
	for _, t := range epicArtTypes[kind] {
		for _, k := range item.KeyImages {
			if k.Type == t && k.URL != "" {
				out = append(out, artSource{from: "epic", url: k.URL})
			}
		}
	}
	return out
}

// ------------------------------------------------------------------ кандидаты для игры

func (a *ArtStore) gameSources(ctx *artCtx, g *Game, kind string) []artSource {
	var out []artSource
	seen := map[string]bool{}
	add := func(list ...artSource) {
		for _, x := range list {
			k := strings.ToLower(x.key())
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, x)
		}
	}
	steamID := steamAppID(g)
	target, via := resolveTarget(g, ctx.launchers)
	dir := g.InstallDir
	if dir == "" && steamID != "" {
		dir = steamInstallDir(ctx.steamRoot, steamID)
	}

	if kind != "icon" {
		add(epicSources(ctx, g, kind)...)
		if steamID == "" && looksGTAV(g) {
			steamID = gtaSteamArtID
		}
		if steamID != "" {
			src := steamSources(ctx, steamID, kind)
			if len(out) > 0 { // у игры из Epic картинки Steam — только запасной вариант из интернета
				for _, s := range src {
					if s.url != "" {
						add(s)
					}
				}
			} else {
				add(src...)
			}
		}
		if kind == "hero" { // заставка приложения Microsoft Store / Xbox
			add(appxSources(target, "hero")...)
			if dir != "" {
				add(packageVisualSources(dir, "", "hero")...)
			}
		}
		return out
	}

	// иконка: сначала то, что выбрал сам пользователь или разработчик, потом файлы игры
	if steamID != "" {
		if p := steamGridFile(ctx.steamRoot, steamID, "icon"); p != "" {
			add(artSource{from: "steam-custom", path: p})
		}
	}
	if product, patch := riotProduct(g, ctx.launchers); product != "" {
		for _, p := range expand(fmt.Sprintf(`%%ProgramData%%\Riot Games\Metadata\%s.%s\%s.%s.ico`, product, patch, product, patch)) {
			add(artSource{from: "riot", path: p, conv: "ico"})
		}
	}
	add(iconSourcesForPath(g.IconSrc, 0)...)
	if via != "riot" && via != "battlenet" { // у {riot} и {battlenet} иконка клиента, а не игры
		add(iconSourcesForPath(target, 0)...)
	}
	if dir != "" {
		add(packageVisualSources(dir, "", "icon")...)
		if exe := a.mainExe(dir); exe != "" {
			add(artSource{from: "exe", path: exe, conv: "exe"})
		}
		add(folderIconSources(dir, baseName(filepath.Clean(dir)))...)
	}
	return out
}

// mainExe — главный .exe в папке игры (для игр, запускаемых ссылкой: Steam, Ubisoft, EA).
// Обход папки недешёвый, поэтому результат помнится полчаса.
func (a *ArtStore) mainExe(dir string) string {
	a.mu.Lock()
	if a.exeCache == nil {
		a.exeCache = map[string]exeCacheEntry{}
	}
	e, ok := a.exeCache[norm(dir)]
	a.mu.Unlock()
	now := a.now()
	if ok && now.Sub(e.at) < 30*time.Minute {
		return e.exe
	}
	exe := ""
	if isDir(dir) {
		exe = bestExe(dir)
	}
	a.mu.Lock()
	a.exeCache[norm(dir)] = exeCacheEntry{exe, now}
	a.mu.Unlock()
	return exe
}

type exeCacheEntry struct {
	exe string
	at  time.Time
}

// steamInstallDir — папка игры Steam по номеру (из appmanifest во всех библиотеках).
func steamInstallDir(root, appid string) string {
	if root == "" || appid == "" {
		return ""
	}
	for _, lib := range steamLibraries(root) {
		raw, err := os.ReadFile(filepath.Join(lib, "steamapps", "appmanifest_"+appid+".acf"))
		if err != nil {
			continue
		}
		if d := toStr(ci(ci(parseVDF(string(raw)), "AppState"), "installdir")); d != "" {
			if p := filepath.Join(lib, "steamapps", "common", d); isDir(p) {
				return p
			}
		}
	}
	return ""
}

// ------------------------------------------------------------------ обновление

// RefreshAll проверяет картинки всех игр и лаунчеров. Возвращает, сколько картинок поменялось.
func (a *ArtStore) RefreshAll(st *Store, force, online bool) int {
	a.run.Lock()
	defer a.run.Unlock()
	return a.refresh(st, "", force, online)
}

// TryRefreshAll — то же, но не ждёт, если проверка уже идёт (тогда ok=false).
func (a *ArtStore) TryRefreshAll(st *Store, force, online bool) (int, bool) {
	if !a.run.TryLock() {
		return 0, false
	}
	defer a.run.Unlock()
	return a.refresh(st, "", force, online), true
}

// RefreshGame проверяет одну игру (после добавления, правки или запуска).
func (a *ArtStore) RefreshGame(st *Store, gid string, force bool) int {
	a.run.Lock()
	defer a.run.Unlock()
	return a.refresh(st, gid, force, !st.SettingsCopy().NoOnlineArt)
}

func (a *ArtStore) refresh(st *Store, only string, force, online bool) int {
	a.mu.Lock()
	forgets := a.forgets // до снимка библиотеки: игру удаляют из библиотеки раньше, чем вызывают Forget
	a.mu.Unlock()
	cfg := st.Snapshot()
	ls := launchersByID(st)
	ctx := &artCtx{steamRoot: steamRoot(ls), launchers: ls, online: online && !cfg.Settings.NoOnlineArt,
		force: force, now: a.now().Unix(), forgets: forgets}
	ctx.epic = a.epicCatalog()
	changed := 0
	alive := map[string]bool{}
	for _, g := range cfg.Games {
		alive[g.ID] = true
		if only != "" && g.ID != only {
			continue
		}
		for _, kind := range artKinds {
			if a.refreshItem(ctx, "g", g.ID, kind, a.gameSources(ctx, g, kind)) {
				changed++
			}
		}
	}
	if only == "" {
		ids := make([]string, 0, len(ls))
		for id := range ls {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			l := ls[id]
			var src []artSource
			if l.Exists && strings.EqualFold(filepath.Ext(l.Path), ".exe") {
				src = append(src, artSource{from: "exe", path: l.Path, conv: "exe"})
			}
			if a.refreshItem(ctx, "l", id, "icon", src) {
				changed++
			}
		}
		a.prune(alive, ls)
	}
	a.mu.Lock()
	// Проверки идут по одной (a.run), а следующая начнётся уже после всех этих удалений.
	a.gone = nil
	a.saveLocked()
	a.mu.Unlock()
	return changed
}

func (a *ArtStore) current(scope, id, kind string) *ArtItem {
	a.mu.Lock()
	defer a.mu.Unlock()
	if scope == "l" {
		if it := a.idx.Launchers[id]; it != nil {
			c := *it
			return &c
		}
		return nil
	}
	if it := a.idx.Games[id][kind]; it != nil {
		c := *it
		return &c
	}
	return nil
}

func (a *ArtStore) fileOK(it *ArtItem) bool {
	return it != nil && artFileRe.MatchString(it.File) && isFile(filepath.Join(a.dir, it.File))
}

func fileFP(st os.FileInfo) string { return fmt.Sprintf("%d|%d", st.Size(), st.ModTime().UnixNano()) }

// refreshItem перебирает источники по приоритету и берёт первый доступный.
func (a *ArtStore) refreshItem(ctx *artCtx, scope, id, kind string, sources []artSource) bool {
	cur := a.current(scope, id, kind)
	for _, s := range sources {
		if s.path != "" {
			st, err := os.Stat(s.path)
			if err != nil || st.IsDir() || st.Size() == 0 {
				continue
			}
			fp := fileFP(st)
			if !ctx.force && cur != nil && cur.Src == s.key() && cur.FP == fp && a.fileOK(cur) {
				return false
			}
			miss := s.key() + "|" + fp // .exe без иконки не перечитываем, пока файл не изменится
			if !ctx.force && a.missed(miss) {
				continue
			}
			data, ext, err := loadArtFile(s)
			if err != nil {
				a.markMiss(miss, ctx.now)
				continue
			}
			return a.put(ctx, scope, id, kind, cur, ArtItem{From: s.from, Src: s.key(), FP: fp}, data, ext)
		}
		if s.url == "" || !ctx.online {
			continue
		}
		if cur != nil && cur.Src == s.url && a.fileOK(cur) && !ctx.force && ctx.now-cur.Checked < artRecheck {
			return false
		}
		if !ctx.force && a.missedRecently(s.url, ctx.now) {
			continue
		}
		var prev *ArtItem
		if cur != nil && cur.Src == s.url && a.fileOK(cur) && !ctx.force {
			prev = cur
		}
		data, ext, etag, lastMod, status, err := a.fetch(s.url, prev)
		switch {
		case err != nil:
			if prev != nil {
				return false // сети нет — оставляем то, что есть
			}
			continue
		case status == http.StatusNotModified && prev != nil:
			a.touch(scope, id, kind, ctx.now)
			return false
		case status != http.StatusOK:
			if status == http.StatusNotFound || status == http.StatusForbidden {
				a.markMiss(s.url, ctx.now)
			}
			continue
		}
		return a.put(ctx, scope, id, kind, cur, ArtItem{From: s.from, Src: s.url, ETag: etag, LastMod: lastMod, Checked: ctx.now}, data, ext)
	}
	// Ни один источник сейчас не доступен. Если картинка из одного из них — оставляем её
	// (файл временно пропал, нет интернета). Если источники другие (игру поменяли) — убираем.
	if cur == nil {
		return false
	}
	for _, s := range sources {
		if s.key() == cur.Src && a.fileOK(cur) {
			return false
		}
	}
	a.drop(scope, id, kind)
	return true
}

func loadArtFile(s artSource) ([]byte, string, error) {
	var data []byte
	var err error
	switch s.conv {
	case "exe":
		data, err = exeIconIndexPNG(s.path, s.index)
	case "ico":
		data, err = icoFilePNG(s.path)
	default:
		var st os.FileInfo
		if st, err = os.Stat(s.path); err == nil && st.Size() > maxArtSize {
			err = errors.New("картинка слишком большая")
		}
		if err == nil {
			data, err = os.ReadFile(s.path)
		}
	}
	if err != nil {
		return nil, "", err
	}
	ext, ok := sniffImage(data)
	if !ok {
		return nil, "", errors.New("это не картинка")
	}
	return data, ext, nil
}

func sniffImage(data []byte) (string, bool) {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/webp":
		return ".webp", true
	case "image/gif":
		return ".gif", true
	}
	return "", false
}

func artContentType(file string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".jpg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return ""
}

func (a *ArtStore) fetch(u string, prev *ArtItem) (data []byte, ext, etag, lastMod string, status int, err error) {
	pu, err := url.Parse(u)
	if err != nil || !artURLAllowed(pu) {
		return nil, "", "", "", 0, errors.New("адрес не разрешён")
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, "", "", "", 0, err
	}
	req.Header.Set("User-Agent", "ZeroLauncher/"+appVersion)
	req.Header.Set("Accept", "image/webp,image/png,image/jpeg,image/*;q=0.8")
	if prev != nil {
		if prev.ETag != "" {
			req.Header.Set("If-None-Match", prev.ETag)
		}
		if prev.LastMod != "" {
			req.Header.Set("If-Modified-Since", prev.LastMod)
		}
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, "", "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, "", "", "", resp.StatusCode, nil
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxArtSize+1))
	if err != nil {
		return nil, "", "", "", 0, err
	}
	if len(data) > maxArtSize {
		return nil, "", "", "", 0, errors.New("картинка слишком большая")
	}
	ext, ok := sniffImage(data)
	if !ok {
		return nil, "", "", "", http.StatusUnsupportedMediaType, nil
	}
	return data, ext, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), http.StatusOK, nil
}

func (a *ArtStore) missedRecently(u string, now int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.idx.Misses[u]
	return ok && now-t < artRetryMiss
}

func (a *ArtStore) missed(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.idx.Misses[key]
	return ok
}

func (a *ArtStore) markMiss(u string, now int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.idx.Misses[u] = now
	a.dirty = true
}

func (a *ArtStore) touch(scope, id, kind string, now int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var it *ArtItem
	if scope == "l" {
		it = a.idx.Launchers[id]
	} else {
		it = a.idx.Games[id][kind]
	}
	if it != nil {
		it.Checked = now
		a.dirty = true
	}
}

// put сохраняет картинку. Если содержимое не изменилось, обновляет только сведения об источнике.
func (a *ArtStore) put(ctx *artCtx, scope, id, kind string, cur *ArtItem, it ArtItem, data []byte, ext string) bool {
	sum := sha256.Sum256(data)
	it.Ver = hex.EncodeToString(sum[:])[:12]
	key := safeKey(scope + "_" + id)
	same := cur != nil && cur.Ver == it.Ver && a.fileOK(cur)
	if same {
		it.File, it.Updated = cur.File, cur.Updated
	} else {
		it.File = fmt.Sprintf("%s-%s-%s%s", key, kind, it.Ver, ext)
		it.Updated = a.now().Unix()
		tmp := filepath.Join(a.dir, it.File+".tmp")
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			logger.Printf("Не удалось сохранить картинку %s: %v", it.File, err)
			return false
		}
		if err := os.Rename(tmp, filepath.Join(a.dir, it.File)); err != nil {
			_ = os.Remove(tmp)
			return false
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if n, ok := a.gone[id]; ok && scope == "g" && n > ctx.forgets {
		// игру удалили, пока шла проверка
		_ = os.Remove(filepath.Join(a.dir, it.File))
		return false
	}
	if scope == "l" {
		a.idx.Launchers[id] = &it
	} else {
		if a.idx.Games[id] == nil {
			a.idx.Games[id] = map[string]*ArtItem{}
		}
		a.idx.Games[id][kind] = &it
	}
	delete(a.idx.Misses, it.Src)
	a.dirty = true
	if same {
		return false
	}
	if cur != nil && cur.File != it.File {
		_ = os.Remove(filepath.Join(a.dir, cur.File))
	}
	a.rev++
	a.idx.Updated = it.Updated
	return true
}

func (a *ArtStore) drop(scope, id, kind string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var it *ArtItem
	if scope == "l" {
		it = a.idx.Launchers[id]
		delete(a.idx.Launchers, id)
	} else {
		it = a.idx.Games[id][kind]
		delete(a.idx.Games[id], kind)
		if len(a.idx.Games[id]) == 0 {
			delete(a.idx.Games, id)
		}
	}
	if it != nil {
		_ = os.Remove(filepath.Join(a.dir, it.File))
	}
	a.dirty = true
	a.rev++
}

// prune убирает картинки удалённых игр и лаунчеров и забытые файлы в папке art.
func (a *ArtStore) prune(alive map[string]bool, ls map[string]*Launcher) {
	a.mu.Lock()
	defer a.mu.Unlock()
	keep := map[string]bool{"index.json": true}
	for gid, set := range a.idx.Games {
		if !alive[gid] {
			for _, it := range set {
				_ = os.Remove(filepath.Join(a.dir, it.File))
			}
			delete(a.idx.Games, gid)
			a.dirty = true
			a.rev++
			continue
		}
		for _, it := range set {
			keep[it.File] = true
		}
	}
	for id, it := range a.idx.Launchers {
		if ls[id] == nil {
			_ = os.Remove(filepath.Join(a.dir, it.File))
			delete(a.idx.Launchers, id)
			a.dirty = true
			continue
		}
		keep[it.File] = true
	}
	now := a.now().Unix()
	for u, t := range a.idx.Misses {
		if now-t > 7*86400 {
			delete(a.idx.Misses, u)
			a.dirty = true
		}
	}
	entries, _ := os.ReadDir(a.dir)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || keep[n] {
			continue
		}
		if artFileRe.MatchString(n) || strings.HasSuffix(n, ".tmp") {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Minute {
				_ = os.Remove(filepath.Join(a.dir, n))
			}
		}
	}
}

var unsafeKeyRe = regexp.MustCompile(`[^A-Za-z0-9_\-]`)

func safeKey(s string) string { return unsafeKeyRe.ReplaceAllString(s, "_") }

// ------------------------------------------------------------------ подписи для интерфейса

func artFromLabel(from string) string {
	if s, ok := artFromNames[from]; ok {
		return s
	}
	return from
}
