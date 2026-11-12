package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"lynx/internal/model"
)

const (
	rawKeep   = 3600  // histórico bruto: 1 h
	minKeep   = 86400 // histórico por minuto: 24 h
	diskStep  = 300   // amostra de disco a cada 5 min
	logKeep   = 2000  // linhas de log por servidor
	alertKeep = 1000  // alertas/eventos guardados
	knownDays = 7 * 86400
)

// Point é uma amostra de série temporal (chaves curtas para o JSON ficar pequeno).
type Point struct {
	T    int64   `json:"t"`
	CPU  float64 `json:"cpu"`
	IOW  float64 `json:"iow"`
	Mem  float64 `json:"mem"`
	Swap float64 `json:"swap"`
	L1   float64 `json:"l1"`
	L5   float64 `json:"l5"`
	L15  float64 `json:"l15"`
	Rx   float64 `json:"rx"`
	Tx   float64 `json:"tx"`
	DR   float64 `json:"dr"`
	DW   float64 `json:"dw"`
}

type DPoint struct {
	T     int64   `json:"t"`
	Used  float64 `json:"u"`
	Total float64 `json:"tot"`
}

type Known struct {
	Port    int    `json:"port"`
	Proto   string `json:"proto"`
	Service string `json:"service"`
	Proc    string `json:"proc"`
	Seen    int64  `json:"seen"`
}

type Host struct {
	Name      string              `json:"name"`
	FirstSeen int64               `json:"first_seen"`
	LastSeen  int64               `json:"last_seen"`
	Interval  int                 `json:"interval"`
	RemoteIP  string              `json:"remote_ip"`
	Latest    *model.Snapshot     `json:"latest"`
	Raw       []Point             `json:"raw"`
	Min       []Point             `json:"min"`
	Disk      map[string][]DPoint `json:"disk"`
	Logs      []model.LogLine     `json:"logs"`
	Known     map[string]*Known   `json:"known"`

	accMinute int64
	accN      int
	acc       Point
	lastDisk  int64
}

type Alert struct {
	ID       int    `json:"id"`
	Host     string `json:"host"`
	Rule     string `json:"rule"`
	Sev      string `json:"sev"` // crit | warn | info
	Msg      string `json:"msg"`
	Since    int64  `json:"since"`
	Last     int64  `json:"last"`
	Resolved int64  `json:"resolved"`
	Active   bool   `json:"active"`
}

type Store struct {
	mu     sync.RWMutex
	hosts  map[string]*Host
	alerts []*Alert
	act    map[string]*Alert // host|rule -> alerta ativo
	nextID int
	notify chan Notice
	dir    string
	dirty  bool
}

type Notice struct{ Text string }

func NewStore(dir string) *Store {
	return &Store{hosts: map[string]*Host{}, act: map[string]*Alert{}, nextID: 1, notify: make(chan Notice, 200), dir: dir}
}

type persisted struct {
	Hosts  map[string]*Host `json:"hosts"`
	Alerts []*Alert         `json:"alerts"`
	NextID int              `json:"next_id"`
}

func (s *Store) Load() error {
	b, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if err != nil {
		return err
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Hosts != nil {
		s.hosts = p.Hosts
	}
	s.alerts, s.nextID = p.Alerts, p.NextID
	if s.nextID == 0 {
		s.nextID = 1
	}
	for _, a := range s.alerts {
		if a.Active {
			s.act[a.Host+"|"+a.Rule] = a
		}
	}
	return nil
}

func (s *Store) Save() error {
	s.mu.RLock()
	b, err := json.Marshal(persisted{s.hosts, s.alerts, s.nextID})
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "state.json"))
}

func pct(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return a / b * 100
}

func r1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// Ingest registra um snapshot vindo do agente.
func (s *Store) Ingest(snap *model.Snapshot, remoteIP string) error {
	if snap.Name == "" || len(snap.Name) > 100 {
		return fmt.Errorf("nome inválido")
	}
	now := time.Now().Unix()
	ts := snap.TS
	if ts < now-7200 || ts > now+300 {
		ts = now
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	h := s.hosts[snap.Name]
	if h == nil {
		h = &Host{Name: snap.Name, FirstSeen: now, Disk: map[string][]DPoint{}, Known: map[string]*Known{}}
		s.hosts[snap.Name] = h
	}
	if h.Disk == nil {
		h.Disk = map[string][]DPoint{}
	}
	if h.Known == nil {
		h.Known = map[string]*Known{}
	}
	prev := h.Latest
	h.LastSeen, h.RemoteIP = now, remoteIP
	if snap.Interval > 0 {
		h.Interval = snap.Interval
	}

	// campos lentos: reaproveita o último valor quando o agente não enviou
	if !snap.Slow && prev != nil {
		snap.Listen, snap.Conns, snap.Probes = prev.Listen, prev.Conns, prev.Probes
	}

	// logs vão para o anel do servidor, não ficam no snapshot
	if len(snap.Logs) > 0 {
		h.Logs = append(h.Logs, snap.Logs...)
		if len(h.Logs) > logKeep {
			h.Logs = h.Logs[len(h.Logs)-logKeep:]
		}
	}
	snap.Logs = nil
	h.Latest = snap
	h.Latest.TS = ts

	// séries temporais
	m := snap.Mem
	p := Point{
		T:    ts,
		CPU:  r1(snap.CPU.Total),
		IOW:  r1(snap.CPU.IOWait),
		Mem:  r1(pct(float64(m.Total)-float64(m.Available), float64(m.Total))),
		Swap: r1(pct(float64(m.SwapUsed), float64(m.SwapTotal))),
		L1:   snap.Load[0], L5: snap.Load[1], L15: snap.Load[2],
		Rx: float64(snap.Net.RxBps), Tx: float64(snap.Net.TxBps),
		DR: float64(snap.DiskIO.ReadBps), DW: float64(snap.DiskIO.WriteBps),
	}
	h.Raw = append(h.Raw, p)
	cut := sort.Search(len(h.Raw), func(i int) bool { return h.Raw[i].T >= now-rawKeep })
	if cut > 0 {
		h.Raw = append([]Point(nil), h.Raw[cut:]...)
	}
	s.accumulate(h, p)

	// tendência de disco
	if ts-h.lastDisk >= diskStep {
		h.lastDisk = ts
		for _, d := range snap.Disks {
			arr := append(h.Disk[d.Mount], DPoint{T: ts, Used: float64(d.Used), Total: float64(d.Used + d.Avail)})
			c := sort.Search(len(arr), func(i int) bool { return arr[i].T >= now-minKeep })
			h.Disk[d.Mount] = append([]DPoint(nil), arr[c:]...)
		}
	}

	// portas conhecidas e novas portas expostas
	if snap.Slow {
		s.trackPorts(h, prev, snap, ts)
	}

	s.evaluate(h)
	s.dirty = true
	return nil
}

func (s *Store) accumulate(h *Host, p Point) {
	minute := p.T / 60
	if h.accN > 0 && h.accMinute != minute {
		n := float64(h.accN)
		a := h.acc
		avg := Point{T: h.accMinute * 60, CPU: r1(a.CPU / n), IOW: r1(a.IOW / n), Mem: r1(a.Mem / n), Swap: r1(a.Swap / n),
			L1: r1(a.L1 / n), L5: r1(a.L5 / n), L15: r1(a.L15 / n), Rx: a.Rx / n, Tx: a.Tx / n, DR: a.DR / n, DW: a.DW / n}
		h.Min = append(h.Min, avg)
		cut := sort.Search(len(h.Min), func(i int) bool { return h.Min[i].T >= time.Now().Unix()-minKeep })
		if cut > 0 {
			h.Min = append([]Point(nil), h.Min[cut:]...)
		}
		h.acc, h.accN = Point{}, 0
	}
	h.accMinute = minute
	h.accN++
	h.acc.CPU += p.CPU
	h.acc.IOW += p.IOW
	h.acc.Mem += p.Mem
	h.acc.Swap += p.Swap
	h.acc.L1 += p.L1
	h.acc.L5 += p.L5
	h.acc.L15 += p.L15
	h.acc.Rx += p.Rx
	h.acc.Tx += p.Tx
	h.acc.DR += p.DR
	h.acc.DW += p.DW
}

func (s *Store) trackPorts(h *Host, prev, snap *model.Snapshot, ts int64) {
	for _, l := range snap.Listen {
		if l.Proto != "tcp" || (l.Service == "" && l.Port >= 1024) {
			continue
		}
		key := fmt.Sprintf("tcp/%d", l.Port)
		k := h.Known[key]
		if k == nil {
			k = &Known{Port: l.Port, Proto: "tcp"}
			h.Known[key] = k
		}
		k.Service, k.Proc, k.Seen = l.Service, l.Proc, ts
	}
	for key, k := range h.Known {
		if ts-k.Seen > knownDays {
			delete(h.Known, key)
		}
	}
	// nova porta exposta (só depois de já ter conhecido o host)
	if prev != nil && len(prev.Listen) > 0 {
		old := map[string]bool{}
		for _, l := range prev.Listen {
			old[fmt.Sprintf("%s/%d", l.Proto, l.Port)] = true
		}
		for _, l := range snap.Listen {
			if l.Proto == "tcp" && l.Exposed && !old[fmt.Sprintf("tcp/%d", l.Port)] {
				name := l.Service
				if name == "" {
					name = l.Proc
				}
				s.event(h.Name, "info", fmt.Sprintf("Nova porta exposta: %d/tcp (%s)", l.Port, name))
			}
		}
	}
}

// Forget remove um host e seus alertas.
func (s *Store) Forget(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[name]; !ok {
		return false
	}
	delete(s.hosts, name)
	kept := s.alerts[:0]
	for _, a := range s.alerts {
		if a.Host != name {
			kept = append(kept, a)
		}
	}
	s.alerts = kept
	for k, a := range s.act {
		if a.Host == name {
			delete(s.act, k)
		}
	}
	s.dirty = true
	return true
}
