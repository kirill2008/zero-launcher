package main

// Обложки и иконки: разбор .ico и .exe, автоматическое обновление из Steam, Epic, Riot и интернета.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIconDecoding(t *testing.T) {
	big := iconPNG(t, 256, color.RGBA{255, 160, 30, 255})
	small := dib32(32, 32, color.NRGBA{10, 20, 30, 255})

	// из .ico берётся самая крупная картинка, PNG отдаётся как есть
	got, err := icoPNG(icoFile([]testIcon{{32, 32, 32, small}, {256, 256, 32, big}}))
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("ico с PNG: err=%v, совпадает=%v", err, bytes.Equal(got, big))
	}

	// 32-битная картинка старого формата перекодируется в PNG с теми же цветами
	got, err = icoPNG(icoFile([]testIcon{{32, 32, 32, small}}))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, a := img.At(5, 5).RGBA(); r>>8 != 10 || g>>8 != 20 || b>>8 != 30 || a>>8 != 255 || img.Bounds().Dx() != 32 {
		t.Errorf("цвет 32 бит: %d %d %d %d, размер %v", r>>8, g>>8, b>>8, a>>8, img.Bounds())
	}

	// 8 бит с палитрой: левая половина прозрачна по маске AND
	got, err = icoPNG(icoFile([]testIcon{{16, 16, 8, dib8(16, 16, color.NRGBA{200, 50, 60, 255})}}))
	if err != nil {
		t.Fatal(err)
	}
	img, _ = png.Decode(bytes.NewReader(got))
	if _, _, _, a := img.At(2, 3).RGBA(); a != 0 {
		t.Errorf("маска прозрачности не применена: %d", a)
	}
	if r, _, _, a := img.At(12, 3).RGBA(); r>>8 != 200 || a>>8 != 255 {
		t.Errorf("цвет из палитры: %d %d", r>>8, a>>8)
	}

	// мусор не роняет программу
	for _, junk := range [][]byte{nil, []byte("abc"), icoFile([]testIcon{{16, 16, 32, []byte("short")}})} {
		if _, err := icoPNG(junk); err == nil {
			t.Errorf("мусор принят за иконку: %q", junk)
		}
	}
}

func TestExeIcon(t *testing.T) {
	dir := t.TempDir()
	big := iconPNG(t, 64, color.RGBA{30, 200, 120, 255})
	exe := filepath.Join(dir, "game.exe")
	writeBytes(t, exe, peWithIcons(t, []testIcon{{16, 16, 32, dib32(16, 16, color.NRGBA{1, 2, 3, 255})}, {64, 64, 32, big}}))
	got, err := exeIconPNG(exe)
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("иконка из .exe: err=%v совпадает=%v", err, bytes.Equal(got, big))
	}
	// только DIB — перекодируется
	exe2 := filepath.Join(dir, "old.exe")
	writeBytes(t, exe2, peWithIcons(t, []testIcon{{48, 48, 32, dib32(48, 48, color.NRGBA{90, 80, 70, 255})}}))
	got, err = exeIconPNG(exe2)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(got)); err != nil || cfg.Width != 48 {
		t.Errorf("DIB из .exe: %v %v", cfg, err)
	}
	// не PE и PE без ресурсов
	writeBytes(t, filepath.Join(dir, "fake.exe"), []byte("MZ"+strings.Repeat("\x00", 200)))
	if _, err := exeIconPNG(filepath.Join(dir, "fake.exe")); err == nil {
		t.Error("из поддельного .exe достали иконку")
	}
}

// artApp — приложение с тестовым хранилищем и HTTP-сервером.
func artApp(t *testing.T) (*fakeWin, *App, func(method, path string, body any) (int, map[string]any), string) {
	fw, st := newTestStore(t)
	p, s := newFake()
	app := NewApp(st, NewProbe(p.snapshot), s)
	if err := app.Listen(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Shutdown(); app.Wait() })
	base := fmt.Sprintf("http://127.0.0.1:%d", app.Port)
	call := func(method, path string, body any) (int, map[string]any) {
		var r io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			r = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, base+path, r)
		req.Header.Set("X-Zero-Token", app.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	return fw, app, call, base
}

func gameState(t *testing.T, call func(string, string, any) (int, map[string]any), id string) map[string]any {
	_, st := call("GET", "/api/state", nil)
	for _, g := range st["games"].([]any) {
		if m := g.(map[string]any); m["id"] == id {
			return m
		}
	}
	t.Fatalf("игры %s нет в состоянии", id)
	return nil
}

func fetchImage(t *testing.T, base, u, token string) []byte {
	resp, err := http.Get(base + u + "&t=" + token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: %d", u, resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	return raw
}

func TestArtSteamAutoUpdate(t *testing.T) {
	fw, app, call, base := artApp(t)
	steam := fw.root + "/PF86/Steam"
	cache := steam + "/appcache/librarycache/578080"
	first := jpegBytes(t, gradientImage(60, 90, color.RGBA{200, 40, 40, 255}, color.RGBA{40, 40, 200, 255}))
	writeBytes(t, cache+"/abc123/library_600x900.jpg", first)
	writeBytes(t, cache+"/library_hero.jpg", jpegBytes(t, gradientImage(192, 62, color.RGBA{10, 10, 10, 255}, color.RGBA{250, 200, 0, 255})))
	writeBytes(t, cache+"/logo.png", iconPNG(t, 40, color.RGBA{255, 255, 255, 255}))

	g := app.Store.AddGame(map[string]any{"name": "PUBG: BATTLEGROUNDS", "target": "steam://rungameid/578080", "platform": "steam",
		"source_id": "steam:578080"})
	if n := app.Art.RefreshAll(app.Store, false, false); n < 3 {
		t.Fatalf("обновлено картинок: %d", n)
	}
	gs := gameState(t, call, g.ID)
	for _, k := range []string{"cover_url", "hero_url", "logo_url"} {
		if !strings.HasPrefix(gs[k].(string), "/img/") {
			t.Fatalf("%s: %v", k, gs[k])
		}
	}
	if gs["art_from"] != "steam" {
		t.Errorf("источник: %v", gs["art_from"])
	}
	if got := fetchImage(t, base, gs["cover_url"].(string), app.Token); !bytes.Equal(got, first) {
		t.Fatal("отдана не та обложка")
	}
	v1 := gs["cover_url"].(string)

	// без изменений — ничего не перезаписывается
	if n := app.Art.RefreshAll(app.Store, false, false); n != 0 {
		t.Errorf("повторная проверка что-то поменяла: %d", n)
	}

	// Steam обновил арт: новый файл в другой папке с хешем — обложка меняется сама
	second := jpegBytes(t, gradientImage(60, 90, color.RGBA{20, 200, 40, 255}, color.RGBA{240, 240, 20, 255}))
	newer := cache + "/def456/library_600x900.jpg"
	writeBytes(t, newer, second)
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(newer, future, future)
	rev := app.Art.Rev()
	if n := app.Art.RefreshAll(app.Store, false, false); n != 1 {
		t.Fatalf("после обновления Steam изменилось картинок: %d", n)
	}
	if app.Art.Rev() == rev {
		t.Error("номер версии картинок не вырос")
	}
	gs = gameState(t, call, g.ID)
	if gs["cover_url"] == v1 {
		t.Fatal("адрес обложки не поменялся")
	}
	if got := fetchImage(t, base, gs["cover_url"].(string), app.Token); !bytes.Equal(got, second) {
		t.Fatal("после обновления отдана старая обложка")
	}
	files, _ := filepath.Glob(filepath.Join(app.Store.Base, "art", "*-cover-*"))
	if len(files) != 1 {
		t.Errorf("старые обложки не удалены: %v", files)
	}

	// своя картинка в Steam важнее кэша
	custom := iconPNG(t, 30, color.RGBA{1, 2, 250, 255})
	grid := steam + "/userdata/12345/config/grid/578080p.png"
	writeBytes(t, grid, custom)
	app.Art.RefreshAll(app.Store, false, false)
	gs = gameState(t, call, g.ID)
	if gs["art_from"] != "steam-custom" || !bytes.Equal(fetchImage(t, base, gs["cover_url"].(string), app.Token), custom) {
		t.Errorf("картинка из Steam не подхвачена: %v", gs["art_from"])
	}
	// убрали её — вернулась обложка из кэша
	_ = os.Remove(grid)
	app.Art.RefreshAll(app.Store, false, false)
	gs = gameState(t, call, g.ID)
	if gs["art_from"] != "steam" || !bytes.Equal(fetchImage(t, base, gs["cover_url"].(string), app.Token), second) {
		t.Errorf("после удаления своей картинки: %v", gs["art_from"])
	}

	// своя обложка в Zero Launcher перекрывает автоматическую, «пустая» — возвращает автоматическую
	mine := filepath.Join(fw.root, "my-cover.png")
	writeBytes(t, mine, iconPNG(t, 20, color.RGBA{9, 9, 9, 255}))
	if code, _ := call("PUT", "/api/games/"+g.ID, map[string]any{"cover": mine}); code != 200 {
		t.Fatal(code)
	}
	gs = gameState(t, call, g.ID)
	if gs["art_from"] != "custom" || gs["cover_custom"] != true {
		t.Errorf("своя обложка: %v %v", gs["art_from"], gs["cover_custom"])
	}
	call("PUT", "/api/games/"+g.ID, map[string]any{"cover": ""})
	gs = gameState(t, call, g.ID)
	if gs["art_from"] != "steam" || gs["cover_custom"] == true {
		t.Errorf("возврат к автоматической: %v", gs["art_from"])
	}

	// игру удалили — картинки удалены, в том числе те, что дописала проверка, начатая до удаления
	call("DELETE", "/api/games/"+g.ID, nil)
	var left []string
	for i := 0; i < 100; i++ { // фоновая проверка могла ещё дописывать файл — она удалит его сама
		if left, _ = filepath.Glob(filepath.Join(app.Store.Base, "art", "*.jpg")); len(left) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(left) != 0 {
		t.Errorf("картинки удалённой игры остались: %v", left)
	}
}

// Проверка, начатая до удаления игры, не возвращает её картинки.
func TestArtForgetDuringRefresh(t *testing.T) {
	dir := t.TempDir()
	a := NewArtStore(dir)
	ctx := &artCtx{now: time.Now().Unix()}
	png := iconPNG(t, 16, color.RGBA{200, 10, 10, 255})

	a.mu.Lock()
	ctx.forgets = a.forgets // проверка началась
	a.mu.Unlock()
	a.Forget("g_1") // игру удалили, пока проверка читала файлы
	if a.put(ctx, "g", "g_1", "icon", nil, ArtItem{From: "exe"}, png, ".png") {
		t.Error("картинка удалённой игры сохранена")
	}
	if _, ok := a.Item("g_1", "icon"); ok {
		t.Error("удалённая игра снова в индексе")
	}
	if files, _ := filepath.Glob(filepath.Join(a.dir, "*.png")); len(files) != 0 {
		t.Errorf("файл остался: %v", files)
	}

	// новая проверка начинается после удаления и сохраняет картинки как обычно
	a.mu.Lock()
	ctx2 := &artCtx{now: time.Now().Unix(), forgets: a.forgets}
	a.mu.Unlock()
	if !a.put(ctx2, "g", "g_1", "icon", nil, ArtItem{From: "exe"}, png, ".png") {
		t.Error("после удаления новая проверка не сохранила картинку")
	}
}

// cdnServer — подставной CDN: отдаёт картинки с ETag и считает запросы.
type cdnServer struct {
	mu    sync.Mutex
	files map[string][]byte
	etags map[string]string
	hits  map[string]int
	cond  int
}

func (c *cdnServer) set(path string, data []byte, etag string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[path], c.etags[path] = data, etag
}

func (c *cdnServer) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[path]
}

func (c *cdnServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits[r.URL.Path]++
	data, ok := c.files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		c.cond++
		if inm == c.etags[r.URL.Path] {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("ETag", c.etags[r.URL.Path])
	_, _ = w.Write(data)
}

func allowTestCDN(t *testing.T, srv *httptest.Server) {
	old, oldCDN := artURLAllowed, steamCDN
	host := strings.TrimPrefix(srv.URL, "http://")
	artURLAllowed = func(u *url.URL) bool { return u.Host == host }
	steamCDN = []string{srv.URL + "/steam/apps/%s/%s"}
	t.Cleanup(func() { artURLAllowed, steamCDN = old, oldCDN })
}

func TestArtOnlineRecheck(t *testing.T) {
	cdn := &cdnServer{files: map[string][]byte{}, etags: map[string]string{}, hits: map[string]int{}}
	srv := httptest.NewServer(cdn)
	defer srv.Close()
	allowTestCDN(t, srv)

	_, app, call, base := artApp(t)
	clock := time.Now()
	app.Art.now = func() time.Time { return clock }
	v1 := jpegBytes(t, gradientImage(30, 45, color.RGBA{1, 1, 100, 255}, color.RGBA{100, 1, 1, 255}))
	cdn.set("/steam/apps/730/library_600x900.jpg", v1, `"a1"`)
	g := app.Store.AddGame(map[string]any{"name": "Counter-Strike 2", "target": "steam://rungameid/730", "platform": "steam"})

	// без разрешения на интернет ничего не скачивается
	app.Art.RefreshAll(app.Store, false, false)
	if gameState(t, call, g.ID)["cover_url"] != "" || cdn.count("/steam/apps/730/library_600x900.jpg") != 0 {
		t.Fatal("скачано без разрешения")
	}

	app.Art.RefreshAll(app.Store, false, true)
	gs := gameState(t, call, g.ID)
	if gs["art_from"] != "steam-cdn" || !bytes.Equal(fetchImage(t, base, gs["cover_url"].(string), app.Token), v1) {
		t.Fatalf("обложка из CDN: %v", gs["art_from"])
	}
	if cdn.count("/steam/apps/730/library_600x900_2x.jpg") != 1 {
		t.Error("сначала должна запрашиваться картинка высокого разрешения")
	}

	// в течение суток повторных запросов нет, адреса с 404 не дёргаются
	app.Art.RefreshAll(app.Store, false, true)
	if n := cdn.count("/steam/apps/730/library_600x900.jpg"); n != 1 {
		t.Errorf("лишние запросы: %d", n)
	}
	if n := cdn.count("/steam/apps/730/library_600x900_2x.jpg"); n != 1 {
		t.Errorf("адрес с 404 запрошен снова: %d", n)
	}

	// через сутки — условный запрос; 304 ничего не меняет
	clock = clock.Add(25 * time.Hour)
	if n := app.Art.RefreshAll(app.Store, false, true); n != 0 {
		t.Errorf("после 304 изменилось: %d", n)
	}
	if cdn.cond == 0 {
		t.Error("не было запроса с If-None-Match")
	}

	// картинку на CDN заменили — через сутки она подтянется
	v2 := jpegBytes(t, gradientImage(30, 45, color.RGBA{1, 200, 1, 255}, color.RGBA{200, 200, 1, 255}))
	cdn.set("/steam/apps/730/library_600x900.jpg", v2, `"b2"`)
	clock = clock.Add(25 * time.Hour)
	if n := app.Art.RefreshAll(app.Store, false, true); n == 0 {
		t.Fatal("новая обложка с CDN не подтянулась")
	}
	gs = gameState(t, call, g.ID)
	if !bytes.Equal(fetchImage(t, base, gs["cover_url"].(string), app.Token), v2) {
		t.Fatal("отдана старая обложка")
	}

	// «Обновить обложку» в меню игры перепроверяет сразу
	v3 := jpegBytes(t, gradientImage(30, 45, color.RGBA{250, 250, 250, 255}, color.RGBA{0, 0, 0, 255}))
	cdn.set("/steam/apps/730/library_600x900.jpg", v3, `"c3"`)
	if code, res := call("POST", "/api/games/"+g.ID+"/art", nil); code != 200 || res["changed"].(float64) < 1 {
		t.Fatalf("обновить обложку: %d %v", code, res)
	}

	// чужой адрес и обычный http не разрешены
	artURLAllowed = func(u *url.URL) bool {
		return u.Scheme == "https" && strings.HasSuffix(u.Hostname(), ".steamstatic.com")
	}
	for _, u := range []string{"http://cdn.cloudflare.steamstatic.com/x.jpg", "https://evil.example.com/x.jpg", "file:///etc/passwd"} {
		if _, _, _, _, _, err := app.Art.fetch(u, nil); err == nil {
			t.Errorf("адрес разрешён: %s", u)
		}
	}
}

func TestArtDefaultAllowlist(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/1/library_600x900.jpg": true,
		"https://cdn1.epicgames.com/offer/fn/x.jpg":                                                true,
		"http://cdn.cloudflare.steamstatic.com/a.jpg":                                              false,
		"https://steamstatic.com.evil.org/a.jpg":                                                   false,
		"https://example.com/a.jpg":                                                                false,
		"file:///C:/Windows/win.ini":                                                               false,
	} {
		u, _ := url.Parse(raw)
		if realArtURLAllowed(u) != want {
			t.Errorf("%s: ожидалось %v", raw, want)
		}
	}
}

func TestArtEpicRiotLaunchers(t *testing.T) {
	cdn := &cdnServer{files: map[string][]byte{}, etags: map[string]string{}, hits: map[string]int{}}
	srv := httptest.NewServer(cdn)
	defer srv.Close()
	allowTestCDN(t, srv)

	fw, app, call, base := artApp(t)
	tall := jpegBytes(t, gradientImage(40, 60, color.RGBA{90, 20, 160, 255}, color.RGBA{20, 160, 200, 255}))
	wide := jpegBytes(t, gradientImage(160, 90, color.RGBA{20, 20, 60, 255}, color.RGBA{200, 90, 250, 255}))
	cdn.set("/fn/tall.jpg", tall, `"t"`)
	cdn.set("/fn/wide.jpg", wide, `"w"`)
	catalog := []map[string]any{
		{"id": "other", "namespace": "x", "title": "Other", "keyImages": []map[string]string{{"type": "DieselGameBoxTall", "url": srv.URL + "/nope.jpg"}}},
		{"id": "4fe75bbc5a674f4f9b356b5c90567da5", "namespace": "fn", "title": "Fortnite",
			"keyImages": []map[string]string{
				{"type": "DieselGameBox", "url": srv.URL + "/fn/wide.jpg"},
				{"type": "DieselGameBoxTall", "url": srv.URL + "/fn/tall.jpg"}},
			"releaseInfo": []map[string]string{{"appId": "Fortnite"}}},
	}
	raw, _ := json.Marshal(catalog)
	writeBytes(t, fw.root+"/ProgramData/Epic/EpicGamesLauncher/Data/Catalog/catcache.bin",
		[]byte(base64.StdEncoding.EncodeToString(raw)))

	fn := app.Store.AddGame(map[string]any{"name": "Fortnite", "platform": "epic",
		"target": "com.epicgames.launcher://apps/fn%3A4fe75bbc5a674f4f9b356b5c90567da5%3AFortnite?action=launch&silent=true"})
	// Riot: иконка из метаданных клиента
	ico := iconPNG(t, 128, color.RGBA{250, 70, 80, 255})
	writeBytes(t, fw.root+"/ProgramData/Riot Games/Metadata/valorant.live/valorant.live.ico",
		icoFile([]testIcon{{128, 128, 32, ico}}))
	val := app.Store.AddGame(map[string]any{"name": "VALORANT", "platform": "riot", "target": "{riot}",
		"args": "--launch-product=valorant --launch-patchline=live"})
	// игра по пути к .exe: иконка из самой программы
	exeIcon := iconPNG(t, 96, color.RGBA{60, 220, 90, 255})
	exe := fw.root + "/D/Games/Block/block.exe"
	writeBytes(t, exe, peWithIcons(t, []testIcon{{96, 96, 32, exeIcon}}))
	blk := app.Store.AddGame(map[string]any{"name": "Block Game", "target": exe})
	// лаунчер Steam с иконкой
	steamIcon := iconPNG(t, 48, color.RGBA{30, 60, 120, 255})
	writeBytes(t, fw.root+"/PF86/Steam/steam.exe", peWithIcons(t, []testIcon{{48, 48, 32, steamIcon}}))

	app.Art.RefreshAll(app.Store, false, true)

	gs := gameState(t, call, fn.ID)
	if gs["art_from"] != "epic" || !bytes.Equal(fetchImage(t, base, gs["cover_url"].(string), app.Token), tall) ||
		!bytes.Equal(fetchImage(t, base, gs["hero_url"].(string), app.Token), wide) {
		t.Errorf("Epic: %v", gs)
	}
	if cdn.count("/nope.jpg") != 0 {
		t.Error("взята картинка чужой игры из каталога")
	}
	gs = gameState(t, call, val.ID)
	if !bytes.Equal(fetchImage(t, base, gs["icon_url"].(string), app.Token), ico) {
		t.Error("иконка VALORANT из метаданных Riot")
	}
	gs = gameState(t, call, blk.ID)
	if !bytes.Equal(fetchImage(t, base, gs["icon_url"].(string), app.Token), exeIcon) {
		t.Error("иконка из .exe игры")
	}
	_, st := call("GET", "/api/state", nil)
	found := false
	for _, l := range st["launchers"].([]any) {
		m := l.(map[string]any)
		if m["id"] == "steam" && strings.HasPrefix(fmt.Sprint(m["icon_url"]), "/img/launcher/steam?v=") {
			found = bytes.Equal(fetchImage(t, base, m["icon_url"].(string), app.Token), steamIcon)
		}
	}
	if !found {
		t.Error("иконка лаунчера Steam")
	}

	// игра обновилась — новая иконка в .exe появляется сама
	exeIcon2 := iconPNG(t, 96, color.RGBA{220, 220, 30, 255})
	writeBytes(t, exe, peWithIcons(t, []testIcon{{96, 96, 32, exeIcon2}}))
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(exe, future, future)
	app.Art.RefreshAll(app.Store, false, true)
	if !bytes.Equal(fetchImage(t, base, gameState(t, call, blk.ID)["icon_url"].(string), app.Token), exeIcon2) {
		t.Error("после обновления игры иконка не поменялась")
	}

	// игру перенастроили на другой файл без иконки — старая иконка не остаётся
	call("PUT", "/api/games/"+blk.ID, map[string]any{"target": "https://example.com/play"})
	app.Art.RefreshGame(app.Store, blk.ID, false)
	if u := gameState(t, call, blk.ID)["icon_url"]; u != "" {
		t.Errorf("иконка от прошлого файла осталась: %v", u)
	}

	// путь к картинке не выходит за папку art
	for _, p := range []string{"/img/cover/..%2Fconfig", "/img/launcher/../../x", "/img/other/" + fn.ID} {
		resp, err := http.Get(base + p + "?t=" + app.Token)
		if err == nil {
			if resp.StatusCode == 200 {
				t.Errorf("%s отдан", p)
			}
			resp.Body.Close()
		}
	}
}

func TestLegacyCoverMigration(t *testing.T) {
	fw, st := newTestStore(t)
	cache := fw.root + "/PF86/Steam/appcache/librarycache/578080/library_600x900.jpg"
	steamArt := jpegBytes(t, gradientImage(20, 30, color.RGBA{5, 5, 5, 255}, color.RGBA{250, 5, 5, 255}))
	writeBytes(t, cache, steamArt)
	// так обложки сохраняла версия 1.x: копия файла из кэша Steam, флага cover_custom нет
	auto := st.AddGame(map[string]any{"name": "PUBG", "target": "steam://rungameid/578080", "source_id": "steam:578080", "cover": cache})
	own := filepath.Join(fw.root, "mine.png")
	writeBytes(t, own, iconPNG(t, 10, color.RGBA{1, 1, 1, 255}))
	mine := st.AddGame(map[string]any{"name": "Hollow", "target": fw.gamesFolder + "/Hollow Knight/hollow_knight.exe", "cover": own})
	_ = st.Update(func(c *Config) error {
		for _, g := range c.Games {
			g.CoverCustom = false
		}
		return nil
	})
	adopted, _ := st.Game(auto.ID)

	NewApp(st, nil, nil)
	a, _ := st.Game(auto.ID)
	m, _ := st.Game(mine.ID)
	if a.Cover != "" || a.CoverCustom {
		t.Errorf("копия обложки Steam не убрана: %+v", a)
	}
	if isFile(adopted.Cover) {
		t.Error("файл старой копии остался")
	}
	if m.Cover == "" || !m.CoverCustom {
		t.Errorf("своя обложка потерялась: %+v", m)
	}
}

func TestFavoritesViewCheck(t *testing.T) {
	_, app, call, _ := artApp(t)
	g := app.Store.AddGame(map[string]any{"name": "PUBG", "target": "steam://rungameid/578080", "platform": "steam",
		"online": true, "requires": []any{"steam"}})
	bad := app.Store.AddGame(map[string]any{"name": "Fortnite", "target": "com.epicgames.launcher://apps/Fortnite",
		"platform": "epic", "online": true, "requires": []any{"epic"}})
	before := app.Store.Snapshot().LibraryChanged

	if code, _ := call("PUT", "/api/games/"+g.ID, map[string]any{"favorite": true}); code != 200 {
		t.Fatal(code)
	}
	if gs := gameState(t, call, g.ID); gs["favorite"] != true {
		t.Error("избранное не сохранилось")
	}
	if app.Store.Snapshot().LibraryChanged != before {
		t.Error("звёздочка не должна считаться изменением библиотеки")
	}
	if st2, _ := NewStore(""); func() bool { x, _ := st2.Game(g.ID); return !x.Favorite }() {
		t.Error("избранное не записано на диск")
	}

	code, res := call("PUT", "/api/settings", map[string]any{"view": "list", "sort": "popular", "no_online_art": true})
	if code != 200 || res["settings"].(map[string]any)["view"] != "list" || !app.Store.SettingsCopy().NoOnlineArt {
		t.Fatalf("настройки: %d %v", code, res)
	}
	for _, b := range []map[string]any{{"view": "tiles"}, {"sort": "random"}, {"no_online_art": "yes"}} {
		if code, _ := call("PUT", "/api/settings", b); code != 400 {
			t.Errorf("%v принято: %d", b, code)
		}
	}

	code, res = call("POST", "/api/check", nil)
	if code != 200 || res["games"].(float64) != 2 {
		t.Fatalf("проверка: %d %v", code, res)
	}
	probs := res["problems"].([]any)
	if len(probs) != 1 || probs[0].(map[string]any)["id"] != bad.ID || !strings.Contains(probs[0].(map[string]any)["problem"].(string), "Epic") {
		t.Errorf("проблемы: %v", probs)
	}

	_, st := call("GET", "/api/state", nil)
	if st["avg_launch_ms"].(float64) != 0 || st["library_updated"].(float64) == 0 {
		t.Errorf("время запуска/обновления: %v %v", st["avg_launch_ms"], st["library_updated"])
	}
	if gameState(t, call, g.ID)["desc"] == "" {
		t.Error("у PUBG нет описания из шаблона")
	}
	app.Store.AddHistory(HistoryEntry{Time: 1, GameID: g.ID, GameName: "PUBG", State: "done", Duration: 6000})
	app.Store.AddHistory(HistoryEntry{Time: 2, GameID: g.ID, GameName: "PUBG", State: "done", Duration: 10000})
	app.Store.AddHistory(HistoryEntry{Time: 3, GameID: g.ID, GameName: "PUBG", State: "error"})
	if _, st = call("GET", "/api/state", nil); st["avg_launch_ms"].(float64) != 8000 {
		t.Errorf("среднее время запуска: %v", st["avg_launch_ms"])
	}
}

// Проверка на настоящем .exe (например, собранном ZeroLauncher.exe): ZERO_REAL_EXE=путь go test -run TestRealExeIcon
func TestRealExeIcon(t *testing.T) {
	p := os.Getenv("ZERO_REAL_EXE")
	if p == "" {
		t.Skip("только по запросу")
	}
	raw, err := exeIconPNG(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("иконка %dx%d, %d байт", cfg.Width, cfg.Height, len(raw))
	if out := os.Getenv("ZERO_REAL_OUT"); out != "" {
		_ = os.WriteFile(out, raw, 0o644)
	}
}

func TestArtSchemaResetsMisses(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "art"), 0o755)
	// индекс прошлой версии: .exe помечен как «иконки нет»
	writeBytes(t, filepath.Join(dir, "art", "index.json"), []byte(`{"games":{},"launchers":{},"misses":{"C:\\Games\\TLauncher.exe|1|2":1}}`))
	a := NewArtStore(dir)
	if a.missed(`C:\Games\TLauncher.exe|1|2`) {
		t.Error("после обновления программы старые промахи должны забываться")
	}
}
