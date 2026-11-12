// Package model define o contrato JSON entre o agente e o servidor.
package model

type HostInfo struct {
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	Kernel   string   `json:"kernel"`
	Arch     string   `json:"arch"`
	CPUModel string   `json:"cpu_model"`
	Cores    int      `json:"cores"`
	Uptime   int64    `json:"uptime"`
	IPs      []string `json:"ips"`
}

// Core guarda o uso de um núcleo em % (user+nice, system+irq, iowait).
type Core struct {
	U float64 `json:"u"`
	S float64 `json:"s"`
	W float64 `json:"w"`
}

type CPU struct {
	Total  float64 `json:"total"`
	User   float64 `json:"user"`
	System float64 `json:"system"`
	IOWait float64 `json:"iowait"`
	Steal  float64 `json:"steal"`
	Cores  []Core  `json:"cores"`
}

type Mem struct {
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	Buffers   uint64 `json:"buffers"`
	Cache     uint64 `json:"cache"`
	Free      uint64 `json:"free"`
	Available uint64 `json:"available"`
	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`
}

type Disk struct {
	Mount    string  `json:"mount"`
	Dev      string  `json:"dev"`
	FS       string  `json:"fs"`
	Total    uint64  `json:"total"`
	Used     uint64  `json:"used"`
	Avail    uint64  `json:"avail"`
	Pct      float64 `json:"pct"`
	InodePct float64 `json:"inode_pct"`
}

type DiskIO struct {
	ReadBps  uint64 `json:"read_bps"`
	WriteBps uint64 `json:"write_bps"`
}

type Iface struct {
	Name  string `json:"name"`
	RxBps uint64 `json:"rx_bps"`
	TxBps uint64 `json:"tx_bps"`
}

type Net struct {
	RxBps  uint64  `json:"rx_bps"`
	TxBps  uint64  `json:"tx_bps"`
	Ifaces []Iface `json:"ifaces"`
}

type Tasks struct {
	Total    int `json:"total"`
	Running  int `json:"running"`
	Sleeping int `json:"sleeping"`
	Zombie   int `json:"zombie"`
	Threads  int `json:"threads"`
}

type Proc struct {
	PID     int     `json:"pid"`
	User    string  `json:"user"`
	Name    string  `json:"name"`
	Cmd     string  `json:"cmd"`
	State   string  `json:"state"`
	CPU     float64 `json:"cpu"` // % de um núcleo (como top/htop)
	Mem     float64 `json:"mem"` // % da RAM total
	RSS     uint64  `json:"rss"`
	Threads int     `json:"threads"`
}

// Group agrega processos de mesmo nome (ex.: 30 workers do postgres).
type Group struct {
	Name  string  `json:"name"`
	RSS   uint64  `json:"rss"`
	CPU   float64 `json:"cpu"`
	Count int     `json:"count"`
}

type Listener struct {
	Proto   string `json:"proto"`
	Addr    string `json:"addr"` // "*" = todas as interfaces
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Proc    string `json:"proc"`
	Service string `json:"service"`
	Exposed bool   `json:"exposed"`
}

type Conn struct {
	Dir     string `json:"dir"` // "in" (cliente conectou aqui) | "out" (esta máquina conectou lá)
	Proc    string `json:"proc"`
	Remote  string `json:"remote"`
	RPort   int    `json:"rport"`
	LPort   int    `json:"lport"`
	Service string `json:"service"`
	Count   int    `json:"count"`
}

type Probe struct {
	URL    string `json:"url"`
	Port   int    `json:"port"`
	Proc   string `json:"proc"`
	Status int    `json:"status"`
	Ms     int    `json:"ms"`
	Err    string `json:"err,omitempty"`
}

type LogLine struct {
	T     int64  `json:"t"` // unix ms
	Src   string `json:"src"`
	Level string `json:"level"` // crit | error | warn | info
	Msg   string `json:"msg"`
}

// Snapshot é o que o agente envia a cada ciclo.
// Slow=true indica que Listen/Conns/Probes foram atualizados neste envio;
// caso contrário o servidor reaproveita os últimos valores conhecidos.
type Snapshot struct {
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	Interval    int        `json:"interval"`
	TS          int64      `json:"ts"`
	Slow        bool       `json:"slow"`
	Host        HostInfo   `json:"host"`
	CPU         CPU        `json:"cpu"`
	Load        [3]float64 `json:"load"`
	Mem         Mem        `json:"mem"`
	Disks       []Disk     `json:"disks"`
	DiskIO      DiskIO     `json:"diskio"`
	Net         Net        `json:"net"`
	Tasks       Tasks      `json:"tasks"`
	Procs       []Proc     `json:"procs"`
	Groups      []Group    `json:"groups"`
	Listen      []Listener `json:"listen,omitempty"`
	Conns       []Conn     `json:"conns,omitempty"`
	Probes      []Probe    `json:"probes,omitempty"`
	Logs        []LogLine  `json:"logs,omitempty"`
	LogsDropped int        `json:"logs_dropped,omitempty"`
}
