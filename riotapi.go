package main

// Локальный API Riot Client — то же, что нажать «Играть» в самом клиенте.
//
// Ярлык Riot (RiotClientServices.exe --launch-product=valorant --launch-patchline=live) иногда только
// открывает клиент на странице игры и саму игру не запускает. Кнопка «Играть» в клиенте отправляет запрос
// POST /product-launcher/v1/products/<игра>/patchlines/<ветка> в его локальный веб-сервер.
// Адрес и пароль сервера клиент пишет в lockfile: «Riot Client:pid:порт:пароль:https».

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// errRiotNotReady — клиент ещё не запущен или его сервер ещё не поднялся.
var errRiotNotReady = errors.New("Riot Client ещё не готов")

// riotAPIError — клиент ответил, но не запустил игру (обычно ещё не выполнен вход в аккаунт).
type riotAPIError struct{ status int }

func (e *riotAPIError) Error() string { return fmt.Sprintf("Riot Client ответил %d", e.status) }

type riotLock struct{ port, password, proto string }

// readRiotLockfile — порт и пароль локального сервера Riot Client.
func readRiotLockfile() (riotLock, bool) {
	for _, p := range expand(`%LOCALAPPDATA%\Riot Games\Riot Client\Config\lockfile`) {
		raw, err := os.ReadFile(p)
		if err != nil || len(raw) > 4096 {
			continue
		}
		parts := strings.Split(strings.TrimSpace(string(raw)), ":")
		if len(parts) < 5 {
			continue
		}
		n := len(parts)
		l := riotLock{port: parts[n-3], password: parts[n-2], proto: strings.ToLower(parts[n-1])}
		if port, err := strconv.Atoi(l.port); err != nil || port <= 0 || port > 65535 || l.password == "" {
			continue
		}
		if l.proto != "https" && l.proto != "http" {
			continue
		}
		return l, true
	}
	return riotLock{}, false
}

// Только 127.0.0.1: у сервера клиента собственный сертификат Riot, системе он неизвестен,
// поэтому проверка сертификата отключена, а соединиться можно лишь с этим компьютером.
var riotHTTP = &http.Client{
	Timeout: 6 * time.Second,
	Transport: &http.Transport{
		Proxy:           nil,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // локальный сервер Riot Client
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil || host != "127.0.0.1" {
				return nil, errors.New("разрешён только 127.0.0.1")
			}
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, network, addr)
		},
	},
}

// riotLaunchViaAPI просит запущенный Riot Client начать игру. nil — клиент принял команду.
func riotLaunchViaAPI(product, patchline string) error {
	l, ok := readRiotLockfile()
	if !ok {
		return errRiotNotReady
	}
	if patchline == "" {
		patchline = "live"
	}
	u := fmt.Sprintf("%s://127.0.0.1:%s/product-launcher/v1/products/%s/patchlines/%s",
		l.proto, l.port, url.PathEscape(product), url.PathEscape(patchline))
	req, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth("riot", l.password)
	req.Header.Set("Accept", "application/json")
	resp, err := riotHTTP.Do(req)
	if err != nil {
		return errRiotNotReady
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errRiotNotReady // старый lockfile: клиент перезапустился, пароль сменился
	}
	return &riotAPIError{status: resp.StatusCode}
}
