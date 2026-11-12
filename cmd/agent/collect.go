package main

import (
	"bufio"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"lynx/internal/model"
)

// ---------- CPU ----------

type cpuStat struct{ user, nice, system, idle, iowait, irq, softirq, steal uint64 }

func (c cpuStat) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

func sat(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

func (c cpuStat) sub(p cpuStat) cpuStat {
	return cpuStat{sat(c.user, p.user), sat(c.nice, p.nice), sat(c.system, p.system), sat(c.idle, p.idle),
		sat(c.iowait, p.iowait), sat(c.irq, p.irq), sat(c.softirq, p.softirq), sat(c.steal, p.steal)}
}

func (d cpuStat) pcts() (u, s, w, st float64) {
	t := float64(d.total())
	if t <= 0 {
		return
	}
	return float64(d.user+d.nice) / t * 100, float64(d.system+d.irq+d.softirq) / t * 100,
		float64(d.iowait) / t * 100, float64(d.steal) / t * 100
}

func readCPU() (all cpuStat, cores []cpuStat) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu") {
			break
		}
		fs := strings.Fields(line)
		var v [8]uint64
		for i := 0; i < 8 && i+1 < len(fs); i++ {
			v[i], _ = strconv.ParseUint(fs[i+1], 10, 64)
		}
		s := cpuStat{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]}
		if fs[0] == "cpu" {
			all = s
		} else {
			cores = append(cores, s)
		}
	}
	return
}

// ---------- coletor ----------

type procPrev struct{ ticks, start uint64 }

type collector struct {
	pageSize  uint64
	prevAll   cpuStat
	prevCores []cpuStat
	prevT     time.Time
	prevProcs map[int]procPrev
	prevNet   map[string][2]uint64
	prevDisk  [2]uint64
	haveNet   bool
	users     map[uint32]string
	info      model.HostInfo
	name      string
}

func newCollector(name string) *collector {
	c := &collector{
		pageSize:  uint64(os.Getpagesize()),
		prevProcs: map[int]procPrev{},
		prevNet:   map[string][2]uint64{},
		users:     loadUsers(),
		name:      name,
	}
	c.info = staticInfo(name)
	return c
}

func loadUsers() map[uint32]string {
	m := map[uint32]string{}
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return m
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) > 2 {
			if id, err := strconv.ParseUint(f[2], 10, 32); err == nil {
				if _, ok := m[uint32(id)]; !ok {
					m[uint32(id)] = f[0]
				}
			}
		}
	}
	return m
}

func staticInfo(name string) model.HostInfo {
	hi := model.HostInfo{Hostname: name}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "PRETTY_NAME=") {
				hi.OS = strings.Trim(strings.TrimPrefix(l, "PRETTY_NAME="), `"`)
			}
		}
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		hi.Kernel = strings.TrimSpace(string(b))
	}
	hi.Arch = archName()
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "model name") {
				if i := strings.IndexByte(l, ':'); i > 0 {
					hi.CPUModel = strings.TrimSpace(l[i+1:])
				}
				break
			}
		}
	}
	return hi
}

func hostIPs() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
			out = append(out, ipn.IP.String())
		}
	}
	return out
}

// baseline apenas registra os contadores atuais (usado antes do primeiro envio).
func (c *collector) baseline() {
	c.fast()
}

// fast coleta tudo que muda a cada ciclo.
func (c *collector) fast() *model.Snapshot {
	now := time.Now()
	dt := now.Sub(c.prevT).Seconds()
	first := c.prevT.IsZero()
	s := &model.Snapshot{TS: now.Unix()}

	// CPU
	all, cores := readCPU()
	dAll := all.sub(c.prevAll)
	if !first {
		s.CPU.User, s.CPU.System, s.CPU.IOWait, s.CPU.Steal = dAll.pcts()
		s.CPU.Total = s.CPU.User + s.CPU.System + s.CPU.IOWait + s.CPU.Steal
		for i, cs := range cores {
			var p cpuStat
			if i < len(c.prevCores) {
				p = c.prevCores[i]
			}
			u, sy, w, _ := cs.sub(p).pcts()
			s.CPU.Cores = append(s.CPU.Cores, model.Core{U: r1(u), S: r1(sy), W: r1(w)})
		}
	} else {
		s.CPU.Cores = make([]model.Core, len(cores))
	}
	s.CPU.Total, s.CPU.User, s.CPU.System, s.CPU.IOWait, s.CPU.Steal = r1(s.CPU.Total), r1(s.CPU.User), r1(s.CPU.System), r1(s.CPU.IOWait), r1(s.CPU.Steal)
	ncpu := len(cores)
	if ncpu == 0 {
		ncpu = 1
	}
	c.prevAll, c.prevCores = all, cores

	// Info do host (uptime e IPs mudam)
	s.Host = c.info
	s.Host.Cores = ncpu
	s.Host.IPs = hostIPs()
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			u, _ := strconv.ParseFloat(f[0], 64)
			s.Host.Uptime = int64(u)
		}
	}

	// Load
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		for i := 0; i < 3 && i < len(f); i++ {
			s.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}

	s.Mem = readMem()
	s.Disks = readDisks()

	// Rede e I/O de disco (taxas)
	rx, tx, ifs := c.readNet(dt, first)
	s.Net = model.Net{RxBps: rx, TxBps: tx, Ifaces: ifs}
	s.DiskIO = c.readDiskIO(dt, first)

	// Processos
	s.Procs, s.Groups, s.Tasks = c.readProcs(dAll.total(), ncpu, s.Mem.Total, first)

	c.prevT = now
	return s
}

func r1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// ---------- memória ----------

func readMem() model.Mem {
	var m model.Mem
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return m
	}
	kv := map[string]uint64{}
	for _, l := range strings.Split(string(b), "\n") {
		i := strings.IndexByte(l, ':')
		if i < 0 {
			continue
		}
		f := strings.Fields(l[i+1:])
		if len(f) > 0 {
			v, _ := strconv.ParseUint(f[0], 10, 64)
			kv[l[:i]] = v * 1024
		}
	}
	m.Total = kv["MemTotal"]
	m.Free = kv["MemFree"]
	m.Available = kv["MemAvailable"]
	m.Buffers = kv["Buffers"]
	m.Cache = sat(kv["Cached"]+kv["SReclaimable"], kv["Shmem"])
	m.Used = sat(sat(sat(m.Total, m.Free), m.Buffers), m.Cache)
	if m.Available == 0 {
		m.Available = m.Free + m.Buffers + m.Cache
	}
	m.SwapTotal = kv["SwapTotal"]
	m.SwapUsed = sat(kv["SwapTotal"], kv["SwapFree"])
	return m
}

// ---------- discos ----------

var diskFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "f2fs": true, "nfs": true, "nfs4": true, "cifs": true, "overlay": true}

var skipMount = []string{"/var/lib/docker/", "/var/lib/containers/", "/var/lib/kubelet/", "/snap/", "/run/", "/sys/", "/proc/", "/dev/"}

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

func readDisks() []model.Disk {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []model.Disk
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 3 || !diskFS[fs[2]] {
			continue
		}
		dev, mnt := fs[0], unescapeMount(fs[1])
		if fs[2] == "overlay" && mnt != "/" {
			continue
		}
		skip := false
		for _, p := range skipMount {
			if strings.HasPrefix(mnt, p) {
				skip = true
			}
		}
		if skip || seen[dev+"|"+fs[2]] {
			continue
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(mnt, &st); err != nil || st.Blocks == 0 {
			continue
		}
		seen[dev+"|"+fs[2]] = true
		bs := uint64(st.Bsize)
		total := st.Blocks * bs
		used := (st.Blocks - st.Bfree) * bs
		avail := st.Bavail * bs
		pct := 0.0
		if used+avail > 0 {
			pct = float64(used) / float64(used+avail) * 100
		}
		ip := 0.0
		if st.Files > 0 {
			ip = float64(st.Files-st.Ffree) / float64(st.Files) * 100
		}
		out = append(out, model.Disk{Mount: mnt, Dev: dev, FS: fs[2], Total: total, Used: used, Avail: avail, Pct: r1(pct), InodePct: r1(ip)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

// ---------- rede e I/O de disco ----------

var virtIf = []string{"lo", "veth", "docker", "br-", "virbr", "cni", "flannel", "cali", "tun", "tap"}

func (c *collector) readNet(dt float64, first bool) (rx, tx uint64, ifs []model.Iface) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return
	}
	cur := map[string][2]uint64{}
	for _, l := range strings.Split(string(b), "\n") {
		i := strings.IndexByte(l, ':')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(l[:i])
		virt := false
		for _, p := range virtIf {
			if strings.HasPrefix(name, p) {
				virt = true
			}
		}
		if virt {
			continue
		}
		f := strings.Fields(l[i+1:])
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		cur[name] = [2]uint64{r, t}
		if p, ok := c.prevNet[name]; ok && !first && dt > 0 {
			ir, it := uint64(float64(sat(r, p[0]))/dt), uint64(float64(sat(t, p[1]))/dt)
			rx += ir
			tx += it
			ifs = append(ifs, model.Iface{Name: name, RxBps: ir, TxBps: it})
		}
	}
	c.prevNet = cur
	return
}

var wholeDisk = regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|hd[a-z]+|nvme\d+n\d+|mmcblk\d+)$`)

func (c *collector) readDiskIO(dt float64, first bool) model.DiskIO {
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return model.DiskIO{}
	}
	var rd, wr uint64
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 10 || !wholeDisk.MatchString(f[2]) {
			continue
		}
		r, _ := strconv.ParseUint(f[5], 10, 64)
		w, _ := strconv.ParseUint(f[9], 10, 64)
		rd += r * 512
		wr += w * 512
	}
	out := model.DiskIO{}
	if !first && dt > 0 {
		out.ReadBps = uint64(float64(sat(rd, c.prevDisk[0])) / dt)
		out.WriteBps = uint64(float64(sat(wr, c.prevDisk[1])) / dt)
	}
	c.prevDisk = [2]uint64{rd, wr}
	return out
}

// ---------- processos ----------

type procRaw struct {
	pid          int
	comm         string
	state        byte
	ticks, start uint64
	rss          uint64
	thr          int
}

func (c *collector) readProcs(dTotalAll uint64, ncpu int, memTotal uint64, first bool) ([]model.Proc, []model.Group, model.Tasks) {
	var tasks model.Tasks
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, nil, tasks
	}
	raws := make([]procRaw, 0, 256)
	for _, e := range ents {
		name := e.Name()
		if name[0] < '0' || name[0] > '9' {
			continue
		}
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + name + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if i < 0 || j < 0 || j+2 >= len(s) {
			continue
		}
		rest := strings.Fields(s[j+2:])
		if len(rest) < 22 {
			continue
		}
		ut, _ := strconv.ParseUint(rest[11], 10, 64)
		st, _ := strconv.ParseUint(rest[12], 10, 64)
		thr, _ := strconv.Atoi(rest[17])
		start, _ := strconv.ParseUint(rest[19], 10, 64)
		rssPages, _ := strconv.ParseUint(rest[21], 10, 64)
		raws = append(raws, procRaw{pid, s[i+1 : j], rest[0][0], ut + st, start, rssPages * c.pageSize, thr})
	}

	newPrev := make(map[int]procPrev, len(raws))
	type scored struct {
		raw procRaw
		cpu float64
	}
	list := make([]scored, 0, len(raws))
	groups := map[string]*model.Group{}
	for _, r := range raws {
		tasks.Total++
		tasks.Threads += r.thr
		switch r.state {
		case 'R':
			tasks.Running++
		case 'Z':
			tasks.Zombie++
		default:
			tasks.Sleeping++
		}
		cpu := 0.0
		if p, ok := c.prevProcs[r.pid]; ok && p.start == r.start && !first && dTotalAll > 0 {
			cpu = float64(sat(r.ticks, p.ticks)) * float64(ncpu) / float64(dTotalAll) * 100
		}
		newPrev[r.pid] = procPrev{r.ticks, r.start}
		list = append(list, scored{r, cpu})
		if r.rss > 0 {
			g := groups[r.comm]
			if g == nil {
				g = &model.Group{Name: r.comm}
				groups[r.comm] = g
			}
			g.RSS += r.rss
			g.CPU += cpu
			g.Count++
		}
	}
	c.prevProcs = newPrev

	// top 12 por CPU ∪ top 12 por memória
	pick := map[int]bool{}
	sort.Slice(list, func(i, j int) bool { return list[i].cpu > list[j].cpu })
	for i := 0; i < len(list) && i < 12; i++ {
		pick[list[i].raw.pid] = true
	}
	sort.Slice(list, func(i, j int) bool { return list[i].raw.rss > list[j].raw.rss })
	for i := 0; i < len(list) && i < 12; i++ {
		pick[list[i].raw.pid] = true
	}
	var procs []model.Proc
	for _, sc := range list {
		if !pick[sc.raw.pid] {
			continue
		}
		p := model.Proc{PID: sc.raw.pid, Name: sc.raw.comm, State: string(sc.raw.state), CPU: r1(sc.cpu), RSS: sc.raw.rss, Threads: sc.raw.thr}
		if memTotal > 0 {
			p.Mem = r1(float64(sc.raw.rss) / float64(memTotal) * 100)
		}
		p.Cmd, p.User = c.procMeta(sc.raw.pid, sc.raw.comm)
		procs = append(procs, p)
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].CPU > procs[j].CPU })

	gl := make([]model.Group, 0, len(groups))
	for _, g := range groups {
		g.CPU = r1(g.CPU)
		gl = append(gl, *g)
	}
	sort.Slice(gl, func(i, j int) bool { return gl[i].RSS > gl[j].RSS })
	if len(gl) > 10 {
		gl = gl[:10]
	}
	return procs, gl, tasks
}

// hideCmdline (AGENT_HIDE_CMDLINE=1) envia só o nome do processo, sem argumentos,
// para que senhas passadas na linha de comando nunca saiam da máquina.
var hideCmdline bool

func (c *collector) procMeta(pid int, comm string) (cmd, user string) {
	dir := "/proc/" + strconv.Itoa(pid)
	if b, err := os.ReadFile(dir + "/cmdline"); err == nil && len(b) > 0 && !hideCmdline {
		cmd = strings.TrimSpace(strings.ReplaceAll(strings.TrimRight(string(b), "\x00"), "\x00", " "))
	}
	if cmd == "" {
		cmd = "[" + comm + "]"
	}
	if len(cmd) > 160 {
		cmd = cmd[:160]
	}
	user = "?"
	if fi, err := os.Stat(dir); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			if u, ok := c.users[st.Uid]; ok {
				user = u
			} else {
				user = strconv.Itoa(int(st.Uid))
			}
		}
	}
	return
}
