package main

// Иконки из ярлыков, Microsoft Store и Xbox; новые источники игр: Xbox, EA app, Battle.net.

import (
	"bytes"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublisherID(t *testing.T) {
	// так Windows называет пакеты Microsoft: Microsoft.MinecraftUWP_8wekyb3d8bbwe
	got := publisherID("CN=Microsoft Corporation, O=Microsoft Corporation, L=Redmond, S=Washington, C=US")
	if got != "8wekyb3d8bbwe" {
		t.Fatalf("издатель: %s", got)
	}
}

func TestExeIconIndexAndSplitSections(t *testing.T) {
	dir := t.TempDir()
	first := iconPNG(t, 32, color.RGBA{200, 0, 0, 255})
	second := iconPNG(t, 64, color.RGBA{0, 200, 0, 255})
	exe := filepath.Join(dir, "multi.exe")
	writeBytes(t, exe, peWithIconGroups(t, [][]testIcon{{{32, 32, 32, first}}, {{64, 64, 32, second}}}, false))
	if got, _ := exeIconIndexPNG(exe, 0); !bytes.Equal(got, first) {
		t.Error("номер 0 — первая группа")
	}
	if got, _ := exeIconIndexPNG(exe, 1); !bytes.Equal(got, second) {
		t.Error("номер 1 — вторая группа")
	}
	if got, _ := exeIconIndexPNG(exe, -2); !bytes.Equal(got, second) {
		t.Error("номер -2 — группа с идентификатором 2")
	}
	// данные иконок в отдельной секции, как у упакованных программ
	split := filepath.Join(dir, "packed.exe")
	writeBytes(t, split, peWithIconGroups(t, [][]testIcon{{{64, 64, 32, second}}}, true))
	if got, err := exeIconPNG(split); err != nil || !bytes.Equal(got, second) {
		t.Errorf("иконка из другой секции: %v", err)
	}
}

func TestShortcutIcons(t *testing.T) {
	dir := t.TempDir()
	gameIcon := iconPNG(t, 128, color.RGBA{30, 160, 60, 255})
	exe := filepath.Join(dir, "Game", "game.exe")
	writeBytes(t, exe, peWithIcons(t, []testIcon{{128, 128, 32, gameIcon}}))

	// ярлык без своего значка — иконка программы, на которую он указывает
	lnk := filepath.Join(dir, "Game.lnk")
	writeBytes(t, lnk, lnkFile(exe, "", 0, "-fullscreen"))
	l := parseLnk(lnk)
	if l == nil || l.Target != exe || l.Args != "-fullscreen" {
		t.Fatalf("ярлык разобран неверно: %+v", l)
	}
	src := iconSourcesForPath(lnk, 0)
	if len(src) == 0 || src[0].path != exe {
		t.Fatalf("источники: %+v", src)
	}

	// значок, выбранный в ярлыке, важнее: .ico и «файл,номер»
	ico := filepath.Join(dir, "Game", "brand.ico")
	brand := iconPNG(t, 256, color.RGBA{250, 200, 20, 255})
	writeBytes(t, ico, icoFile([]testIcon{{256, 256, 32, brand}}))
	writeBytes(t, lnk, lnkFile(exe, ico, 0, ""))
	src = iconSourcesForPath(lnk, 0)
	if len(src) == 0 || src[0].path != ico || src[0].conv != "ico" {
		t.Fatalf("значок ярлыка: %+v", src)
	}
	multi := filepath.Join(dir, "Game", "icons.dll")
	writeBytes(t, multi, peWithIconGroups(t, [][]testIcon{{{32, 32, 32, gameIcon}}, {{256, 256, 32, brand}}}, false))
	writeBytes(t, lnk, lnkFile(exe, multi, 1, ""))
	src = iconSourcesForPath(lnk, 0)
	if len(src) == 0 || src[0].path != multi || src[0].index != 1 {
		t.Fatalf("значок с номером: %+v", src)
	}
	if data, _, err := loadArtFile(src[0]); err != nil || !bytes.Equal(data, brand) {
		t.Errorf("загрузка значка с номером: %v", err)
	}

	// javaw.exe -jar TLauncher.jar: значок рядом с .jar, а не значок Java
	jarDir := filepath.Join(dir, "TL")
	writeBytes(t, filepath.Join(jarDir, "TLauncher.jar"), []byte("PK"))
	writeBytes(t, filepath.Join(jarDir, "TLauncher.ico"), icoFile([]testIcon{{64, 64, 32, gameIcon}}))
	java := filepath.Join(dir, "jre", "bin", "javaw.exe")
	writeBytes(t, java, peWithIcons(t, []testIcon{{32, 32, 32, iconPNG(t, 32, color.RGBA{0, 0, 255, 255})}}))
	writeBytes(t, lnk, lnkFile(java, "", 0, `-jar "`+filepath.Join(jarDir, "TLauncher.jar")+`"`))
	found := false
	for _, s := range iconSourcesForPath(lnk, 0) {
		if s.path == filepath.Join(jarDir, "TLauncher.ico") {
			found = true
		}
	}
	if !found {
		t.Error("значок .jar не найден")
	}
	// .jar напрямую
	if src := iconSourcesForPath(filepath.Join(jarDir, "TLauncher.jar"), 0); len(src) == 0 || !strings.HasSuffix(src[0].path, "TLauncher.ico") {
		t.Errorf("значок у .jar: %+v", src)
	}

	// интернет-ярлык со значком
	url := filepath.Join(dir, "Dota.url")
	writeBytes(t, url, []byte("[InternetShortcut]\r\nURL=steam://rungameid/570\r\nIconFile="+ico+"\r\nIconIndex=0\r\n"))
	if src := iconSourcesForPath(url, 0); len(src) == 0 || src[0].path != ico {
		t.Errorf("значок .url: %+v", src)
	}
}

// fakePackage — установленное приложение Microsoft Store: манифест и картинки с суффиксами масштаба.
func fakePackage(t *testing.T, root string, appID string) []byte {
	manifest := `<?xml version="1.0" encoding="utf-8"?>
<Package xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10" xmlns:uap="http://schemas.microsoft.com/appx/manifest/uap/windows10">
  <Identity Name="Microsoft.4297127D64EC6" Publisher="CN=Microsoft Corporation, O=Microsoft Corporation, L=Redmond, S=Washington, C=US" Version="1.0.0.0"/>
  <Properties><DisplayName>Minecraft Launcher</DisplayName><Logo>Assets\StoreLogo.png</Logo></Properties>
  <Applications>
    <Application Id="Other"><uap:VisualElements Square44x44Logo="Assets\Other.png" Square150x150Logo="Assets\Other150.png"/></Application>
    <Application Id="` + appID + `" Executable="Minecraft.exe">
      <uap:VisualElements DisplayName="Minecraft" Square150x150Logo="Assets\Square150x150Logo.png" Square44x44Logo="Assets\Square44x44Logo.png">
        <uap:DefaultTile Wide310x150Logo="Assets\Wide310x150Logo.png"/>
        <uap:SplashScreen Image="Assets\SplashScreen.png"/>
      </uap:VisualElements>
    </Application>
  </Applications>
</Package>`
	writeBytes(t, filepath.Join(root, "AppxManifest.xml"), []byte(manifest))
	best := iconPNG(t, 256, color.RGBA{90, 170, 60, 255})
	assets := filepath.Join(root, "Assets")
	writeBytes(t, filepath.Join(assets, "Square44x44Logo.scale-200.png"), iconPNG(t, 88, color.RGBA{1, 1, 1, 255}))
	writeBytes(t, filepath.Join(assets, "Square44x44Logo.targetsize-256.png"), iconPNG(t, 256, color.RGBA{2, 2, 2, 255}))
	writeBytes(t, filepath.Join(assets, "Square44x44Logo.targetsize-256_altform-unplated.png"), best)
	writeBytes(t, filepath.Join(assets, "Square44x44Logo.targetsize-256_altform-lightunplated.png"), iconPNG(t, 256, color.RGBA{3, 3, 3, 255}))
	writeBytes(t, filepath.Join(assets, "Square44x44Logo.targetsize-256_contrast-white.png"), iconPNG(t, 256, color.RGBA{4, 4, 4, 255}))
	writeBytes(t, filepath.Join(assets, "SplashScreen.scale-200.png"), pngBytes(t, gradientImage(1240, 600, color.RGBA{20, 60, 20, 255}, color.RGBA{90, 170, 60, 255})))
	writeBytes(t, filepath.Join(assets, "Other.png"), iconPNG(t, 44, color.RGBA{5, 5, 5, 255}))
	return best
}

func TestMinecraftStoreIcon(t *testing.T) {
	fw, app, call, base := artApp(t)
	// официальный Minecraft Launcher из Microsoft Store: только ссылка shell:AppsFolder, никакого .exe
	pkgRoot := filepath.Join(fw.root, "WindowsApps", "Microsoft.4297127D64EC6_1.0.0.0_x64__8wekyb3d8bbwe")
	best := fakePackage(t, pkgRoot, "Minecraft")
	_ = os.MkdirAll(fw.root+"/Local/Packages/Microsoft.4297127D64EC6_8wekyb3d8bbwe", 0o755)
	_ = os.Remove(fw.root + "/Roaming/.minecraft/TLauncher.exe")
	old := appxRootLookup
	appxRootLookup = func(family string) string {
		if family == "Microsoft.4297127D64EC6_8wekyb3d8bbwe" {
			return pkgRoot
		}
		return ""
	}
	t.Cleanup(func() { appxRootLookup = old })

	items := scanMinecraft(&scanCtx{})
	if len(items) != 1 || !strings.HasPrefix(items[0].Target, `shell:AppsFolder\Microsoft.4297127D64EC6_8wekyb3d8bbwe!Minecraft`) {
		t.Fatalf("Minecraft из Store: %+v", items)
	}
	g := app.Store.AddGame(map[string]any{"name": items[0].Name, "target": items[0].Target, "platform": "minecraft",
		"source_id": items[0].SourceID})
	app.Art.RefreshAll(app.Store, false, false)
	gs := gameState(t, call, g.ID)
	if gs["icon_url"] == "" {
		t.Fatal("у Minecraft из Store нет иконки")
	}
	if got := fetchImage(t, base, gs["icon_url"].(string), app.Token); !bytes.Equal(got, best) {
		t.Error("взята не та картинка: нужна самая крупная без подложки (altform-unplated)")
	}
	if gs["hero_url"] == "" {
		t.Error("заставка приложения не стала фоном")
	}
}

func TestSteamIconFromInstallDir(t *testing.T) {
	fw, app, call, base := artApp(t)
	// PUBG добавлен шаблоном: в игре нет папки установки, но Steam знает её по appmanifest
	icon := iconPNG(t, 256, color.RGBA{220, 160, 40, 255})
	writeBytes(t, fw.root+"/PF86/Steam/steamapps/common/PUBG/TslGame/Binaries/Win64/TslGame.exe",
		peWithIcons(t, []testIcon{{256, 256, 32, icon}}))
	g := app.Store.AddGame(map[string]any{"name": "PUBG: BATTLEGROUNDS", "target": "steam://rungameid/578080", "platform": "steam"})
	app.Art.RefreshAll(app.Store, false, false)
	gs := gameState(t, call, g.ID)
	if gs["icon_url"] == "" || !bytes.Equal(fetchImage(t, base, gs["icon_url"].(string), app.Token), icon) {
		t.Fatal("иконка игры Steam из папки установки")
	}
	// у старой игры нет вертикальной обложки — берётся шапка магазина
	cache := fw.root + "/PF86/Steam/appcache/librarycache"
	_ = os.RemoveAll(cache + "/578080")
	header := jpegBytes(t, gradientImage(46, 21, color.RGBA{10, 10, 90, 255}, color.RGBA{90, 10, 10, 255}))
	writeBytes(t, cache+"/578080/header.jpg", header)
	app.Art.RefreshAll(app.Store, false, false)
	gs = gameState(t, call, g.ID)
	if gs["cover_url"] == "" || !bytes.Equal(fetchImage(t, base, gs["cover_url"].(string), app.Token), header) {
		t.Error("шапка Steam вместо обложки")
	}
}

func TestScanXboxEABattlenet(t *testing.T) {
	fw, _ := newTestStore(t)
	// Xbox: игра Game Pass в C:\XboxGames
	content := fw.root + "/C/XboxGames/Forza Horizon 5/Content"
	writeBytes(t, content+"/MicrosoftGame.config", []byte(`<?xml version="1.0"?>
<Game configVersion="1">
  <Identity Name="Microsoft.624F8B84B80" Publisher="CN=Microsoft Corporation, O=Microsoft Corporation, L=Redmond, S=Washington, C=US" Version="1.0.0.0"/>
  <ExecutableList><Executable Name="ForzaHorizon5.exe" Id="Game"/></ExecutableList>
  <ShellVisuals DefaultDisplayName="Forza Horizon 5" Square150x150Logo="Logo.png" Square44x44Logo="SmallLogo.png" SplashScreenImage="SplashScreen.png" StoreLogo="StoreLogo.png"/>
</Game>`))
	small := iconPNG(t, 88, color.RGBA{200, 40, 120, 255})
	writeBytes(t, content+"/SmallLogo.png", small)
	items := scanXbox(&scanCtx{})
	if len(items) != 1 || items[0].Name != "Forza Horizon 5" || items[0].Platform != "xbox" ||
		items[0].Target != `shell:AppsFolder\Microsoft.624F8B84B80_8wekyb3d8bbwe!Game` {
		t.Fatalf("Xbox: %+v", items)
	}
	if src := packageVisualSources(content, "", "icon"); len(src) == 0 || !strings.HasSuffix(src[0].path, "SmallLogo.png") {
		t.Errorf("значок игры Xbox: %+v", src)
	}

	// EA app и Battle.net — по списку установленных программ
	ea := fw.root + "/PF/EA Games/Apex"
	writeBytes(t, ea+"/r5apex.exe", []byte("MZ"+strings.Repeat("\x00", 9000)))
	writeBytes(t, ea+"/EasyAntiCheat_launcher.exe", []byte("MZ"+strings.Repeat("\x00", 90000)))
	ow := fw.root + "/PF86/Overwatch"
	writeBytes(t, ow+"/_retail_/Overwatch.exe", []byte("MZ"+strings.Repeat("\x00", 9000)))
	old := uninstallEntries
	uninstallEntries = func() []uninstallEntry {
		return []uninstallEntry{
			{Name: "Apex Legends", Publisher: "Electronic Arts", InstallLocation: ea, UninstallString: `"C:\Program Files\Common Files\EAInstaller\Apex\Cleanup.exe"`},
			{Name: "EA app", Publisher: "Electronic Arts", InstallLocation: ea},
			{Name: "Overwatch", Publisher: "Blizzard Entertainment", InstallLocation: ow,
				UninstallString: `"C:\ProgramData\Battle.net\Agent\Blizzard Uninstaller.exe" --lang=enUS --uid=prometheus --displayname="Overwatch"`},
			{Name: "Battle.net", Publisher: "Blizzard Entertainment", InstallLocation: ow,
				UninstallString: `"C:\Program Files (x86)\Battle.net\Battle.net.exe" --uid=battle.net`},
			{Name: "7-Zip", Publisher: "Igor Pavlov", InstallLocation: fw.root},
		}
	}
	t.Cleanup(func() { uninstallEntries = old })
	eaItems := scanEA(&scanCtx{})
	if len(eaItems) != 1 || eaItems[0].Name != "Apex Legends" || !strings.HasSuffix(eaItems[0].Target, "r5apex.exe") ||
		!contains(eaItems[0].Requires, "ea") {
		t.Errorf("EA: %+v", eaItems)
	}
	bn := scanBattlenet(&scanCtx{})
	if len(bn) != 1 || bn[0].Target != "{battlenet}" || bn[0].Args != `--exec="launch Pro"` || bn[0].InstallDir != ow {
		t.Errorf("Battle.net: %+v", bn)
	}
}
