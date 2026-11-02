package main

// Демо-сервер для проверки интерфейса без Windows: поддельные лаунчеры, процессы и картинки.
// Все картинки рисуются здесь же (градиенты и фигуры) — чужих обложек в демо нет.
// Запуск: ZERO_DEMO=/путь/к/url.txt go test -run TestDemoServer -timeout 0

import (
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"math"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type slowStarter struct{ procs *fakeProcs }

func (s slowStarter) Start(path, args, cwd string, admin bool) error {
	name := strings.ToLower(baseName(path))
	if name == "launcher.exe" {
		name = "socialclubhelper.exe"
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		s.procs.add(name)
		if strings.Contains(args, "--launch-product=valorant") {
			s.procs.add("valorant-win64-shipping.exe")
		}
	}()
	return nil
}

func (s slowStarter) OpenURI(string) error { return nil }

type demoPalette struct{ sky1, sky2, sun, ground color.RGBA }

func lerp(a, b uint8, t float64) uint8 { return uint8(float64(a)*(1-t) + float64(b)*t) }

func mix(a, b color.RGBA, t float64) color.RGBA {
	t = math.Max(0, math.Min(1, t))
	return color.RGBA{lerp(a.R, b.R, t), lerp(a.G, b.G, t), lerp(a.B, b.B, t), 255}
}

// demoScene — небо, солнце и силуэт города: заглушка вместо настоящего арта игры.
func demoScene(w, h int, p demoPalette, sunX, sunY float64, seed int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	sx, sy, sr := sunX*float64(w), sunY*float64(h), float64(min(w, h))*0.16
	heights := make([]float64, w)
	bw := max(8, w/48)
	for x := 0; x < w; x++ {
		k := x / bw
		v := math.Abs(math.Sin(float64(k*7919+seed*31))) * 0.32
		if (k+seed)%5 == 0 {
			v += 0.18
		}
		heights[x] = 0.42 + v
	}
	for y := 0; y < h; y++ {
		fy := float64(y) / float64(h)
		row := mix(p.sky1, p.sky2, fy*1.15)
		for x := 0; x < w; x++ {
			c := row
			d := math.Hypot(float64(x)-sx, float64(y)-sy)
			if d < sr {
				c = mix(p.sun, color.RGBA{255, 240, 200, 255}, 1-d/sr)
			} else if d < sr*2.6 {
				c = mix(c, p.sun, (1-(d-sr)/(sr*1.6))*0.55)
			}
			if fy > heights[x] {
				c = mix(p.ground, color.RGBA{8, 6, 20, 255}, (fy-heights[x])*1.6)
				if (x/3+y/5)%17 == 0 && fy < 0.92 {
					c = mix(c, p.sun, 0.7) // окна
				}
			}
			if fy > 0.9 {
				c = mix(c, color.RGBA{8, 6, 20, 255}, (fy-0.9)*8)
			}
			img.Set(x, y, c)
		}
	}
	return img
}

// demoLogo — абстрактная «надпись» из полосок на прозрачном фоне.
func demoLogo(w, h int, c color.RGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	bars := []struct{ x, y, bw, bh float64 }{{0.02, 0.1, 0.16, 0.8}, {0.22, 0.1, 0.16, 0.8}, {0.42, 0.1, 0.56, 0.34}, {0.42, 0.56, 0.4, 0.34}}
	for _, b := range bars {
		for y := int(b.y * float64(h)); y < int((b.y+b.bh)*float64(h)); y++ {
			for x := int(b.x * float64(w)); x < int((b.x+b.bw)*float64(w)); x++ {
				img.Set(x, y, color.NRGBA{c.R, c.G, c.B, 245})
			}
		}
	}
	return img
}

// demoIcon — круг или ромб с обводкой: иконка программы.
func demoIcon(t testing.TB, size int, c color.RGBA, diamond bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	r := float64(size)/2 - 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-float64(size)/2, float64(y)-float64(size)/2
			d := math.Hypot(dx, dy)
			if diamond {
				d = (math.Abs(dx) + math.Abs(dy)) * 0.78
			}
			switch {
			case d < r*0.45:
				img.Set(x, y, color.NRGBA{255, 255, 255, 255})
			case d < r*0.8:
				img.Set(x, y, color.NRGBA{c.R, c.G, c.B, 255})
			case d < r:
				img.Set(x, y, color.NRGBA{c.R / 2, c.G / 2, c.B / 2, 255})
			}
		}
	}
	return pngBytes(t, img)
}

func TestDemoServer(t *testing.T) {
	out := os.Getenv("ZERO_DEMO")
	if out == "" {
		t.Skip("только по запросу")
	}
	fw, st := newTestStore(t)

	// «CDN» для каталога Epic: картинки отдаются с локального сервера
	cdn := &cdnServer{files: map[string][]byte{}, etags: map[string]string{}, hits: map[string]int{}}
	srv := httptest.NewServer(cdn)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	artURLAllowed = func(u *url.URL) bool { return u.Host == host }

	sunset := demoPalette{color.RGBA{40, 18, 96, 255}, color.RGBA{255, 120, 70, 255}, color.RGBA{255, 170, 60, 255}, color.RGBA{30, 14, 52, 255}}
	olive := demoPalette{color.RGBA{30, 52, 44, 255}, color.RGBA{210, 160, 80, 255}, color.RGBA{255, 205, 90, 255}, color.RGBA{24, 30, 26, 255}}
	ocean := demoPalette{color.RGBA{16, 30, 90, 255}, color.RGBA{120, 70, 220, 255}, color.RGBA{120, 230, 255, 255}, color.RGBA{14, 12, 48, 255}}
	empty := os.Getenv("ZERO_DEMO_EMPTY") != ""
	if !empty {
		lc := fw.root + "/PF86/Steam/appcache/librarycache"
		writeBytes(t, lc+"/578080/abc123/library_600x900.jpg", jpegBytes(t, demoScene(600, 900, olive, 0.5, 0.33, 1)))
		writeBytes(t, lc+"/578080/library_hero.jpg", jpegBytes(t, demoScene(1920, 620, olive, 0.72, 0.42, 2)))
		writeBytes(t, lc+"/271590/library_600x900.jpg", jpegBytes(t, demoScene(600, 900, sunset, 0.55, 0.3, 3)))
		writeBytes(t, lc+"/271590/library_hero.jpg", jpegBytes(t, demoScene(1920, 620, sunset, 0.7, 0.36, 4)))
		writeBytes(t, lc+"/271590/logo.png", pngBytes(t, demoLogo(640, 300, color.RGBA{255, 255, 255, 255})))

		cdn.set("/fn/tall.jpg", jpegBytes(t, demoScene(600, 800, ocean, 0.5, 0.3, 5)), `"t"`)
		cdn.set("/fn/wide.jpg", jpegBytes(t, demoScene(1600, 900, ocean, 0.68, 0.38, 6)), `"w"`)
		raw, _ := json.Marshal([]map[string]any{{"id": "4fe75bbc5a674f4f9b356b5c90567da5", "namespace": "fn", "title": "Fortnite",
			"keyImages":   []map[string]string{{"type": "DieselGameBoxTall", "url": srv.URL + "/fn/tall.jpg"}, {"type": "DieselGameBox", "url": srv.URL + "/fn/wide.jpg"}},
			"releaseInfo": []map[string]string{{"appId": "Fortnite"}}}})
		writeBytes(t, fw.root+"/ProgramData/Epic/EpicGamesLauncher/Data/Catalog/catcache.bin", []byte(base64.StdEncoding.EncodeToString(raw)))

		writeBytes(t, fw.root+"/ProgramData/Riot Games/Metadata/valorant.live/valorant.live.ico",
			icoFile([]testIcon{{256, 256, 32, demoIcon(t, 256, color.RGBA{255, 70, 85, 255}, true)}}))
		writeBytes(t, fw.root+"/Roaming/.minecraft/TLauncher.exe", peWithIcons(t, []testIcon{{256, 256, 32, demoIcon(t, 256, color.RGBA{70, 200, 90, 255}, false)}}))
		// Minecraft Launcher из Microsoft Store: у него нет .exe, только ссылка shell:AppsFolder
		pkg := fw.root + "/WindowsApps/Microsoft.4297127D64EC6_1.0.0.0_x64__8wekyb3d8bbwe"
		fakePackage(t, pkg, "Minecraft")
		writeBytes(t, pkg+"/Assets/Square44x44Logo.targetsize-256_altform-unplated.png", demoIcon(t, 256, color.RGBA{110, 170, 60, 255}, true))
		_ = os.MkdirAll(fw.root+"/Local/Packages/Microsoft.4297127D64EC6_8wekyb3d8bbwe", 0o755)
		appxRootLookup = func(family string) string {
			if family == "Microsoft.4297127D64EC6_8wekyb3d8bbwe" {
				return pkg
			}
			return ""
		}
		// старая игра Steam: только горизонтальная шапка магазина
		writeBytes(t, lc+"/570/header.jpg", jpegBytes(t, demoScene(460, 215, sunset, 0.3, 0.4, 7)))
	}
	// иконки лаунчеров — из их .exe
	writeBytes(t, fw.root+"/PF86/Steam/steam.exe", peWithIcons(t, []testIcon{{64, 64, 32, demoIcon(t, 64, color.RGBA{40, 110, 200, 255}, false)}}))
	writeBytes(t, fw.root+"/C/Riot Games/Riot Client/RiotClientServices.exe", peWithIcons(t, []testIcon{{64, 64, 32, demoIcon(t, 64, color.RGBA{235, 60, 60, 255}, true)}}))
	writeBytes(t, fw.root+"/C/RAGEMP/updater.exe", peWithIcons(t, []testIcon{{64, 64, 32, demoIcon(t, 64, color.RGBA{230, 50, 90, 255}, false)}}))
	writeBytes(t, fw.root+"/PF/Rockstar Games/Launcher/Launcher.exe", peWithIcons(t, []testIcon{{64, 64, 32, demoIcon(t, 64, color.RGBA{250, 190, 40, 255}, true)}}))

	four, six := 4, 6
	_ = st.Update(func(c *Config) error {
		for id, o := range c.Launchers {
			o.Delay = &four
			if id == "socialclub" {
				o.Delay = &six
			}
		}
		return nil
	})
	if !empty {
		ls := launchersByID(st)
		for _, it := range scanAll(st, ls, []string{"steam", "riot", "minecraft"}, nil) {
			if it.Name == "PUBG: BATTLEGROUNDS" || it.Name == "VALORANT" || it.Name == "Minecraft (TLauncher)" || it.Name == "Minecraft" {
				st.AddGame(map[string]any{"name": it.Name, "target": it.Target, "args": it.Args, "platform": it.Platform,
					"online": it.Online, "requires": it.Requires, "source_id": it.SourceID, "icon_src": it.IconSrc})
			}
		}
		st.AddGame(map[string]any{"name": "Hollow Knight", "target": fw.gamesFolder + "/Hollow Knight/hollow_knight.exe"})
		st.AddGame(map[string]any{"name": "Dota 2", "target": "steam://rungameid/570", "platform": "steam", "online": true, "requires": []any{"steam"}})
		st.AddGame(map[string]any{"name": "Fortnite", "target": "com.epicgames.launcher://apps/fn%3A4fe75bbc5a674f4f9b356b5c90567da5%3AFortnite?action=launch&silent=true",
			"platform": "epic", "online": true, "requires": []any{"epic"}})
		g := st.AddGame(map[string]any{"name": "GTA 5 RP", "target": "{rage}", "platform": "rage", "online": true,
			"requires": []any{"steam", "socialclub"}, "favorite": true})
		if os.Getenv("ZERO_DEMO_SERVER") != "" {
			st.UpdateGame(g.ID, map[string]any{"server": "blaine.gta5rp.com:22005", "server_name": "Blaine"})
		}
		_ = st.Update(func(c *Config) error {
			for _, x := range c.Games {
				if x.ID == g.ID {
					x.LastPlayed, x.PlayCount = time.Now().Add(-2*time.Hour).Unix(), 12
				}
			}
			return nil
		})
	}
	if os.Getenv("ZERO_DEMO_HISTORY") != "" {
		now := time.Now().Unix()
		_ = st.Update(func(c *Config) error {
			for _, x := range c.Games {
				switch x.Name {
				case "VALORANT":
					x.PlayCount, x.LastPlayed, x.Favorite = 7, now-5*3600, true
					c.History = append(c.History, HistoryEntry{Time: now - 5*3600, GameID: x.ID, GameName: x.Name, State: "done", Message: "Игра запущена", Duration: 7400})
				case "PUBG: BATTLEGROUNDS":
					x.PlayCount, x.LastPlayed = 4, now-26*3600
					c.History = append(c.History, HistoryEntry{Time: now - 26*3600, GameID: x.ID, GameName: x.Name, State: "done", Message: "Игра запущена", Duration: 9100})
				case "Fortnite":
					c.History = append(c.History, HistoryEntry{Time: now - 50*3600, GameID: x.ID, GameName: x.Name, State: "error", Message: "Epic Games: путь не указан. Укажите путь в разделе «Лаунчеры»."})
				case "Minecraft (TLauncher)":
					x.PlayCount, x.LastPlayed = 2, now-3*86400
					c.History = append(c.History, HistoryEntry{Time: now - 3*86400, GameID: x.ID, GameName: x.Name, State: "done", Message: "Игра запущена", Duration: 6800})
				case "GTA 5 RP":
					c.History = append(c.History, HistoryEntry{Time: now - 2*3600, GameID: x.ID, GameName: x.Name, State: "done", Message: "Подключение к Blaine", Duration: 8300})
				}
			}
			return nil
		})
	}
	p := &fakeProcs{running: map[string]bool{}}
	app := NewApp(st, NewProbe(p.snapshot), slowStarter{p})
	app.Art.RefreshAll(st, false, true)
	app.StartArt()
	if err := app.Listen(8766); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(out, []byte(app.URL()), 0o644)
	time.Sleep(10 * time.Minute)
}
