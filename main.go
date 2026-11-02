// Zero Launcher — личный лаунчер игр: одна кнопка открывает нужные лаунчеры и запускает игру.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(argv []string) int {
	fs := flag.NewFlagSet("ZeroLauncher", flag.ContinueOnError)
	launch := fs.String("launch", "", "сразу запустить игру без окна (для ярлыков)")
	skip := fs.Bool("skip-missing", false, "пропускать лаунчеры без пути")
	noWindow := fs.Bool("no-window", false, "только сервер, адрес печатается в консоль")
	port := fs.Int("port", 0, "порт (0 — любой свободный)")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	setupLogging()
	if n := migrateLegacy(appDir(), legacyDir()); n {
		logger.Printf("Библиотека перенесена из %s", legacyDir())
	}
	st, err := NewStore("")
	if err != nil {
		messageBox("Не удалось создать папку настроек: "+err.Error(), true)
		return 1
	}

	if *launch != "" {
		return runHeadless(st, *launch, *skip)
	}

	if !*noWindow {
		if url := existingInstance(); url != "" {
			openAppWindow(url, filepath.Join(appDir(), "window"))
			return 0
		}
	}

	detectMissing(st, false) // пути лаунчеров: реестр и типовые папки, обычно доли секунды

	app := NewApp(st, nil, nil)
	if err := app.Listen(*port); err != nil {
		messageBox("Не удалось запустить Zero Launcher: "+err.Error(), true)
		return 1
	}
	logger.Printf("Zero Launcher %s запущен на порту %d", appVersion, app.Port)
	writeInstance(app)
	defer os.Remove(instanceFile())
	app.StartArt()

	if *noWindow {
		fmt.Println(app.URL())
		select {}
	}
	cmd := openAppWindow(app.URL(), filepath.Join(appDir(), "window"))
	waitUntilClosed(app, cmd)
	app.Shutdown()
	return 0
}

// runHeadless — запуск с ярлыка на рабочем столе: без окна, ошибки показываются сообщением.
func runHeadless(st *Store, gameID string, skip bool) int {
	detectMissing(st, false)
	job := NewLaunchJob(st, gameID, skip, NewProbe(nil), sysStarter{})
	job.Run()
	v := job.View()
	if v.State != "done" {
		msg := "Не удалось запустить игру"
		if v.Error != nil && v.Error.Message != "" {
			msg = v.Error.Message
		}
		messageBox(msg+"\n\nОткройте Zero Launcher, чтобы указать пути.", true)
		return 1
	}
	return 0
}

// ------------------------------------------------------------------ один экземпляр

type instanceInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func instanceFile() string { return filepath.Join(appDir(), "instance.json") }

func writeInstance(app *App) {
	raw, _ := json.Marshal(instanceInfo{app.Port, app.Token, os.Getpid()})
	_ = os.WriteFile(instanceFile(), raw, 0o600)
}

// existingInstance: если Zero Launcher уже открыт — вернуть его адрес, чтобы не запускать второй сервер.
func existingInstance() string {
	raw, err := os.ReadFile(instanceFile())
	if err != nil {
		return ""
	}
	var info instanceInfo
	if json.Unmarshal(raw, &info) != nil || info.Port == 0 {
		return ""
	}
	c := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Post(fmt.Sprintf("http://127.0.0.1:%d/api/ping?t=%s", info.Port, info.Token), "application/json", nil)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", info.Port, info.Token)
}

// waitUntilClosed ждёт закрытия окна: окно шлёт «пинг» и «пока», процесс браузера — запасной сигнал.
func waitUntilClosed(app *App, cmd *exec.Cmd) {
	started := time.Now()
	exited := make(chan struct{})
	if cmd != nil {
		go func() { _ = cmd.Wait(); close(exited) }()
	}
	procDone := func() bool {
		if cmd == nil {
			return false
		}
		select {
		case <-exited:
			return true
		default:
			return false
		}
	}
	for {
		time.Sleep(500 * time.Millisecond)
		last, bye := app.pingState()
		if !bye.IsZero() && time.Since(bye) > 4*time.Second { // окно закрыли, а не перезагрузили
			return
		}
		if procDone() && time.Since(started) > 8*time.Second && time.Since(last) > 20*time.Second {
			return
		}
		if time.Since(last) > 180*time.Second && time.Since(started) > 60*time.Second { // окно потерялось
			return
		}
	}
}
