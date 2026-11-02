package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const isWindows = runtime.GOOS == "windows"
const appVersion = "2.0.2"

const bom = "\xef\xbb\xbf"

var logger = log.New(os.Stderr, "", log.LstdFlags)

// appDir — папка с настройками, обложками и журналом (%APPDATA%\ZeroLauncher).
func appDir() string {
	if v := os.Getenv("ZERO_HOME"); v != "" {
		return v
	}
	if isWindows {
		base := os.Getenv("APPDATA")
		if base == "" {
			base, _ = os.UserHomeDir()
		}
		return filepath.Join(base, "ZeroLauncher")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "zero-launcher")
}

// legacyDir — папка первой версии лаунчера (Ignite на Python).
func legacyDir() string {
	if isWindows {
		base := os.Getenv("APPDATA")
		if base == "" {
			base, _ = os.UserHomeDir()
		}
		return filepath.Join(base, "Ignite")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ignite")
}

// migrateLegacy переносит библиотеку, обложки и иконки из старой версии, если новой ещё нет.
func migrateLegacy(newDir, oldDir string) bool {
	oldCfg := filepath.Join(oldDir, "config.json")
	if oldDir == "" || isFile(filepath.Join(newDir, "config.json")) || !isFile(oldCfg) {
		return false
	}
	raw, err := os.ReadFile(oldCfg)
	if err != nil {
		return false
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return false
	}
	text := string(raw)
	for _, sub := range []string{"covers", "icons"} {
		from, to := filepath.Join(oldDir, sub), filepath.Join(newDir, sub)
		if entries, err := os.ReadDir(from); err == nil {
			_ = os.MkdirAll(to, 0o755)
			for _, e := range entries {
				if !e.IsDir() {
					_ = copyFile(filepath.Join(from, e.Name()), filepath.Join(to, e.Name()))
				}
			}
		}
		// пути в JSON записаны с экранированными «\»
		a, _ := json.Marshal(from)
		b, _ := json.Marshal(to)
		text = strings.ReplaceAll(text, strings.Trim(string(a), `"`), strings.Trim(string(b), `"`))
	}
	return os.WriteFile(filepath.Join(newDir, "config.json"), []byte(text), 0o644) == nil
}

func setupLogging() {
	dir := appDir()
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "zero-launcher.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 1_000_000 {
		_ = os.Rename(p, p+".old")
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		logger.SetOutput(f)
	}
}

func newID(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// ------------------------------------------------------------------ пути

var envRe = regexp.MustCompile(`%([^%]+)%`)

var envDefaults = map[string]string{
	"programfiles":      `C:\Program Files`,
	"programfiles(x86)": `C:\Program Files (x86)`,
	"programdata":       `C:\ProgramData`,
	"systemdrive":       "C:",
}

func expandPercent(s string) (string, bool) {
	ok := true
	out := envRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		if v, found := os.LookupEnv(name); found && v != "" {
			return v
		}
		if isWindows {
			if v, found := envDefaults[strings.ToLower(name)]; found {
				return v
			}
		}
		ok = false
		return m
	})
	return out, ok
}

// expand раскрывает %ПЕРЕМЕННЫЕ% и {drive}. Возвращает список кандидатов.
func expand(pattern string) []string {
	s, ok := expandPercent(pattern)
	if !ok {
		return nil
	}
	var res []string
	if strings.Contains(s, "{drive}") {
		for _, d := range fixedDrives() {
			res = append(res, strings.ReplaceAll(s, "{drive}", d))
		}
	} else {
		res = []string{s}
	}
	if !isWindows {
		for i := range res {
			res[i] = strings.ReplaceAll(res[i], `\`, "/")
		}
	}
	return res
}

func norm(p string) string {
	if p == "" {
		return ""
	}
	return strings.ToLower(filepath.Clean(p))
}

// baseName понимает и «\», и «/».
func baseName(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func isFile(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func isDir(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

var uriRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]{1,40}://`)

func isURI(t string) bool {
	t = strings.TrimSpace(t)
	return uriRe.MatchString(t) || strings.HasPrefix(strings.ToLower(t), "shell:")
}

var exeRe = regexp.MustCompile(`(?i)^.+?\.exe\b`)

// exeFromCommand: из '"C:\X\app.exe" -- "%1"' достаёт путь к .exe.
func exeFromCommand(cmd string) string {
	cmd, _ = expandPercent(strings.TrimSpace(cmd))
	if cmd == "" {
		return ""
	}
	if strings.HasPrefix(cmd, `"`) {
		rest := cmd[1:]
		if i := strings.Index(rest, `"`); i >= 0 {
			return rest[:i]
		}
		return rest
	}
	if m := exeRe.FindString(cmd); m != "" {
		return m
	}
	return strings.Fields(cmd)[0]
}

// ------------------------------------------------------------------ значения из JSON

func toStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}

func toBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1"
	case float64:
		return x != 0
	}
	return false
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	}
	return 0, false
}

func toStrSlice(v any) []string {
	out := []string{}
	switch x := v.(type) {
	case []any:
		for _, it := range x {
			if s := strings.TrimSpace(toStr(it)); s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range x {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, s := range strings.Split(strings.ReplaceAll(x, ";", ","), ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func uniq(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
