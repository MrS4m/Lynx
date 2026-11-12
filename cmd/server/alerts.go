package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ---------- ciclo de vida dos alertas (chamar com s.mu travado) ----------

var sevName = map[string]string{"crit": "CRÍTICO", "warn": "AVISO", "info": "INFO"}

func (s *Store) push(msg string) {
	select {
	case s.notify <- Notice{msg}:
	default:
	}
}

func (s *Store) raise(host, rule, sev, msg string) {
	now := time.Now().Unix()
	key := host + "|" + rule
	if a := s.act[key]; a != nil {
		escalated := a.Sev == "warn" && sev == "crit"
		a.Sev, a.Msg, a.Last = sev, msg, now
		if escalated {
			s.push(fmt.Sprintf("[%s] %s — %s", sevName[sev], host, msg))
		}
		return
	}
	a := &Alert{ID: s.nextID, Host: host, Rule: rule, Sev: sev, Msg: msg, Since: now, Last: now, Active: true}
	s.nextID++
	s.alerts = append(s.alerts, a)
	s.act[key] = a
	s.trim()
	s.push(fmt.Sprintf("[%s] %s — %s", sevName[sev], host, msg))
}

func (s *Store) clear(host, rule string) {
	key := host + "|" + rule
	a := s.act[key]
	if a == nil {
		return
	}
	a.Active, a.Resolved = false, time.Now().Unix()
	delete(s.act, key)
	if a.Sev == "crit" {
		s.push(fmt.Sprintf("[OK] %s — recuperado: %s", host, a.Msg))
	}
}

// clearPrefix limpa alertas com prefixo cuja chave não está em keep.
func (s *Store) clearPrefix(host, prefix string, keep map[string]bool) {
	for _, a := range s.act {
		if a.Host == host && strings.HasPrefix(a.Rule, prefix) && !keep[a.Rule] {
			s.clear(host, a.Rule)
		}
	}
}

func (s *Store) event(host, sev, msg string) {
	now := time.Now().Unix()
	s.alerts = append(s.alerts, &Alert{ID: s.nextID, Host: host, Rule: "event", Sev: sev, Msg: msg, Since: now, Last: now, Resolved: now})
	s.nextID++
	s.trim()
}

func (s *Store) trim() {
	if len(s.alerts) <= alertKeep {
		return
	}
	over := len(s.alerts) - alertKeep
	kept := make([]*Alert, 0, alertKeep)
	for _, a := range s.alerts {
		if over > 0 && !a.Active {
			over--
			continue
		}
		kept = append(kept, a)
	}
	s.alerts = kept
}

// grade aplica limiares com histerese de 3 pontos para evitar alertas que piscam.
func (s *Store) grade(host, rule string, v, warn, crit float64, msg string) {
	s.gradeH(host, rule, v, warn, crit, 3, msg)
}

// gradeH é o grade com histerese configurável (quanto o valor precisa cair
// abaixo do limite para o alerta ser considerado resolvido).
func (s *Store) gradeH(host, rule string, v, warn, crit, hysteresis float64, msg string) {
	hys := 0.0
	if s.act[host+"|"+rule] != nil {
		hys = hysteresis
	}
	switch {
	case v >= crit-hys:
		s.raise(host, rule, "crit", msg)
	case v >= warn-hys:
		s.raise(host, rule, "warn", msg)
	default:
		s.clear(host, rule)
	}
}

// ---------- regras ----------

func avgLast(pts []Point, n int, f func(Point) float64) float64 {
	if len(pts) == 0 {
		return 0
	}
	if n > len(pts) {
		n = len(pts)
	}
	sum := 0.0
	for _, p := range pts[len(pts)-n:] {
		sum += f(p)
	}
	return sum / float64(n)
}

func gb(b float64) string { return fmt.Sprintf("%.1f GB", b/1073741824) }

// linfit: regressão linear por mínimos quadrados. Retorna inclinação e R².
func linfit(xs, ys []float64) (slope, r2 float64) {
	n := float64(len(xs))
	if n < 3 {
		return 0, 0
	}
	var sx, sy, sxx, sxy, syy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
		sxx += xs[i] * xs[i]
		sxy += xs[i] * ys[i]
		syy += ys[i] * ys[i]
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, 0
	}
	slope = (n*sxy - sx*sy) / den
	vy := n*syy - sy*sy
	if vy <= 0 {
		return slope, 0
	}
	r := (n*sxy - sx*sy) / math.Sqrt(den*vy)
	return slope, r * r
}

func fmtHours(h float64) string {
	if h < 1 {
		return fmt.Sprintf("%.0f min", h*60)
	}
	if h < 48 {
		return fmt.Sprintf("%.0f h", h)
	}
	return fmt.Sprintf("%.0f dias", h/24)
}

func (s *Store) evaluate(h *Host) {
	snap := h.Latest
	if snap == nil {
		return
	}
	n := h.Name
	s.clear(n, "offline")
	cores := float64(snap.Host.Cores)
	if cores < 1 {
		cores = 1
	}

	// CPU (média das últimas 3 amostras)
	cpu := avgLast(h.Raw, 3, func(p Point) float64 { return p.CPU })
	msg := fmt.Sprintf("CPU em %.0f%%", cpu)
	if len(snap.Procs) > 0 && cpu >= 70 {
		top := snap.Procs[0]
		msg += fmt.Sprintf(" — maior consumo: %s (%.0f%% de um núcleo)", top.Name, top.CPU)
	}
	s.grade(n, "cpu", cpu, 75, 90, msg)

	// Memória
	mem := avgLast(h.Raw, 3, func(p Point) float64 { return p.Mem })
	msg = fmt.Sprintf("Memória em %.0f%% (%s de %s)", mem, gb(float64(snap.Mem.Total-snap.Mem.Available)), gb(float64(snap.Mem.Total)))
	if len(snap.Groups) > 0 && mem >= 75 {
		msg += fmt.Sprintf(" — maior consumo: %s (%s)", snap.Groups[0].Name, gb(float64(snap.Groups[0].RSS)))
	}
	s.grade(n, "mem", mem, 80, 92, msg)

	// Swap
	if snap.Mem.SwapTotal > 0 {
		sw := pct(float64(snap.Mem.SwapUsed), float64(snap.Mem.SwapTotal))
		s.grade(n, "swap", sw, 40, 75, fmt.Sprintf("Swap em %.0f%% (%s)", sw, gb(float64(snap.Mem.SwapUsed))))
	} else {
		s.clear(n, "swap")
	}

	// Load average por núcleo (5 min)
	perCore := snap.Load[1] / cores
	s.gradeH(n, "load", perCore*100, 150, 300, 30,
		fmt.Sprintf("Load average de 5 min = %.2f em %d núcleos (%.1f por núcleo)", snap.Load[1], int(cores), perCore))

	// Discos, inodes e previsão de enchimento
	keep := map[string]bool{}
	keepIn := map[string]bool{}
	keepEta := map[string]bool{}
	for _, d := range snap.Disks {
		rule := "disk:" + d.Mount
		keep[rule] = true
		s.grade(n, rule, d.Pct, 80, 92, fmt.Sprintf("Disco %s em %.0f%% (%s livres)", d.Mount, d.Pct, gb(float64(d.Avail))))
		if d.InodePct > 0 {
			ir := "inode:" + d.Mount
			keepIn[ir] = true
			s.grade(n, ir, d.InodePct, 85, 95, fmt.Sprintf("Inodes de %s em %.0f%%", d.Mount, d.InodePct))
		}
		if hrs, rate, ok := diskETA(h.Disk[d.Mount]); ok && hrs < 48 {
			er := "eta:" + d.Mount
			keepEta[er] = true
			sev := "warn"
			if hrs < 6 {
				sev = "crit"
			}
			s.raise(n, er, sev, fmt.Sprintf("Disco %s deve encher em ~%s no ritmo atual (+%s/h)", d.Mount, fmtHours(hrs), gb(rate)))
		}
	}
	s.clearPrefix(n, "disk:", keep)
	s.clearPrefix(n, "inode:", keepIn)
	s.clearPrefix(n, "eta:", keepEta)

	// Tendência de memória (possível vazamento)
	if hrs, rate, ok := memETA(h.Raw); ok && hrs < 3 && mem < 92 {
		s.raise(n, "memtrend", "warn", fmt.Sprintf("Memória crescendo ~%.1f%%/h — chega a 95%% em ~%s (possível vazamento)", rate, fmtHours(hrs)))
	} else {
		s.clear(n, "memtrend")
	}

	// Testes web
	keepW := map[string]bool{}
	for _, p := range snap.Probes {
		rule := "web:" + p.URL
		switch {
		case p.Status >= 500:
			keepW[rule] = true
			s.raise(n, rule, "crit", fmt.Sprintf("%s respondeu HTTP %d", p.URL, p.Status))
		case p.Err == "timeout":
			keepW[rule] = true
			s.raise(n, rule, "warn", fmt.Sprintf("%s não respondeu em 2 s", p.URL))
		case p.Err == "recusada":
			keepW[rule] = true
			s.raise(n, rule, "crit", fmt.Sprintf("%s recusou a conexão", p.URL))
		}
	}
	s.clearPrefix(n, "web:", keepW)

	// Serviços que pararam de escutar
	if snap.Slow {
		cur := map[string]bool{}
		for _, l := range snap.Listen {
			cur[fmt.Sprintf("tcp/%d", l.Port)] = true
		}
		keepS := map[string]bool{}
		for key, k := range h.Known {
			if cur[key] {
				continue
			}
			rule := "svc:" + key
			keepS[rule] = true
			label := k.Service
			if label == "" {
				label = k.Proc
			}
			s.raise(n, rule, "crit", fmt.Sprintf("Porta %d/tcp (%s) parou de escutar", k.Port, label))
		}
		s.clearPrefix(n, "svc:", keepS)
	}

	// Logs
	cutoff := time.Now().Add(-5 * time.Minute).UnixMilli()
	errs, lastCrit := 0, ""
	critCut := time.Now().Add(-15 * time.Minute).UnixMilli()
	for i := len(h.Logs) - 1; i >= 0; i-- {
		l := h.Logs[i]
		if l.T >= cutoff && (l.Level == "error" || l.Level == "crit") {
			errs++
		}
		if l.T >= critCut && l.Level == "crit" && lastCrit == "" {
			lastCrit = l.Msg
		}
		if l.T < critCut {
			break
		}
	}
	s.grade(n, "logerr", float64(errs), 20, 100, fmt.Sprintf("%d mensagens de erro nos logs nos últimos 5 min", errs))
	if lastCrit != "" {
		if len(lastCrit) > 110 {
			lastCrit = lastCrit[:110] + "…"
		}
		s.raise(n, "logcrit", "crit", "Log crítico: "+lastCrit)
	} else {
		s.clear(n, "logcrit")
	}
}

func diskETA(pts []DPoint) (hours, gbPerHour float64, ok bool) {
	if len(pts) < 12 || pts[len(pts)-1].T-pts[0].T < 3600 {
		return
	}
	if len(pts) > 60 {
		pts = pts[len(pts)-60:]
	}
	xs, ys := make([]float64, len(pts)), make([]float64, len(pts))
	for i, p := range pts {
		xs[i], ys[i] = float64(p.T-pts[0].T), p.Used
	}
	slope, r2 := linfit(xs, ys)
	last := pts[len(pts)-1]
	free := last.Total - last.Used
	if slope <= 0 || r2 < 0.7 || free <= 0 {
		return
	}
	hours = free / slope / 3600
	return hours, slope * 3600, true
}

func memETA(raw []Point) (hours, ratePerHour float64, ok bool) {
	if len(raw) < 90 || raw[len(raw)-1].T-raw[0].T < 1200 {
		return
	}
	xs, ys := make([]float64, len(raw)), make([]float64, len(raw))
	for i, p := range raw {
		xs[i], ys[i] = float64(p.T-raw[0].T), p.Mem
	}
	slope, r2 := linfit(xs, ys)
	if slope <= 0 || r2 < 0.8 {
		return
	}
	cur := raw[len(raw)-1].Mem
	if cur >= 95 {
		return
	}
	return (95 - cur) / slope / 3600, slope * 3600, true
}

// ---------- agente sem resposta ----------

func (s *Store) CheckOffline() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	for _, h := range s.hosts {
		if now-h.LastSeen > offlineAfter(h) {
			ago := time.Duration(now-h.LastSeen) * time.Second
			s.raise(h.Name, "offline", "crit", fmt.Sprintf("Agente sem resposta há %s", ago.Round(time.Second)))
			s.dirty = true
		}
	}
}

func offlineAfter(h *Host) int64 {
	iv := int64(h.Interval)
	if iv < 5 {
		iv = 10
	}
	if v := iv * 3; v > 30 {
		return v
	}
	return 30
}

// ---------- webhook (Slack, Discord, Mattermost, Teams via conector) ----------

func runWebhook(s *Store, url string) {
	cl := &http.Client{Timeout: 8 * time.Second}
	for n := range s.notify {
		if url == "" {
			continue
		}
		body, _ := json.Marshal(map[string]string{"text": n.Text, "content": n.Text})
		resp, err := cl.Post(url, "application/json", bytes.NewReader(body))
		if err == nil {
			resp.Body.Close()
		}
	}
}

// ---------- consultas ----------

func (s *Store) sortedAlerts(activeOnly bool) []*Alert {
	out := make([]*Alert, 0, len(s.alerts))
	for _, a := range s.alerts {
		if activeOnly && !a.Active {
			continue
		}
		out = append(out, a)
	}
	rank := map[string]int{"crit": 0, "warn": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		if out[i].Active && rank[out[i].Sev] != rank[out[j].Sev] {
			return rank[out[i].Sev] < rank[out[j].Sev]
		}
		return out[i].Last > out[j].Last
	})
	return out
}
