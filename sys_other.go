//go:build !windows

package main

// Заглушки для Linux/macOS: Zero Launcher рассчитан на Windows, здесь — только разработка и тесты.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func rageConnect(addr string) error { return nil }

func regGet(hive, key, value string) (string, bool) { return "", false }
func regSubkeys(hive, key string) []string          { return nil }

func fixedDrives() []string {
	if v := os.Getenv("ZERO_FAKE_DRIVES"); v != "" {
		var out []string
		for _, d := range strings.Split(v, string(os.PathListSeparator)) {
			if d != "" {
				out = append(out, d)
			}
		}
		return out
	}
	return nil
}

func snapshotProcs() ([]procInfo, error) {
	raw, err := exec.Command("ps", "-eo", "comm=").Output()
	if err != nil {
		return nil, err
	}
	var out []procInfo
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, procInfo{strings.ToLower(baseName(l)), ""})
		}
	}
	return out, nil
}

type sysStarter struct{}

func (sysStarter) Start(path, args, cwd string, admin bool) error {
	cmd := exec.Command(path, strings.Fields(args)...)
	if isDir(cwd) {
		cmd.Dir = cwd
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить %s: %v", baseName(path), err)
	}
	return cmd.Process.Release()
}

func (sysStarter) OpenURI(uri string) error { return exec.Command("xdg-open", uri).Start() }

func browseDialog(kind, title, initial string) (string, error) {
	return "", errors.New("окно выбора файла есть только в Windows")
}

func createShortcut(g *Game, iconExe string) (string, error) {
	return "", errors.New("ярлыки создаются только в Windows")
}

func openInExplorer(path string) error {
	if !isFile(path) && !isDir(path) {
		return errors.New("нет такого пути")
	}
	return exec.Command("xdg-open", path).Start()
}

func messageBox(text string, isError bool) { fmt.Fprintln(os.Stderr, "[Zero Launcher]", text) }

func openAppWindow(url, profile string) *exec.Cmd {
	for _, b := range []string{"/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/microsoft-edge"} {
		if isFile(b) {
			cmd := exec.Command(b, "--app="+url, "--user-data-dir="+profile)
			if cmd.Start() == nil {
				return cmd
			}
		}
	}
	_ = exec.Command("xdg-open", url).Start()
	return nil
}

func appxInstallLocation(family string) string { return "" }
