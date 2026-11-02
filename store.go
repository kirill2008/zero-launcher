package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Game struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Target      string   `json:"target"` // .exe/.lnk/.url, ссылка steam://... или {rage} — путь лаунчера
	Args        string   `json:"args"`
	Workdir     string   `json:"workdir"`
	Platform    string   `json:"platform"`
	Online      bool     `json:"online"` // перед игрой открыть лаунчеры из Requires
	Requires    []string `json:"requires"`
	Admin       bool     `json:"admin"`
	Favorite    bool     `json:"favorite"`
	Cover       string   `json:"cover"`        // своя обложка (копия в папке covers); пусто — подбирается автоматически
	CoverCustom bool     `json:"cover_custom"` // cover выбран пользователем, а не взят из кэша Steam старой версией
	Icon        string   `json:"icon"`
	IconSrc     string   `json:"icon_src"`
	InstallDir  string   `json:"install_dir"`
	SourceID    string   `json:"source_id"` // steam:578080 — чтобы не добавлять дубли
	Server      string   `json:"server"`    // GTA 5 RP: адрес сервера для автоподключения, «list» — список серверов
	ServerName  string   `json:"server_name"`
	Added       int64    `json:"added"`
	LastPlayed  int64    `json:"last_played"`
	PlayCount   int64    `json:"play_count"`
}

type pair struct{ K, V string }

var platforms = []pair{
	{"steam", "Steam"}, {"epic", "Epic Games"}, {"riot", "Riot Games"}, {"rockstar", "Rockstar"},
	{"rage", "RAGE MP"}, {"minecraft", "Minecraft"}, {"ea", "EA"}, {"ubisoft", "Ubisoft"},
	{"battlenet", "Battle.net"}, {"gog", "GOG"}, {"xbox", "Xbox"}, {"other", "Другое"},
}

// orderedMap сохраняет порядок ключей в JSON (для списка платформ в интерфейсе).
type orderedMap []pair

func (m orderedMap) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range m {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(p.K)
		v, _ := json.Marshal(p.V)
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func platformValid(p string) bool {
	for _, x := range platforms {
		if x.K == p {
			return true
		}
	}
	return false
}

// Какие лаунчеры по умолчанию нужны онлайн-игре платформы.
var platformRequires = map[string][]string{
	"steam": {"steam"}, "epic": {"epic"}, "riot": {"riot"}, "rockstar": {"socialclub"},
	"rage": {"steam", "socialclub"}, "ea": {"ea"}, "ubisoft": {"ubisoft"}, "battlenet": {"battlenet"},
	"gog": {}, "minecraft": {}, "xbox": {}, "other": {},
}

type LauncherOverride struct {
	Path           string   `json:"path"`
	Args           string   `json:"args,omitempty"`
	Delay          *int     `json:"delay,omitempty"`
	Auto           bool     `json:"auto"`
	ExtraProcesses []string `json:"extra_processes,omitempty"`
}

type CustomLauncher struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Args      string   `json:"args"`
	Delay     int      `json:"delay"`
	Processes []string `json:"processes"`
}

type Settings struct {
	AfterLaunch     string `json:"after_launch"` // none | close
	LauncherTimeout int    `json:"launcher_timeout"`
	Sort            string `json:"sort"` // popular | recent | name | added
	View            string `json:"view"` // grid | list
	// Интерфейс: хранится здесь, а не в браузере — порт окна меняется при каждом запуске.
	Accent        string `json:"accent"`         // amber | violet | cyan
	Compact       bool   `json:"compact"`        // компактные карточки
	ReduceMotion  bool   `json:"reduce_motion"`  // без анимаций
	ConfirmLaunch bool   `json:"confirm_launch"` // спрашивать перед запуском
	HideToasts    bool   `json:"hide_toasts"`    // не показывать всплывающие уведомления
	NoOnlineArt   bool   `json:"no_online_art"`  // не скачивать обложки из Steam и Epic
}

var sortModes = map[string]bool{"popular": true, "recent": true, "name": true, "added": true}

var accents = map[string]bool{"amber": true, "violet": true, "cyan": true}

// HistoryEntry — запись журнала «Последние действия»: чем закончился запуск игры.
type HistoryEntry struct {
	Time     int64  `json:"time"`
	GameID   string `json:"game_id"`
	GameName string `json:"game_name"`
	State    string `json:"state"` // done | warn | error | cancelled
	Message  string `json:"message"`
	Duration int64  `json:"duration_ms,omitempty"` // от нажатия «Запустить» до появления игры
}

const historyLimit = 50

type Config struct {
	Version         int                          `json:"version"`
	Games           []*Game                      `json:"games"`
	Launchers       map[string]*LauncherOverride `json:"launchers"`
	CustomLaunchers []*CustomLauncher            `json:"custom_launchers"`
	LibraryFolders  []string                     `json:"library_folders"`
	Settings        Settings                     `json:"settings"`
	History         []HistoryEntry               `json:"history"`
	LibraryChanged  int64                        `json:"library_changed"` // когда менялся список игр
}

type Store struct {
	mu        sync.Mutex
	Base      string
	CoversDir string
	IconsDir  string
	path      string
	cfg       *Config
}

func NewStore(base string) (*Store, error) {
	if base == "" {
		base = appDir()
	}
	s := &Store{Base: base, CoversDir: filepath.Join(base, "covers"), IconsDir: filepath.Join(base, "icons"),
		path: filepath.Join(base, "config.json")}
	for _, d := range []string{s.Base, s.CoversDir, s.IconsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	s.load()
	return s, nil
}

func (s *Store) load() {
	cfg := &Config{}
	if raw, err := os.ReadFile(s.path); err == nil {
		raw = []byte(strings.TrimPrefix(string(raw), bom))
		err := json.Unmarshal(raw, cfg)
		if err != nil {
			if fixed, ok := lenientJSON(raw); ok {
				cfg = &Config{}
				err = json.Unmarshal(fixed, cfg)
			}
		}
		if err != nil {
			broken := fmt.Sprintf("%s.broken-%d", s.path, time.Now().Unix())
			logger.Printf("config.json повреждён (%v), копия: %s", err, broken)
			_ = os.Rename(s.path, broken)
			cfg = &Config{}
		}
	}
	cfg.Version = 1
	if cfg.Launchers == nil {
		cfg.Launchers = map[string]*LauncherOverride{}
	}
	if cfg.LibraryFolders == nil {
		cfg.LibraryFolders = []string{}
	}
	if cfg.CustomLaunchers == nil {
		cfg.CustomLaunchers = []*CustomLauncher{}
	}
	if cfg.Settings.AfterLaunch != "close" {
		cfg.Settings.AfterLaunch = "none"
	}
	if cfg.Settings.LauncherTimeout <= 0 {
		cfg.Settings.LauncherTimeout = 90
	}
	if !sortModes[cfg.Settings.Sort] {
		cfg.Settings.Sort = "popular"
	}
	if cfg.Settings.View != "list" {
		cfg.Settings.View = "grid"
	}
	if !accents[cfg.Settings.Accent] {
		cfg.Settings.Accent = "amber"
	}
	if cfg.History == nil {
		cfg.History = []HistoryEntry{}
	}
	games := []*Game{}
	for _, g := range cfg.Games {
		if g != nil {
			normalizeGame(g)
			games = append(games, g)
		}
	}
	cfg.Games = games
	for _, c := range cfg.CustomLaunchers {
		if c.Processes == nil {
			c.Processes = []string{}
		}
	}
	s.cfg = cfg
}

func normalizeGame(g *Game) {
	if g.ID == "" {
		g.ID = newID("g")
	}
	for _, p := range []*string{&g.Name, &g.Target, &g.Args, &g.Workdir, &g.Cover, &g.Icon, &g.IconSrc, &g.InstallDir, &g.SourceID, &g.Server, &g.ServerName} {
		*p = strings.TrimSpace(*p)
	}
	g.Target = strings.Trim(g.Target, `"`)
	if !platformValid(g.Platform) {
		g.Platform = "other"
	}
	g.Requires = uniq(g.Requires)
	if g.Name == "" {
		g.Name = "Без названия"
	}
	if g.Server == "list" { // общий список RAGE MP отключён с июня 2026 — заходим через лаунчер GTA5RP
		g.Server, g.ServerName = serverOfficial, ""
	}
}

// lenientJSON чинит частые расхождения типов в старом config.json («5» вместо 5, «true» вместо true),
// чтобы библиотека не потерялась из-за одного поля.
func lenientJSON(raw []byte) ([]byte, bool) {
	var data any
	if json.Unmarshal(raw, &data) != nil {
		return nil, false
	}
	nums := map[string]bool{"delay": true, "launcher_timeout": true, "added": true, "last_played": true,
		"play_count": true, "version": true, "duration_ms": true, "library_changed": true}
	bools := map[string]bool{"online": true, "admin": true, "auto": true, "favorite": true, "cover_custom": true}
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if s, ok := val.(string); ok && nums[k] {
					if n, good := toInt(s); good {
						x[k] = n
					} else {
						delete(x, k)
					}
					continue
				}
				if _, ok := val.(string); ok && bools[k] {
					x[k] = toBool(val)
					continue
				}
				if val == nil && (nums[k] || bools[k]) {
					delete(x, k)
					continue
				}
				x[k] = walk(val)
			}
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		}
		return v
	}
	out, err := json.Marshal(walk(data))
	return out, err == nil
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// Snapshot — независимая копия всех данных.
func (s *Store) Snapshot() Config {
	s.mu.Lock()
	raw, _ := json.Marshal(s.cfg)
	s.mu.Unlock()
	var c Config
	_ = json.Unmarshal(raw, &c)
	if c.Launchers == nil {
		c.Launchers = map[string]*LauncherOverride{}
	}
	if c.LibraryFolders == nil {
		c.LibraryFolders = []string{}
	}
	return c
}

// Update меняет данные под блокировкой и сохраняет их, если fn не вернула ошибку.
func (s *Store) Update(fn func(c *Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(s.cfg); err != nil {
		return err
	}
	return s.saveLocked()
}

func (s *Store) SettingsCopy() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Settings
}

func (s *Store) findLocked(id string) (int, *Game) {
	for i, g := range s.cfg.Games {
		if g.ID == id {
			return i, g
		}
	}
	return -1, nil
}

func (s *Store) Game(id string) (Game, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, g := s.findLocked(id); g != nil {
		c := *g
		c.Requires = append([]string{}, g.Requires...)
		return c, true
	}
	return Game{}, false
}

func (s *Store) FindDuplicate(sourceID, target, args string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.cfg.Games {
		if sourceID != "" && g.SourceID == sourceID {
			return true
		}
		if target != "" && norm(g.Target) == norm(target) && g.Args == args {
			return true
		}
	}
	return false
}

var gameKeys = []string{"name", "target", "args", "workdir", "platform", "online", "requires", "admin",
	"cover", "install_dir", "source_id", "icon_src", "server", "server_name", "favorite"}

func applyGameFields(g *Game, m map[string]any) {
	for _, k := range gameKeys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch k {
		case "name":
			g.Name = toStr(v)
		case "target":
			g.Target = toStr(v)
		case "args":
			g.Args = toStr(v)
		case "workdir":
			g.Workdir = toStr(v)
		case "platform":
			g.Platform = toStr(v)
		case "online":
			g.Online = toBool(v)
		case "requires":
			g.Requires = toStrSlice(v)
		case "admin":
			g.Admin = toBool(v)
		case "cover":
			g.Cover = toStr(v)
		case "install_dir":
			g.InstallDir = toStr(v)
		case "source_id":
			g.SourceID = toStr(v)
		case "icon_src":
			g.IconSrc = toStr(v)
		case "server":
			g.Server = toStr(v)
		case "server_name":
			g.ServerName = toStr(v)
		case "favorite":
			g.Favorite = toBool(v)
		}
	}
}

func (s *Store) AddGame(fields map[string]any) Game {
	g := &Game{Requires: []string{}}
	applyGameFields(g, fields)
	g.ID = newID("g")
	g.Added = time.Now().Unix()
	normalizeGame(g)
	g.Cover = s.adoptCover(g.ID, g.Cover)
	g.CoverCustom = g.Cover != ""
	s.mu.Lock()
	s.cfg.Games = append(s.cfg.Games, g)
	s.cfg.LibraryChanged = time.Now().Unix()
	_ = s.saveLocked()
	c := *g
	s.mu.Unlock()
	return c
}

func (s *Store) UpdateGame(id string, fields map[string]any) (Game, bool) {
	s.mu.Lock()
	_, g := s.findLocked(id)
	if g == nil {
		s.mu.Unlock()
		return Game{}, false
	}
	upd := *g
	s.mu.Unlock()

	oldCover := upd.Cover
	applyGameFields(&upd, fields)
	normalizeGame(&upd)
	if upd.Cover != oldCover {
		upd.Cover = s.adoptCover(id, upd.Cover)
		upd.CoverCustom = upd.Cover != ""
		if upd.Cover == "" && oldCover != "" && filepath.Dir(oldCover) == s.CoversDir {
			_ = os.Remove(oldCover) // вернулись к автоматической обложке
		}
	}
	_, onlyFav := fields["favorite"]
	onlyFav = onlyFav && len(fields) == 1

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, g = s.findLocked(id); g == nil {
		return Game{}, false
	}
	upd.ID, upd.Added, upd.LastPlayed, upd.PlayCount = g.ID, g.Added, g.LastPlayed, g.PlayCount
	*g = upd
	if !onlyFav {
		s.cfg.LibraryChanged = time.Now().Unix()
	}
	_ = s.saveLocked()
	return upd, true
}

func (s *Store) DeleteGame(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, g := s.findLocked(id)
	if g == nil {
		return false
	}
	s.cfg.Games = append(s.cfg.Games[:i], s.cfg.Games[i+1:]...)
	s.cfg.LibraryChanged = time.Now().Unix()
	for _, p := range []string{g.Cover, g.Icon} {
		if p != "" {
			d := filepath.Dir(p)
			if d == s.CoversDir || d == s.IconsDir {
				_ = os.Remove(p)
			}
		}
	}
	_ = s.saveLocked()
	return true
}

func (s *Store) MarkPlayed(id string) {
	_ = s.Update(func(c *Config) error {
		for _, g := range c.Games {
			if g.ID == id {
				g.LastPlayed = time.Now().Unix()
				g.PlayCount++
			}
		}
		return nil
	})
}

// AddHistory записывает итог запуска; хранятся последние historyLimit записей, новые — в конце.
func (s *Store) AddHistory(e HistoryEntry) {
	_ = s.Update(func(c *Config) error {
		c.History = append(c.History, e)
		if len(c.History) > historyLimit {
			c.History = append([]HistoryEntry{}, c.History[len(c.History)-historyLimit:]...)
		}
		return nil
	})
}

var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".bmp": true}

// adoptCover копирует выбранную картинку в папку covers, чтобы обложка не пропала.
func (s *Store) adoptCover(id, src string) string {
	if src == "" {
		return ""
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return ""
	}
	if filepath.Dir(abs) == s.CoversDir {
		return abs
	}
	ext := strings.ToLower(filepath.Ext(abs))
	if !isFile(abs) || !imageExt[ext] {
		return ""
	}
	dst := filepath.Join(s.CoversDir, fmt.Sprintf("%s_%d%s", id, time.Now().UnixNano(), ext))
	if err := copyFile(abs, dst); err != nil {
		logger.Printf("Не удалось скопировать обложку %s: %v", abs, err)
		return ""
	}
	if entries, err := os.ReadDir(s.CoversDir); err == nil {
		for _, e := range entries {
			p := filepath.Join(s.CoversDir, e.Name())
			if strings.HasPrefix(e.Name(), id+"_") && p != dst {
				_ = os.Remove(p)
			}
		}
	}
	return dst
}

// copyFile копирует через временный файл: при сбое на месте dst не остаётся обрезанного файла.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
