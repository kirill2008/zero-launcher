package main

// Откуда ещё брать иконку игры, кроме самого .exe:
//   - ярлык .lnk: значок, который выбрал разработчик (IconLocation), или программа, на которую он указывает;
//   - интернет-ярлык .url: IconFile/IconIndex;
//   - приложение Microsoft Store или Xbox (shell:AppsFolder\…): картинки из AppxManifest.xml
//     или MicrosoftGame.config — так запускается официальный Minecraft Launcher и игры Game Pass;
//   - .ico и картинки-логотипы рядом с игрой (для .jar, .bat и игр без значка в .exe).

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ------------------------------------------------------------------ ярлыки .lnk

type shellLink struct {
	Target    string // куда указывает ярлык
	Args      string
	Workdir   string
	IconPath  string // значок, выбранный в ярлыке
	IconIndex int
}

const (
	lnkHasIDList   = 0x1
	lnkHasLinkInfo = 0x2
	lnkHasName     = 0x4
	lnkHasRelPath  = 0x8
	lnkHasWorkDir  = 0x10
	lnkHasArgs     = 0x20
	lnkHasIconLoc  = 0x40
	lnkIsUnicode   = 0x80
)

// parseLnk разбирает ярлык Windows (формат MS-SHLLINK). Ошибочные или чужие файлы — nil.
func parseLnk(path string) *shellLink {
	st, err := os.Stat(path)
	if err != nil || st.Size() < 0x4c || st.Size() > 1<<20 {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil || binary.LittleEndian.Uint32(b) != 0x4c {
		return nil
	}
	flags := binary.LittleEndian.Uint32(b[0x14:])
	out := &shellLink{IconIndex: int(int32(binary.LittleEndian.Uint32(b[0x38:])))}
	pos := 0x4c
	if flags&lnkHasIDList != 0 {
		if pos+2 > len(b) {
			return nil
		}
		pos += 2 + int(binary.LittleEndian.Uint16(b[pos:]))
	}
	if flags&lnkHasLinkInfo != 0 {
		if pos+28 > len(b) {
			return nil
		}
		size := int(binary.LittleEndian.Uint32(b[pos:]))
		if size < 28 || pos+size > len(b) {
			return nil
		}
		li := b[pos : pos+size]
		hdr := binary.LittleEndian.Uint32(li[4:])
		liFlags := binary.LittleEndian.Uint32(li[8:])
		if liFlags&1 != 0 {
			at := func(o uint32) string {
				if o == 0 {
					return ""
				}
				return cString(li, int(o))
			}
			base := at(binary.LittleEndian.Uint32(li[16:]))
			suffix := at(binary.LittleEndian.Uint32(li[24:]))
			if hdr >= 0x24 && len(li) >= 0x24 {
				if o := int(binary.LittleEndian.Uint32(li[28:])); o > 0 {
					base = wString(li, o)
				}
				if o := int(binary.LittleEndian.Uint32(li[32:])); o > 0 {
					suffix = wString(li, o)
				}
			}
			out.Target = base + suffix
		}
		pos += size
	}
	unicode := flags&lnkIsUnicode != 0
	readStr := func() string {
		if pos+2 > len(b) {
			pos = len(b)
			return ""
		}
		n := int(binary.LittleEndian.Uint16(b[pos:]))
		pos += 2
		if unicode {
			n *= 2
		}
		if pos+n > len(b) {
			pos = len(b)
			return ""
		}
		raw := b[pos : pos+n]
		pos += n
		if unicode {
			return decodeUTF16(raw)
		}
		return string(raw)
	}
	var rel string
	if flags&lnkHasName != 0 {
		readStr()
	}
	if flags&lnkHasRelPath != 0 {
		rel = readStr()
	}
	if flags&lnkHasWorkDir != 0 {
		out.Workdir = readStr()
	}
	if flags&lnkHasArgs != 0 {
		out.Args = readStr()
	}
	if flags&lnkHasIconLoc != 0 {
		out.IconPath = readStr()
	}
	// дополнительные блоки: пути с переменными окружения (%ProgramFiles%\…)
	for pos+8 <= len(b) {
		size := int(binary.LittleEndian.Uint32(b[pos:]))
		if size < 8 || pos+size > len(b) {
			break
		}
		sig := binary.LittleEndian.Uint32(b[pos+4:])
		if (sig == 0xA0000001 || sig == 0xA0000007) && size >= 788 {
			v := wString(b[pos+268:pos+788], 0)
			if v == "" {
				v = cString(b[pos+8:pos+268], 0)
			}
			if sig == 0xA0000001 && out.Target == "" {
				out.Target = v
			}
			if sig == 0xA0000007 {
				out.IconPath = v
			}
		}
		pos += size
	}
	if out.Target == "" && rel != "" {
		out.Target = filepath.Join(filepath.Dir(path), rel)
	}
	out.Target = expandWin(out.Target)
	out.IconPath = expandWin(out.IconPath)
	out.Workdir = expandWin(out.Workdir)
	if out.IconPath != "" && !filepath.IsAbs(out.IconPath) && !strings.Contains(out.IconPath, ":") {
		out.IconPath = filepath.Join(filepath.Dir(path), out.IconPath)
	}
	return out
}

func cString(b []byte, off int) string {
	if off < 0 || off >= len(b) {
		return ""
	}
	end := bytes.IndexByte(b[off:], 0)
	if end < 0 {
		end = len(b) - off
	}
	return string(b[off : off+end])
}

func wString(b []byte, off int) string {
	if off < 0 || off >= len(b) {
		return ""
	}
	var u []uint16
	for i := off; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

// expandWin раскрывает %ПЕРЕМЕННЫЕ% и приводит разделители к системным (для тестов на Linux).
func expandWin(p string) string {
	p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"`))
	if p == "" {
		return ""
	}
	if s, ok := expandPercent(p); ok {
		p = s
	}
	if !isWindows {
		p = strings.ReplaceAll(p, `\`, "/")
	}
	return p
}

// splitIconLocation: "C:\Games\x.exe,-101" → путь и номер.
func splitIconLocation(s string) (string, int) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ","); i > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(s[i+1:])); err == nil {
			return strings.Trim(strings.TrimSpace(s[:i]), `"`), n
		}
	}
	return strings.Trim(s, `"`), 0
}

// ------------------------------------------------------------------ интернет-ярлыки .url

func parseURLFile(path string) (target, icon string, index int) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 1<<20 {
		return "", "", 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "url":
			target = strings.TrimSpace(v)
		case "iconfile":
			icon = expandWin(v)
		case "iconindex":
			index, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return target, icon, index
}

// ------------------------------------------------------------------ иконка по пути к файлу

// iconSourcesForPath — откуда взять иконку для файла, который запускает игру.
func iconSourcesForPath(p string, depth int) []artSource {
	p = strings.Trim(strings.TrimSpace(p), `"`)
	if p == "" || depth > 3 {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(p), "shell:appsfolder") {
		return appxSources(p, "icon")
	}
	if isURI(p) {
		return nil
	}
	var out []artSource
	switch strings.ToLower(filepath.Ext(p)) {
	case ".exe", ".dll":
		if isFile(p) {
			out = append(out, artSource{from: "exe", path: p, conv: "exe"})
		}
	case ".ico":
		if isFile(p) {
			out = append(out, artSource{from: "exe", path: p, conv: "ico"})
		}
	case ".png", ".jpg", ".jpeg", ".webp":
		if isFile(p) {
			out = append(out, artSource{from: "exe", path: p})
		}
	case ".lnk":
		if l := parseLnk(p); l != nil {
			if l.IconPath != "" {
				ip := l.IconPath
				switch strings.ToLower(filepath.Ext(ip)) {
				case ".exe", ".dll":
					if isFile(ip) {
						out = append(out, artSource{from: "exe", path: ip, conv: "exe", index: l.IconIndex})
					}
				default:
					out = append(out, iconSourcesForPath(ip, depth+1)...)
				}
			}
			out = append(out, iconSourcesForPath(l.Target, depth+1)...)
			out = append(out, javaJarSources(l.Target, l.Args, l.Workdir)...)
		}
	case ".url":
		target, icon, idx := parseURLFile(p)
		if icon != "" {
			if ext := strings.ToLower(filepath.Ext(icon)); (ext == ".exe" || ext == ".dll") && isFile(icon) {
				out = append(out, artSource{from: "exe", path: icon, conv: "exe", index: idx})
			} else {
				out = append(out, iconSourcesForPath(icon, depth+1)...)
			}
		}
		if strings.HasPrefix(strings.ToLower(target), "shell:") {
			out = append(out, iconSourcesForPath(target, depth+1)...)
		}
	}
	// .jar, .bat, .cmd и программы без значка: картинка рядом с файлом
	if !isURI(p) {
		out = append(out, folderIconSources(filepath.Dir(p), strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))...)
	}
	return out
}

var jarArgRe = regexp.MustCompile(`(?i)-jar\s+("[^"]+\.jar"|\S+\.jar)`)

// javaJarSources: ярлык вида javaw.exe -jar TLauncher.jar — значок ищем у .jar, а не у Java.
func javaJarSources(target, args, workdir string) []artSource {
	b := strings.ToLower(baseName(target))
	if b != "java.exe" && b != "javaw.exe" && b != "java" && b != "javaw" {
		return nil
	}
	m := jarArgRe.FindStringSubmatch(args)
	if m == nil {
		return nil
	}
	jar := expandWin(m[1])
	if !filepath.IsAbs(jar) && workdir != "" {
		jar = filepath.Join(workdir, jar)
	}
	return folderIconSources(filepath.Dir(jar), strings.TrimSuffix(filepath.Base(jar), filepath.Ext(jar)))
}

var iconNameRe = regexp.MustCompile(`(?i)^(icon|app|game|logo|favicon|launcher)[\w\-]*\.(png|ico)$`)

// folderIconSources — .ico или логотип в папке игры. Сначала файл с тем же именем, что у программы.
func folderIconSources(dir, stem string) []artSource {
	if dir == "" || !isDir(dir) {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 3000 {
		return nil
	}
	type cand struct {
		path  string
		score int
	}
	var list []cand
	want := cleanName(stem)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		ext := strings.ToLower(filepath.Ext(n))
		if ext != ".ico" && ext != ".png" {
			continue
		}
		base := cleanName(strings.TrimSuffix(n, filepath.Ext(n)))
		score := 0
		switch {
		case want != "" && base == want:
			score = 100
		case iconNameRe.MatchString(n):
			score = 50
		case ext == ".ico":
			score = 30
		default:
			continue // произвольные .png рядом — скорее скриншоты, не значок
		}
		if ext == ".ico" {
			score += 5
		}
		if info, err := e.Info(); err == nil && info.Size() < 512 {
			continue
		}
		list = append(list, cand{filepath.Join(dir, n), score})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	var out []artSource
	for _, c := range list {
		conv := ""
		if strings.EqualFold(filepath.Ext(c.path), ".ico") {
			conv = "ico"
		}
		out = append(out, artSource{from: "exe", path: c.path, conv: conv})
	}
	return out
}

// ------------------------------------------------------------------ Microsoft Store и Xbox

var appsFolderRe = regexp.MustCompile(`(?i)^shell:appsfolder\\+([^!\\]+)(?:!(.+))?$`)

// appxRootLookup находит папку установленного пакета по имени семейства (Name_PublisherId).
// В Windows — через реестр, в тестах подменяется.
var appxRootLookup = func(family string) string {
	if p := appxRootFromRegistry(family); p != "" {
		return p
	}
	return appxInstallLocation(family) // запасной путь — PowerShell Get-AppxPackage (только Windows)
}

const appxRepo = `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppModel\Repository\Packages`

func appxRootFromRegistry(family string) string {
	name, pub, ok := strings.Cut(family, "_")
	if !ok {
		return ""
	}
	if i := strings.LastIndex(family, "_"); i > 0 {
		name, pub = family[:i], family[i+1:]
	}
	best := ""
	for _, full := range regSubkeys("HKCU", appxRepo) {
		low := strings.ToLower(full)
		if !strings.HasPrefix(low, strings.ToLower(name)+"_") || !strings.HasSuffix(low, "_"+strings.ToLower(pub)) {
			continue
		}
		if dir, ok := regGet("HKCU", appxRepo+`\`+full, "PackageRootFolder"); ok && isDir(dir) && full > best {
			best = dir
		}
	}
	return best
}

// appxSources — картинки приложения из Microsoft Store или Xbox по ссылке shell:AppsFolder\Семейство!Приложение.
func appxSources(target, kind string) []artSource {
	m := appsFolderRe.FindStringSubmatch(strings.TrimSpace(target))
	if m == nil {
		return nil
	}
	root := appxRootLookup(m[1])
	if root == "" {
		return nil
	}
	return packageVisualSources(root, m[2], kind)
}

// packageVisualSources читает AppxManifest.xml или MicrosoftGame.config в папке пакета.
func packageVisualSources(root, appID, kind string) []artSource {
	v := readPackageVisuals(root, appID)
	var rels []string
	switch kind {
	case "icon":
		rels = []string{v.Square44, v.Square150, v.Square310, v.StoreLogo}
	case "hero":
		rels = []string{v.Splash, v.Wide310}
	default:
		return nil
	}
	var out []artSource
	for _, rel := range rels {
		if rel == "" {
			continue
		}
		if p := resolvePackageAsset(root, rel, kind == "icon"); p != "" {
			out = append(out, artSource{from: "store", path: p})
		}
	}
	return out
}

type packageVisuals struct {
	Square44, Square150, Square310, Wide310, Splash, StoreLogo string
}

func readPackageVisuals(root, appID string) packageVisuals {
	var v packageVisuals
	if f, err := os.Open(filepath.Join(root, "AppxManifest.xml")); err == nil {
		v = parseAppxManifest(io.LimitReader(f, 4<<20), appID)
		f.Close()
	}
	if v.Square44 == "" && v.Square150 == "" {
		if f, err := os.Open(filepath.Join(root, "MicrosoftGame.config")); err == nil {
			g := parseGameConfig(io.LimitReader(f, 4<<20))
			f.Close()
			v.Square44, v.Square150, v.StoreLogo, v.Splash = g.Visuals.Square44, g.Visuals.Square150, g.Visuals.StoreLogo, g.Visuals.Splash
		}
	}
	return v
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if strings.EqualFold(a.Name.Local, name) {
			return a.Value
		}
	}
	return ""
}

// parseAppxManifest берёт картинки приложения appID (или первого приложения пакета).
func parseAppxManifest(r io.Reader, appID string) packageVisuals {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	var v, first packageVisuals
	inApp, matched, gotFirst := false, false, false
	inProps := false
	storeLogo := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "Properties":
				inProps = true
			case "Logo":
				if inProps {
					var s string
					if dec.DecodeElement(&s, &t) == nil {
						storeLogo = strings.TrimSpace(s)
					}
				}
			case "Application":
				inApp = true
				matched = appID == "" || strings.EqualFold(attr(t, "Id"), appID)
			case "VisualElements":
				if inApp {
					cur := packageVisuals{Square44: attr(t, "Square44x44Logo"), Square150: attr(t, "Square150x150Logo")}
					if matched {
						v.Square44, v.Square150 = cur.Square44, cur.Square150
					}
					if !gotFirst {
						first.Square44, first.Square150, gotFirst = cur.Square44, cur.Square150, true
					}
				}
			case "DefaultTile":
				if inApp {
					w, sq := attr(t, "Wide310x150Logo"), attr(t, "Square310x310Logo")
					if matched {
						v.Wide310, v.Square310 = w, sq
					}
					if first.Wide310 == "" {
						first.Wide310, first.Square310 = w, sq
					}
				}
			case "SplashScreen":
				if inApp {
					if matched {
						v.Splash = attr(t, "Image")
					}
					if first.Splash == "" {
						first.Splash = attr(t, "Image")
					}
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "Properties":
				inProps = false
			case "Application":
				inApp, matched = false, false
			}
		}
	}
	if v.Square44 == "" && v.Square150 == "" {
		v = first
	}
	v.StoreLogo = storeLogo
	return v
}

type gameConfig struct {
	Identity struct {
		Name      string `xml:"Name,attr"`
		Publisher string `xml:"Publisher,attr"`
	} `xml:"Identity"`
	Executables []struct {
		Name string `xml:"Name,attr"`
		ID   string `xml:"Id,attr"`
	} `xml:"ExecutableList>Executable"`
	Visuals struct {
		DisplayName string `xml:"DefaultDisplayName,attr"`
		StoreLogo   string `xml:"StoreLogo,attr"`
		Square150   string `xml:"Square150x150Logo,attr"`
		Square44    string `xml:"Square44x44Logo,attr"`
		Splash      string `xml:"SplashScreenImage,attr"`
	} `xml:"ShellVisuals"`
}

func parseGameConfig(r io.Reader) gameConfig {
	var g gameConfig
	dec := xml.NewDecoder(r)
	dec.Strict = false
	_ = dec.Decode(&g)
	return g
}

var assetQualRe = regexp.MustCompile(`(?i)(targetsize|scale)-(\d+)`)

// resolvePackageAsset: в манифесте «Assets\Logo.png», а на диске Logo.scale-200.png,
// Logo.targetsize-256_altform-unplated.png и т.п. Берём самую крупную; для иконок — без подложки.
func resolvePackageAsset(root, rel string, unplated bool) string {
	rel = strings.ReplaceAll(strings.TrimSpace(rel), `\`, string(filepath.Separator))
	rel = strings.ReplaceAll(rel, "/", string(filepath.Separator))
	if rel == "" || strings.Contains(rel, "..") {
		return ""
	}
	full := filepath.Join(root, rel)
	dir := filepath.Dir(full)
	ext := strings.ToLower(filepath.Ext(full))
	stem := strings.ToLower(strings.TrimSuffix(filepath.Base(full), filepath.Ext(full)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if isFile(full) {
			return full
		}
		return ""
	}
	best, bestScore := "", -1
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if e.IsDir() || filepath.Ext(n) != ext {
			continue
		}
		base := strings.TrimSuffix(n, ext)
		if base != stem && !strings.HasPrefix(base, stem+".") {
			continue
		}
		q := strings.TrimPrefix(base, stem)
		if strings.Contains(q, "contrast-") || strings.Contains(q, "theme-light") || strings.Contains(q, "lightunplated") {
			continue // варианты для высокой контрастности и светлой темы
		}
		size := 100
		if m := assetQualRe.FindStringSubmatch(q); m != nil {
			size, _ = strconv.Atoi(m[2])
			if strings.EqualFold(m[1], "scale") {
				size = size * 3 / 2 // scale-200 у квадрата 150 ≈ 300 px
			}
		}
		score := size
		if unplated && strings.Contains(q, "altform-unplated") {
			score += 10000
		}
		if score > bestScore {
			best, bestScore = filepath.Join(dir, e.Name()), score
		}
	}
	return best
}

// ------------------------------------------------------------------ Xbox: имя семейства пакета

const pubIDAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// publisherID — как Windows получает «8wekyb3d8bbwe» из строки издателя «CN=Microsoft Corporation, …»:
// SHA-256 от UTF-16LE, первые 8 байт, 13 символов base32.
func publisherID(publisher string) string {
	u := utf16.Encode([]rune(publisher))
	raw := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(raw[i*2:], c)
	}
	sum := sha256.Sum256(raw)
	var bits uint64
	for _, b := range sum[:8] {
		bits = bits<<8 | uint64(b)
	}
	out := make([]byte, 13)
	for i := 0; i < 13; i++ {
		shift := 64 - 5*(i+1) // 65-й бит — дополнительный ноль
		var v uint64
		if shift >= 0 {
			v = (bits >> uint(shift)) & 31
		} else {
			v = (bits << uint(-shift)) & 31
		}
		out[i] = pubIDAlphabet[v]
	}
	return string(out)
}

// parseAppxIdentity: имя пакета, издатель, первое приложение и название из AppxManifest.xml.
func parseAppxIdentity(r io.Reader) (name, publisher, appID, display string) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	inProps := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "Identity":
				if name == "" {
					name, publisher = attr(t, "Name"), attr(t, "Publisher")
				}
			case "Properties":
				inProps = true
			case "DisplayName":
				if inProps && display == "" {
					var s string
					if dec.DecodeElement(&s, &t) == nil {
						display = strings.TrimSpace(s)
					}
				}
			case "Application":
				if appID == "" {
					appID = attr(t, "Id")
				}
			}
		case xml.EndElement:
			if t.Name.Local == "Properties" {
				inProps = false
			}
		}
	}
	return
}
