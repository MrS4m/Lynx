package main

import (
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"lynx/internal/model"
)

type Svc struct {
	Name    string `json:"name"`
	Port    int    `json:"port"`
	Exposed bool   `json:"exposed"`
}

type HostSummary struct {
	Name      string     `json:"name"`
	Status    string     `json:"status"` // ok | warn | crit | offline
	OS        string     `json:"os"`
	Kernel    string     `json:"kernel"`
	LastSeen  int64      `json:"last_seen"`
	Interval  int        `json:"interval"`
	Cores     int        `json:"cores"`
	CPU       float64    `json:"cpu"`
	Mem       float64    `json:"mem"`
	MemUsed   uint64     `json:"mem_used"`
	MemTotal  uint64     `json:"mem_total"`
	Swap      float64    `json:"swap"`
	Load      [3]float64 `json:"load"`
	Disk      float64    `json:"disk"`
	DiskMount string     `json:"disk_mount"`
	Uptime    int64      `json:"uptime"`
	Tasks     int        `json:"tasks"`
	Crit      int        `json:"crit"`
	Warn      int        `json:"warn"`
	Services  []Svc      `json:"services"`
	IPs       []string   `json:"ips"`
	Version   string     `json:"version"`
	Rx        uint64     `json:"rx"`
	Tx        uint64     `json:"tx"`
	Top       string     `json:"top"`
}

func (s *Store) summary(h *Host, now int64) HostSummary {
	hs := HostSummary{Name: h.Name, LastSeen: h.LastSeen, Interval: h.Interval}
	for _, a := range s.act {
		if a.Host != h.Name {
			continue
		}
		switch a.Sev {
		case "crit":
			hs.Crit++
		case "warn":
			hs.Warn++
		}
	}
	sn := h.Latest
	if sn != nil {
		hs.OS, hs.Kernel, hs.Cores, hs.Uptime, hs.IPs, hs.Version = sn.Host.OS, sn.Host.Kernel, sn.Host.Cores, sn.Host.Uptime, sn.Host.IPs, sn.Version
		hs.CPU = sn.CPU.Total
		hs.MemTotal = sn.Mem.Total
		if sn.Mem.Total > sn.Mem.Available {
			hs.MemUsed = sn.Mem.Total - sn.Mem.Available
		}
		hs.Mem = r1(pct(float64(hs.MemUsed), float64(sn.Mem.Total)))
		hs.Swap = r1(pct(float64(sn.Mem.SwapUsed), float64(sn.Mem.SwapTotal)))
		hs.Load, hs.Tasks = sn.Load, sn.Tasks.Total
		hs.Rx, hs.Tx = sn.Net.RxBps, sn.Net.TxBps
		for _, d := range sn.Disks {
			if d.Pct >= hs.Disk {
				hs.Disk, hs.DiskMount = d.Pct, d.Mount
			}
		}
		seen := map[int]bool{}
		for _, l := range sn.Listen {
			if l.Proto == "tcp" && l.Service != "" && !seen[l.Port] {
				seen[l.Port] = true
				hs.Services = append(hs.Services, Svc{l.Service, l.Port, l.Exposed})
			}
		}
		sort.Slice(hs.Services, func(i, j int) bool { return hs.Services[i].Port < hs.Services[j].Port })
		if len(sn.Groups) > 0 {
			hs.Top = sn.Groups[0].Name
		}
	}
	switch {
	case s.act[h.Name+"|offline"] != nil || now-h.LastSeen > offlineAfter(h):
		hs.Status = "offline"
	case hs.Crit > 0:
		hs.Status = "crit"
	case hs.Warn > 0:
		hs.Status = "warn"
	default:
		hs.Status = "ok"
	}
	return hs
}

var statusRank = map[string]int{"crit": 0, "offline": 1, "warn": 2, "ok": 3}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func (s *Store) handleOverview(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	s.mu.RLock()
	hosts := make([]HostSummary, 0, len(s.hosts))
	type app struct {
		Host string  `json:"host"`
		Name string  `json:"name"`
		RSS  uint64  `json:"rss"`
		CPU  float64 `json:"cpu"`
	}
	apps := []app{}
	for _, h := range s.hosts {
		hosts = append(hosts, s.summary(h, now))
		if h.Latest != nil && now-h.LastSeen <= offlineAfter(h) {
			for _, g := range h.Latest.Groups {
				apps = append(apps, app{h.Name, g.Name, g.RSS, g.CPU})
			}
		}
	}
	sort.Slice(hosts, func(i, j int) bool {
		if statusRank[hosts[i].Status] != statusRank[hosts[j].Status] {
			return statusRank[hosts[i].Status] < statusRank[hosts[j].Status]
		}
		return hosts[i].Name < hosts[j].Name
	})
	sort.Slice(apps, func(i, j int) bool { return apps[i].RSS > apps[j].RSS })
	if len(apps) > 8 {
		apps = apps[:8]
	}

	tot := map[string]any{}
	var online, offline, okN, warnN, critN int
	var cpuSum, memSum, worstDisk float64
	worstHost, worstMount := "", ""
	for _, h := range hosts {
		switch h.Status {
		case "offline":
			offline++
			continue
		case "ok":
			okN++
		case "warn":
			warnN++
		case "crit":
			critN++
		}
		online++
		cpuSum += h.CPU
		memSum += h.Mem
		if h.Disk >= worstDisk {
			worstDisk, worstHost, worstMount = h.Disk, h.Name, h.DiskMount
		}
	}
	avg := func(v float64) float64 {
		if online == 0 {
			return 0
		}
		return r1(v / float64(online))
	}
	var aCrit, aWarn int
	for _, a := range s.act {
		switch a.Sev {
		case "crit":
			aCrit++
		case "warn":
			aWarn++
		}
	}
	tot["total"], tot["online"], tot["offline"] = len(hosts), online, offline
	tot["ok"], tot["warn"], tot["crit"] = okN, warnN, critN
	tot["alerts_crit"], tot["alerts_warn"] = aCrit, aWarn
	tot["cpu_avg"], tot["mem_avg"] = avg(cpuSum), avg(memSum)
	tot["worst_disk"], tot["worst_disk_host"], tot["worst_disk_mount"] = worstDisk, worstHost, worstMount

	active := s.sortedAlerts(true)
	if len(active) > 30 {
		active = active[:30]
	}
	events := []*Alert{}
	for i := len(s.alerts) - 1; i >= 0 && len(events) < 10; i-- {
		if !s.alerts[i].Active {
			events = append(events, s.alerts[i])
		}
	}
	resp := map[string]any{"now": now, "hosts": hosts, "totals": tot, "apps": apps, "alerts": active, "events": events}
	writeJSON(w, resp)
	s.mu.RUnlock()
}

func (s *Store) handleHost(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	now := time.Now().Unix()
	s.mu.RLock()
	h := s.hosts[name]
	if h == nil {
		s.mu.RUnlock()
		http.Error(w, `{"error":"servidor não encontrado"}`, http.StatusNotFound)
		return
	}
	rng := r.URL.Query().Get("range")
	src := h.Raw
	if rng == "24h" {
		src = h.Min
	}
	pts := append([]Point{}, src...)
	alerts := []*Alert{}
	for _, a := range s.sortedAlerts(false) {
		if a.Host == name && len(alerts) < 40 {
			alerts = append(alerts, a)
		}
	}
	nl := len(h.Logs)
	if nl > 200 {
		nl = 200
	}
	logs := append([]model.LogLine{}, h.Logs[len(h.Logs)-nl:]...)
	var etas = map[string]float64{}
	for mount, d := range h.Disk {
		if hrs, _, ok := diskETA(d); ok {
			etas[mount] = hrs
		}
	}
	resp := map[string]any{"summary": s.summary(h, now), "snap": h.Latest, "points": pts, "range": rng,
		"alerts": alerts, "logs": logs, "disk_eta": etas, "first_seen": h.FirstSeen, "remote_ip": h.RemoteIP}
	writeJSON(w, resp)
	s.mu.RUnlock()
}

func (s *Store) handleAlerts(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	list := s.sortedAlerts(r.URL.Query().Get("state") == "active")
	if len(list) > 300 {
		list = list[:300]
	}
	writeJSON(w, list)
	s.mu.RUnlock()
}

type logOut struct {
	Host string `json:"host"`
	model.LogLine
}

func (s *Store) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	host, level, text := q.Get("host"), q.Get("level"), strings.ToLower(q.Get("q"))
	out := []logOut{}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	s.mu.RLock()
	for _, h := range s.hosts {
		if host != "" && h.Name != host {
			continue
		}
		for _, l := range h.Logs {
			if level == "error" && l.Level != "error" && l.Level != "crit" {
				continue
			}
			if level == "warn" && l.Level == "info" {
				continue
			}
			if text != "" && !strings.Contains(strings.ToLower(l.Msg+" "+l.Src), text) {
				continue
			}
			out = append(out, logOut{h.Name, l})
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].T > out[j].T })
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, out)
}

func (s *Store) handleForget(w http.ResponseWriter, r *http.Request) {
	if !s.Forget(r.PathValue("name")) {
		http.Error(w, `{"error":"servidor não encontrado"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// Dispensar um alerta; para "serviço parou de escutar" também esquece a porta.
func (s *Store) handleDismiss(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.alerts {
		if a.ID != id {
			continue
		}
		if strings.HasPrefix(a.Rule, "svc:") {
			if h := s.hosts[a.Host]; h != nil {
				delete(h.Known, strings.TrimPrefix(a.Rule, "svc:"))
			}
		}
		delete(s.act, a.Host+"|"+a.Rule)
		s.alerts = append(s.alerts[:i], s.alerts[i+1:]...)
		s.dirty = true
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	http.Error(w, `{"error":"alerta não encontrado"}`, http.StatusNotFound)
}

// ---------- ingestão ----------

func (a *App) handleIngest(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(auth), []byte(a.token)) != 1 {
		http.Error(w, "token inválido", http.StatusUnauthorized)
		return
	}
	var body io.Reader = http.MaxBytesReader(w, r.Body, 4<<20)
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(body)
		if err != nil {
			http.Error(w, "gzip inválido", http.StatusBadRequest)
			return
		}
		defer zr.Close()
		body = io.LimitReader(zr, 16<<20)
	}
	var snap model.Snapshot
	if err := json.NewDecoder(body).Decode(&snap); err != nil {
		http.Error(w, "json inválido", http.StatusBadRequest)
		return
	}
	ip := r.RemoteAddr
	if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		ip = strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	if err := a.store.Ingest(&snap, ip); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- instalador e binários do agente ----------

var binName = regexp.MustCompile(`^lynx-agent-linux-(amd64|arm64)$`)

func (a *App) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "lynx-agent.ps1" { // agente Windows, embutido no binário do servidor
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(agentPS1)
		return
	}
	if !binName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(a.binDir, name)
	if _, err := os.Stat(p); err != nil {
		http.Error(w, "binário não disponível neste servidor", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, p)
}

func (a *App) handleInstall(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	base := scheme + "://" + r.Host
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Write([]byte(strings.ReplaceAll(installTmpl, "{{SERVER}}", base)))
}

func (a *App) handleInstallPS1(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	base := scheme + "://" + r.Host
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(strings.ReplaceAll(installPS1Tmpl, "{{SERVER}}", base)))
}
