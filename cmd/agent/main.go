// lynx-agent: coletor leve. Lê /proc, portas, conexões e logs e envia ao servidor.
// Sem dependências externas; binário estático de ~5 MB.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"lynx/internal/model"
)

var version = "dev"

func archName() string { return runtime.GOARCH }

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	server := flag.String("server", env("AGENT_SERVER", ""), "URL do servidor, ex.: http://monitor.lan:8080")
	token := flag.String("token", env("AGENT_TOKEN", ""), "token de ingestão")
	name := flag.String("name", env("AGENT_NAME", ""), "nome exibido (padrão: hostname)")
	interval := flag.Int("interval", atoi(env("AGENT_INTERVAL", "10"), 10), "segundos entre envios")
	once := flag.Bool("once", false, "coleta uma vez, imprime o JSON e sai (depuração)")
	showVer := flag.Bool("version", false, "mostra a versão")
	flag.Parse()

	if *showVer {
		fmt.Println("lynx-agent", version)
		return
	}
	debug.SetGCPercent(50)
	debug.SetMemoryLimit(48 << 20)

	if *name == "" {
		h, _ := os.Hostname()
		*name = h
	}
	if *interval < 2 {
		*interval = 2
	}
	var extraLogs, extraProbes []string
	if v := os.Getenv("AGENT_LOGS"); v != "" {
		extraLogs = strings.Split(v, ",")
	}
	if v := os.Getenv("AGENT_PROBES"); v != "" {
		extraProbes = strings.Split(v, ",")
	}
	all := os.Getenv("AGENT_LOG_ALL") == "1"
	hideCmdline = os.Getenv("AGENT_HIDE_CMDLINE") == "1"

	col := newCollector(*name)
	col.baseline()
	time.Sleep(1500 * time.Millisecond)

	if *once {
		s := col.fast()
		s.Name, s.Version, s.Interval, s.Slow = *name, version, *interval, true
		s.Listen, s.Conns, s.Probes = col.slow(extraProbes)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(s)
		return
	}
	if *server == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "defina AGENT_SERVER e AGENT_TOKEN (ou -server/-token)")
		os.Exit(2)
	}
	*server = strings.TrimRight(*server, "/")

	lg := newLogger(all, extraLogs)
	lg.start()

	cl := &http.Client{Timeout: 8 * time.Second}
	tick := time.NewTicker(time.Duration(*interval) * time.Second)
	defer tick.Stop()

	var pending []model.LogLine
	var lastSlow time.Time
	var lastErr time.Time
	droppedTotal := 0

	for {
		s := col.fast()
		s.Name, s.Version, s.Interval = *name, version, *interval
		if time.Since(lastSlow) >= 30*time.Second {
			s.Listen, s.Conns, s.Probes = col.slow(extraProbes)
			s.Slow = true
			lastSlow = time.Now()
		}
		lines, dr := lg.poll()
		droppedTotal += dr
		pending = append(pending, lines...)
		if len(pending) > 500 {
			droppedTotal += len(pending) - 500
			pending = pending[len(pending)-500:]
		}
		s.Logs, s.LogsDropped = pending, droppedTotal

		if err := post(cl, *server, *token, s); err != nil {
			if time.Since(lastErr) > time.Minute {
				fmt.Fprintln(os.Stderr, "falha ao enviar:", err)
				lastErr = time.Now()
			}
		} else {
			pending, droppedTotal = nil, 0
		}
		<-tick.C
	}
}

func atoi(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func post(cl *http.Client, server, token string, s *model.Snapshot) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	req, _ := http.NewRequest("POST", server+"/api/ingest", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "lynx-agent/"+version)
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("servidor respondeu HTTP %d", resp.StatusCode)
	}
	return nil
}
