// simulador: gera servidores fictícios para testar a interface e fazer demonstrações
// (prints, vídeos, posts) sem expor máquinas reais.
//
//	go run ./cmd/simulador -server http://127.0.0.1:8080 -token SEU_TOKEN
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"lynx/internal/model"
)

type disk struct {
	mount, dev, fs string
	totalGB        float64
	pct, perHour   float64
}
type procDef struct {
	name, cmd, user string
	share           float64 // fatia do CPU total
	rssMB           float64
	threads, count  int
}
type logT struct {
	level, src, msg string
	p               float64 // probabilidade por amostra
}
type prof struct {
	name, os, kernel string
	cores            int
	memGB            float64
	cpu, cpuAmp      float64
	mem, memPerHour  float64
	disks            []disk
	listen           []model.Listener
	conns            []model.Conn
	procs            []procDef
	probes           []model.Probe
	logs             []logT
	stopAfter        int // 0 = nunca para
	ip               string
}

func L(proto, addr string, port int, proc, svc string, exp bool) model.Listener {
	return model.Listener{Proto: proto, Addr: addr, Port: port, Proc: proc, Service: svc, Exposed: exp}
}

var profiles = []prof{
	{name: "web-prod-01", os: "Ubuntu 22.04.4 LTS", kernel: "5.15.0-107-generic", cores: 8, memGB: 16, cpu: 28, cpuAmp: 12, mem: 52, memPerHour: 0.1, ip: "10.0.1.11",
		disks: []disk{{"/", "/dev/sda2", "ext4", 200, 46, 0.01}, {"/var/log", "/dev/sdb1", "xfs", 100, 61, 0.05}},
		listen: []model.Listener{L("tcp", "*", 22, "sshd", "SSH", true), L("tcp", "*", 80, "nginx", "Nginx", true), L("tcp", "*", 443, "nginx", "Nginx", true),
			L("tcp", "*", 9100, "node_exporter", "Node Exporter", true), L("tcp", "*", 10050, "zabbix_agentd", "Zabbix Agent", true), L("tcp", "127.0.0.1", 9000, "php-fpm8.1", "", false)},
		conns: []model.Conn{{Dir: "out", Proc: "php-fpm8.1", Remote: "10.0.2.15", RPort: 5432, Service: "PostgreSQL", Count: 14},
			{Dir: "out", Proc: "php-fpm8.1", Remote: "10.0.2.20", RPort: 6379, Service: "Redis", Count: 6},
			{Dir: "out", Proc: "zabbix_agentd", Remote: "10.0.1.5", RPort: 10051, Service: "Zabbix Server", Count: 1},
			{Dir: "in", Proc: "nginx", Remote: "177.54.12.9", LPort: 443, Service: "Nginx", Count: 62},
			{Dir: "in", Proc: "nginx", Remote: "189.40.77.3", LPort: 443, Service: "Nginx", Count: 31}},
		procs: []procDef{{"php-fpm8.1", "php-fpm: pool www", "www-data", 0.42, 95, 24, 1}, {"nginx", "nginx: worker process", "www-data", 0.18, 38, 8, 1},
			{"redis-server", "/usr/bin/redis-server 127.0.0.1:6379", "redis", 0.05, 310, 1, 4}, {"node_exporter", "/usr/local/bin/node_exporter", "nodeexp", 0.02, 22, 1, 6},
			{"zabbix_agentd", "/usr/sbin/zabbix_agentd -c /etc/zabbix/zabbix_agentd.conf", "zabbix", 0.01, 6, 6, 1}},
		probes: []model.Probe{{URL: "http://127.0.0.1:80/", Port: 80, Proc: "nginx", Status: 200, Ms: 14}, {URL: "https://127.0.0.1:443/", Port: 443, Proc: "nginx", Status: 200, Ms: 22}},
		logs: []logT{{"error", "error.log", "upstream timed out (110: Connection timed out) while reading response header from upstream, client: 177.54.12.9, request: \"GET /api/pedidos HTTP/2.0\"", 0.05},
			{"warn", "error.log", "a client request body is buffered to a temporary file /var/cache/nginx/client_temp/0000012345", 0.04}}},

	{name: "db-postgres-01", os: "Rocky Linux 9.4 (Blue Onyx)", kernel: "5.14.0-427.el9.x86_64", cores: 16, memGB: 64, cpu: 41, cpuAmp: 18, mem: 74, memPerHour: 0.2, ip: "10.0.2.15",
		disks:  []disk{{"/", "/dev/mapper/rl-root", "xfs", 100, 34, 0.01}, {"/var/lib/pgsql", "/dev/mapper/vg_data-lv_pg", "xfs", 500, 71, 1.2}},
		listen: []model.Listener{L("tcp", "*", 22, "sshd", "SSH", true), L("tcp", "*", 5432, "postgres", "PostgreSQL", true), L("tcp", "*", 9100, "node_exporter", "Node Exporter", true), L("tcp", "*", 10050, "zabbix_agentd", "Zabbix Agent", true)},
		conns: []model.Conn{{Dir: "in", Proc: "postgres", Remote: "10.0.1.11", LPort: 5432, Service: "PostgreSQL", Count: 38}, {Dir: "in", Proc: "postgres", Remote: "10.0.1.12", LPort: 5432, Service: "PostgreSQL", Count: 17},
			{Dir: "in", Proc: "postgres", Remote: "10.0.3.8", LPort: 5432, Service: "PostgreSQL", Count: 41}, {Dir: "out", Proc: "zabbix_agentd", Remote: "10.0.1.5", RPort: 10051, Service: "Zabbix Server", Count: 1}},
		procs: []procDef{{"postgres", "postgres: app app_db 10.0.3.8(51822) SELECT", "postgres", 0.78, 820, 1, 52}, {"node_exporter", "/usr/local/bin/node_exporter", "nodeexp", 0.01, 20, 1, 6}, {"zabbix_agentd", "/usr/sbin/zabbix_agentd", "zabbix", 0.01, 6, 6, 1}},
		logs:  []logT{{"error", "postgresql-Tue.log", "FATAL:  password authentication failed for user \"app_ro\"", 0.03}, {"warn", "postgresql-Tue.log", "LOG:  checkpoints are occurring too frequently (24 seconds apart)", 0.05}}},

	{name: "zabbix-srv", os: "Debian GNU/Linux 12 (bookworm)", kernel: "6.1.0-21-amd64", cores: 4, memGB: 8, cpu: 34, cpuAmp: 10, mem: 58, memPerHour: 0.05, ip: "10.0.1.5",
		disks: []disk{{"/", "/dev/vda1", "ext4", 80, 38, 0.02}},
		listen: []model.Listener{L("tcp", "*", 22, "sshd", "SSH", true), L("tcp", "*", 80, "apache2", "Apache", true), L("tcp", "127.0.0.1", 3306, "mysqld", "MySQL/MariaDB", false),
			L("tcp", "*", 10051, "zabbix_server", "Zabbix Server", true), L("tcp", "*", 10050, "zabbix_agentd", "Zabbix Agent", true)},
		conns: []model.Conn{{Dir: "in", Proc: "zabbix_server", Remote: "10.0.1.11", LPort: 10051, Service: "Zabbix Server", Count: 1}, {Dir: "in", Proc: "zabbix_server", Remote: "10.0.2.15", LPort: 10051, Service: "Zabbix Server", Count: 1},
			{Dir: "in", Proc: "zabbix_server", Remote: "10.0.3.8", LPort: 10051, Service: "Zabbix Server", Count: 2}, {Dir: "out", Proc: "zabbix_server", Remote: "127.0.0.1", RPort: 3306, Service: "MySQL/MariaDB", Count: 24}},
		procs:  []procDef{{"zabbix_server", "/usr/sbin/zabbix_server: poller #1", "zabbix", 0.55, 60, 1, 38}, {"mysqld", "/usr/sbin/mysqld", "mysql", 0.3, 1450, 40, 1}, {"apache2", "/usr/sbin/apache2 -k start", "www-data", 0.08, 26, 1, 6}},
		probes: []model.Probe{{URL: "http://127.0.0.1:80/", Port: 80, Proc: "apache2", Status: 302, Ms: 31}},
		logs:   []logT{{"warn", "zabbix_server.log", "slow query of 5.21 sec (\"select ...history_uint...\")", 0.05}, {"error", "zabbix_server.log", "cannot send list of active checks to \"10.0.1.40\": host [db-old-03] not found", 0.03}}},

	{name: "wazuh-manager", os: "Ubuntu 22.04.4 LTS", kernel: "5.15.0-107-generic", cores: 8, memGB: 32, cpu: 52, cpuAmp: 15, mem: 84, memPerHour: 0.1, ip: "10.0.1.30",
		disks: []disk{{"/", "/dev/sda2", "ext4", 400, 64, 0.04}},
		listen: []model.Listener{L("tcp", "*", 22, "sshd", "SSH", true), L("tcp", "*", 443, "node", "HTTPS", true), L("tcp", "*", 1514, "wazuh-remoted", "Wazuh", true), L("tcp", "*", 1515, "wazuh-authd", "Wazuh", true),
			L("tcp", "*", 55000, "wazuh-apid", "Wazuh API", true), L("tcp", "*", 9200, "java", "Elasticsearch", true)},
		conns: []model.Conn{{Dir: "in", Proc: "wazuh-remoted", Remote: "10.0.1.11", LPort: 1514, Service: "Wazuh", Count: 1}, {Dir: "in", Proc: "wazuh-remoted", Remote: "10.0.1.5", LPort: 1514, Service: "Wazuh", Count: 1},
			{Dir: "in", Proc: "wazuh-remoted", Remote: "10.0.2.15", LPort: 1514, Service: "Wazuh", Count: 1}, {Dir: "out", Proc: "filebeat", Remote: "127.0.0.1", RPort: 9200, Service: "Elasticsearch", Count: 4}},
		procs: []procDef{{"java", "/usr/share/wazuh-indexer/jdk/bin/java -Xms8g -Xmx8g", "wazuh-indexer", 0.62, 11800, 120, 1}, {"wazuh-analysisd", "/var/ossec/bin/wazuh-analysisd", "wazuh", 0.2, 940, 12, 1},
			{"node", "/usr/share/wazuh-dashboard/node/bin/node", "wazuh-dashboard", 0.05, 610, 11, 1}, {"filebeat", "/usr/share/filebeat/bin/filebeat", "root", 0.04, 120, 14, 1}},
		logs: []logT{{"error", "ossec.log", "wazuh-remoted: ERROR: (1103): Could not open file '/var/ossec/queue/rids/012'", 0.03}, {"warn", "ossec.log", "wazuh-remoted: WARNING: Agent buffer at 90 %.", 0.05}}},

	{name: "app-java-02", os: "Debian GNU/Linux 12 (bookworm)", kernel: "6.1.0-21-amd64", cores: 4, memGB: 8, cpu: 90, cpuAmp: 7, mem: 83, memPerHour: 0.4, ip: "10.0.3.8",
		disks:  []disk{{"/", "/dev/vda1", "ext4", 80, 83, 0.03}},
		listen: []model.Listener{L("tcp", "*", 22, "sshd", "SSH", true), L("tcp", "*", 8080, "java", "HTTP-alt", true), L("tcp", "*", 10050, "zabbix_agentd", "Zabbix Agent", true)},
		conns: []model.Conn{{Dir: "out", Proc: "java", Remote: "10.0.2.15", RPort: 5432, Service: "PostgreSQL", Count: 41}, {Dir: "out", Proc: "java", Remote: "10.0.2.30", RPort: 9092, Service: "Kafka", Count: 6},
			{Dir: "out", Proc: "zabbix_agentd", Remote: "10.0.1.5", RPort: 10051, Service: "Zabbix Server", Count: 1}, {Dir: "in", Proc: "java", Remote: "10.0.1.11", LPort: 8080, Service: "HTTP-alt", Count: 28}},
		procs:  []procDef{{"java", "/opt/java/bin/java -Xmx5g -jar pedidos-api.jar --server.port=8080", "app", 0.93, 5900, 210, 1}, {"zabbix_agentd", "/usr/sbin/zabbix_agentd", "zabbix", 0.01, 6, 6, 1}},
		probes: []model.Probe{{URL: "http://127.0.0.1:8080/", Port: 8080, Proc: "java", Status: 503, Ms: 1840}},
		logs: []logT{{"error", "catalina.out", "java.sql.SQLTransientConnectionException: HikariPool-1 - Connection is not available, request timed out after 30000ms.", 0.55},
			{"warn", "catalina.out", "[GC overhead] Pause Full (Allocation Failure) 5120M->4980M(5120M) 2847.113ms", 0.25}}},

	{name: "proxy-edge-01", os: "Ubuntu 24.04 LTS", kernel: "6.8.0-45-generic", cores: 2, memGB: 4, cpu: 11, cpuAmp: 5, mem: 31, memPerHour: 0.01, ip: "10.0.0.2",
		disks:  []disk{{"/", "/dev/vda1", "ext4", 40, 22, 0.005}},
		listen: []model.Listener{L("tcp", "*", 22, "sshd", "SSH", true), L("tcp", "*", 80, "haproxy", "HAProxy", true), L("tcp", "*", 443, "haproxy", "HAProxy", true), L("tcp", "127.0.0.1", 8404, "haproxy", "", false)},
		conns:  []model.Conn{{Dir: "out", Proc: "haproxy", Remote: "10.0.1.11", RPort: 443, Service: "HTTPS", Count: 48}, {Dir: "out", Proc: "haproxy", Remote: "10.0.3.8", RPort: 8080, Service: "HTTP-alt", Count: 19}},
		procs:  []procDef{{"haproxy", "/usr/sbin/haproxy -Ws -f /etc/haproxy/haproxy.cfg", "haproxy", 0.8, 180, 4, 1}},
		probes: []model.Probe{{URL: "http://127.0.0.1:80/", Port: 80, Proc: "haproxy", Status: 301, Ms: 3}, {URL: "https://127.0.0.1:443/", Port: 443, Proc: "haproxy", Status: 200, Ms: 9}}},

	{name: "backup-nas-01", os: "Debian GNU/Linux 12 (bookworm)", kernel: "6.1.0-21-amd64", cores: 2, memGB: 4, cpu: 8, cpuAmp: 4, mem: 45, memPerHour: 0.02, ip: "10.0.4.9", stopAfter: 2,
		disks:  []disk{{"/", "/dev/sda1", "ext4", 30, 41, 0}, {"/srv/backup", "/dev/sdb1", "ext4", 4000, 88, 0.02}},
		listen: []model.Listener{L("tcp", "*", 22, "sshd", "SSH", true), L("tcp", "*", 2049, "nfsd", "NFS", true)},
		procs:  []procDef{{"rsync", "rsync --daemon", "root", 0.3, 40, 1, 2}}},
}

type sim struct {
	p       prof
	rnd     *rand.Rand
	start   time.Time
	pid     int
	critLog bool
}

func (s *sim) snapshot(ts time.Time, slow bool, interval int) *model.Snapshot {
	t := ts.Sub(s.start).Seconds()
	p := s.p
	phase := float64(len(p.name))
	cpu := p.cpu + p.cpuAmp*math.Sin(t/240+phase) + s.rnd.NormFloat64()*1.8
	cpu = math.Max(1, math.Min(99, cpu))
	memPct := p.mem + p.memPerHour*t/3600 + math.Sin(t/500+phase)*0.4
	memPct = math.Min(97, memPct)
	gib := float64(1 << 30)
	total := p.memGB * gib
	avail := total * (1 - memPct/100)
	buf, cache := total*0.015, total*0.12
	free := math.Max(total*0.01, avail-buf-cache)
	used := total - free - buf - cache

	sn := &model.Snapshot{Name: p.name, Version: "0.1.0-sim", Interval: interval, TS: ts.Unix(), Slow: slow}
	sn.Host = model.HostInfo{Hostname: p.name, OS: p.os, Kernel: p.kernel, Arch: "amd64", CPUModel: "Intel(R) Xeon(R) Silver 4314 CPU @ 2.40GHz", Cores: p.cores,
		Uptime: int64(86400*23 + t), IPs: []string{p.ip}}
	user := cpu * 0.72
	sys := cpu * 0.22
	iow := cpu * 0.05
	sn.CPU = model.CPU{Total: r1(cpu), User: r1(user), System: r1(sys), IOWait: r1(iow), Steal: 0}
	for i := 0; i < p.cores; i++ {
		c := math.Max(0.5, math.Min(100, cpu+s.rnd.NormFloat64()*cpu*0.35))
		sn.CPU.Cores = append(sn.CPU.Cores, model.Core{U: r1(c * 0.72), S: r1(c * 0.22), W: r1(c * 0.05)})
	}
	l1 := cpu / 100 * float64(p.cores) * (0.97 + 0.06*math.Sin(t/70+phase) + 0.02*s.rnd.Float64())
	if cpu > 60 { // máquina saturada: processos na fila de execução (curva contínua)
		l1 *= 1 + 1.4*math.Min(1, (cpu-60)/30)
	}
	sn.Load = [3]float64{r1(l1), r1(l1 * 0.92), r1(l1 * 0.85)}
	for i := range sn.Load {
		sn.Load[i] = math.Round(sn.Load[i]*100) / 100
	}
	sn.Mem = model.Mem{Total: uint64(total), Used: uint64(used), Buffers: uint64(buf), Cache: uint64(cache), Free: uint64(free), Available: uint64(avail)}
	if p.name == "app-java-02" || p.name == "wazuh-manager" {
		sn.Mem.SwapTotal = uint64(4 * gib)
		sn.Mem.SwapUsed = uint64(0.9 * gib * (memPct - 70) / 10)
	}
	for _, d := range p.disks {
		pc := math.Min(99, d.pct+d.perHour*t/3600)
		tot := d.totalGB * gib
		sn.Disks = append(sn.Disks, model.Disk{Mount: d.mount, Dev: d.dev, FS: d.fs, Total: uint64(tot), Used: uint64(tot * pc / 100), Avail: uint64(tot * (1 - pc/100)), Pct: r1(pc), InodePct: r1(pc * 0.3)})
	}
	sn.DiskIO = model.DiskIO{ReadBps: uint64(math.Max(0, 2e6+cpu*1.5e5*s.rnd.Float64())), WriteBps: uint64(math.Max(0, 4e6+cpu*3e5*s.rnd.Float64()))}
	rx := math.Max(2e4, 4e5+cpu*2.2e4*(0.6+s.rnd.Float64()))
	sn.Net = model.Net{RxBps: uint64(rx), TxBps: uint64(rx * 0.6), Ifaces: []model.Iface{{Name: "eth0", RxBps: uint64(rx), TxBps: uint64(rx * 0.6)}}}

	// processos
	var groups []model.Group
	nproc := 0
	for _, d := range p.procs {
		cpuSingle := d.share * cpu / 100 * float64(p.cores)
		rss := d.rssMB * float64(1<<20) * (1 + 0.02*math.Sin(t/90))
		nproc += d.count
		groups = append(groups, model.Group{Name: d.name, RSS: uint64(rss) * uint64(d.count), CPU: r1(cpuSingle), Count: d.count})
		s.pid++
		sn.Procs = append(sn.Procs, model.Proc{PID: 900 + len(sn.Procs)*37, User: d.user, Name: d.name, Cmd: d.cmd, State: "S",
			CPU: r1(cpuSingle / float64(d.count) * (1 + s.rnd.Float64()*0.3)), Mem: r1(rss / total * 100), RSS: uint64(rss), Threads: d.threads})
	}
	sn.Procs = append(sn.Procs, model.Proc{PID: 1, User: "root", Name: "systemd", Cmd: "/sbin/init", State: "S", CPU: 0, Mem: 0.1, RSS: 14 << 20, Threads: 1},
		model.Proc{PID: 612, User: "root", Name: "sshd", Cmd: "sshd: /usr/sbin/sshd -D [listener]", State: "S", CPU: 0, Mem: 0.1, RSS: 9 << 20, Threads: 1})
	groups = append(groups, model.Group{Name: "systemd", RSS: 14 << 20, Count: 1}, model.Group{Name: "sshd", RSS: 9 << 20, Count: 2})
	sn.Groups = groups
	for i := range sn.Procs {
		for j := i + 1; j < len(sn.Procs); j++ {
			if sn.Procs[j].CPU > sn.Procs[i].CPU {
				sn.Procs[i], sn.Procs[j] = sn.Procs[j], sn.Procs[i]
			}
		}
	}
	sn.Tasks = model.Tasks{Total: 110 + nproc, Running: 1 + int(cpu/25), Sleeping: 109 + nproc - int(cpu/25), Zombie: 0, Threads: 320 + nproc*3}

	if slow {
		sn.Listen, sn.Conns = p.listen, p.conns
		sn.Probes = append([]model.Probe(nil), p.probes...)
		for i := range sn.Probes {
			sn.Probes[i].Ms += s.rnd.Intn(8)
		}
	}
	for _, lt := range p.logs {
		if s.rnd.Float64() < lt.p {
			sn.Logs = append(sn.Logs, model.LogLine{T: ts.UnixMilli(), Src: lt.src, Level: lt.level, Msg: lt.msg})
		}
	}
	return sn
}

func r1(v float64) float64 { return math.Round(v*10) / 10 }

func send(cl *http.Client, server, token string, sn *model.Snapshot) error {
	raw, _ := json.Marshal(sn)
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write(raw)
	zw.Close()
	req, _ := http.NewRequest("POST", server+"/api/ingest", &b)
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func main() {
	server := flag.String("server", "http://127.0.0.1:8080", "URL do servidor")
	token := flag.String("token", "", "token de ingestão")
	n := flag.Int("hosts", len(profiles), "quantidade de servidores simulados")
	interval := flag.Int("interval", 5, "segundos entre envios ao vivo")
	back := flag.Int("backfill", 90, "minutos de histórico para preencher ao iniciar")
	flag.Parse()
	if *token == "" {
		fmt.Println("use -token")
		return
	}
	if *n > len(profiles) {
		*n = len(profiles)
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	var wg sync.WaitGroup
	for i := 0; i < *n; i++ {
		wg.Add(1)
		go func(p prof, seed int64) {
			defer wg.Done()
			now := time.Now()
			s := &sim{p: p, rnd: rand.New(rand.NewSource(seed)), start: now.Add(-time.Duration(*back) * time.Minute)}
			steps := *back * 6 // 1 amostra a cada 10 s
			for k := -steps; k < 0; k++ {
				ts := now.Add(time.Duration(k) * 10 * time.Second)
				sn := s.snapshot(ts, k%3 == 0, 10)
				if p.name == "app-java-02" && k == -50 { // um evento crítico recente
					sn.Logs = append(sn.Logs, model.LogLine{T: ts.UnixMilli(), Src: "kernel", Level: "crit", Msg: "Out of memory: Killed process 2211 (java) total-vm:9204312kB, anon-rss:5961220kB"})
				}
				if err := send(cl, *server, *token, sn); err != nil {
					fmt.Println(p.name, "backfill:", err)
					return
				}
			}
			fmt.Println(p.name, "histórico enviado")
			for c := 0; ; c++ {
				if p.stopAfter > 0 && c >= p.stopAfter {
					fmt.Println(p.name, "parou de enviar (simulando queda)")
					return
				}
				sn := s.snapshot(time.Now(), c%6 == 0, *interval)
				if err := send(cl, *server, *token, sn); err != nil {
					fmt.Println(p.name, err)
				}
				time.Sleep(time.Duration(*interval) * time.Second)
			}
		}(profiles[i], int64(i+1))
	}
	wg.Wait()
}
