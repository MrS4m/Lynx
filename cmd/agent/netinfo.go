package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lynx/internal/model"
)

// Nomes de serviços por porta (usado para rotular portas abertas e conexões).
var portNames = map[int]string{
	22: "SSH", 25: "SMTP", 53: "DNS", 80: "HTTP", 110: "POP3", 123: "NTP", 143: "IMAP", 389: "LDAP", 443: "HTTPS",
	445: "SMB", 465: "SMTPS", 514: "Syslog", 587: "SMTP", 636: "LDAPS", 993: "IMAPS", 995: "POP3S",
	1433: "SQL Server", 1514: "Wazuh", 1515: "Wazuh", 1521: "Oracle DB", 2049: "NFS", 2181: "ZooKeeper",
	2375: "Docker API", 2376: "Docker API", 3000: "Grafana", 3100: "Loki", 3306: "MySQL/MariaDB", 3389: "RDP",
	5000: "HTTP-alt", 5432: "PostgreSQL", 5601: "Kibana", 5672: "RabbitMQ", 5984: "CouchDB", 6379: "Redis",
	6443: "Kubernetes API", 8000: "HTTP-alt", 8080: "HTTP-alt", 8086: "InfluxDB", 8443: "HTTPS-alt",
	8500: "Consul", 9042: "Cassandra", 9090: "Prometheus", 9092: "Kafka", 9093: "Alertmanager", 9100: "Node Exporter",
	9200: "Elasticsearch", 9300: "Elasticsearch", 10050: "Zabbix Agent", 10051: "Zabbix Server", 11211: "Memcached",
	15672: "RabbitMQ UI", 27017: "MongoDB", 55000: "Wazuh API",
}

// Dicas por nome de processo (prioridade sobre o nome da porta em portas que escutam).
var procHints = map[string]string{
	"nginx": "Nginx", "apache2": "Apache", "httpd": "Apache", "caddy": "Caddy", "traefik": "Traefik", "haproxy": "HAProxy",
	"postgres": "PostgreSQL", "postmaster": "PostgreSQL", "mysqld": "MySQL/MariaDB", "mariadbd": "MySQL/MariaDB",
	"redis-server": "Redis", "mongod": "MongoDB", "zabbix_agentd": "Zabbix Agent", "zabbix_agent2": "Zabbix Agent",
	"zabbix_server": "Zabbix Server", "sshd": "SSH", "dockerd": "Docker", "named": "DNS (BIND)", "dnsmasq": "DNS (dnsmasq)",
	"grafana": "Grafana", "grafana-server": "Grafana", "prometheus": "Prometheus", "node_exporter": "Node Exporter",
	"wazuh-manager": "Wazuh", "wazuh-agentd": "Wazuh", "wazuh-remoted": "Wazuh", "wazuh-apid": "Wazuh API",
	"filebeat": "Filebeat", "rabbitmq": "RabbitMQ", "memcached": "Memcached", "chronyd": "NTP", "master": "Postfix",
}

var webPorts = map[int]bool{80: true, 443: true, 3000: true, 5000: true, 8000: true, 8008: true, 8080: true, 8081: true, 8443: true, 8888: true, 9443: true}
var webProcs = map[string]bool{"nginx": true, "apache2": true, "httpd": true, "caddy": true, "traefik": true, "lighttpd": true, "haproxy": true}

func svcLabel(port int, proc string, useProc bool) string {
	if useProc {
		if h, ok := procHints[proc]; ok {
			return h
		}
	}
	return portNames[port]
}

// ---------- sockets ----------

type sock struct {
	proto      string
	lip, rip   string
	lport, rpt int
	state      int
	inode      uint64
}

func parseAddr(s string) (string, int) {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return "", 0
	}
	port, _ := strconv.ParseUint(s[i+1:], 16, 32)
	h := s[:i]
	var ip net.IP
	switch len(h) {
	case 8:
		v, _ := strconv.ParseUint(h, 16, 32)
		ip = net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	case 32:
		b := make([]byte, 16)
		for g := 0; g < 4; g++ {
			v, _ := strconv.ParseUint(h[g*8:g*8+8], 16, 32)
			b[g*4], b[g*4+1], b[g*4+2], b[g*4+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
		}
		ip = net.IP(b)
	default:
		return "", int(port)
	}
	return ip.String(), int(port)
}

func readSocks(file, proto string) []sock {
	b, err := os.ReadFile("/proc/net/" + file)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	var out []sock
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) < 10 {
			continue
		}
		lip, lp := parseAddr(f[1])
		rip, rp := parseAddr(f[2])
		st, _ := strconv.ParseUint(f[3], 16, 32)
		in, _ := strconv.ParseUint(f[9], 10, 64)
		out = append(out, sock{proto, lip, rip, lp, rp, int(st), in})
	}
	return out
}

// mapInodes descobre qual PID é dono de cada socket de interesse.
func mapInodes(want map[uint64]struct{}) map[uint64]int {
	res := map[uint64]int{}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return res
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := "/proc/" + e.Name() + "/fd"
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			l, err := os.Readlink(dir + "/" + fd.Name())
			if err != nil || !strings.HasPrefix(l, "socket:[") {
				continue
			}
			in, err := strconv.ParseUint(l[8:len(l)-1], 10, 64)
			if err != nil {
				continue
			}
			if _, ok := want[in]; ok {
				if old, ok := res[in]; !ok || pid < old {
					res[in] = pid
				}
			}
		}
	}
	return res
}

func commOf(pid int, cache map[int]string) string {
	if n, ok := cache[pid]; ok {
		return n
	}
	n := ""
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm"); err == nil {
		n = strings.TrimSpace(string(b))
	}
	cache[pid] = n
	return n
}

func isLoop(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.IsLoopback()
}

// slow coleta portas, conexões e testes web (mais caro, roda a cada ~30s).
func (c *collector) slow(extraProbes []string) ([]model.Listener, []model.Conn, []model.Probe) {
	var socks []sock
	socks = append(socks, readSocks("tcp", "tcp")...)
	socks = append(socks, readSocks("tcp6", "tcp")...)
	socks = append(socks, readSocks("udp", "udp")...)
	socks = append(socks, readSocks("udp6", "udp")...)

	want := map[uint64]struct{}{}
	for _, s := range socks {
		if s.inode == 0 {
			continue
		}
		if (s.proto == "tcp" && (s.state == 0x0A || s.state == 0x01)) || (s.proto == "udp" && s.rpt == 0) {
			want[s.inode] = struct{}{}
		}
	}
	owners := mapInodes(want)
	names := map[int]string{}

	// Listeners
	seen := map[string]bool{}
	listenPorts := map[int]bool{}
	var listen []model.Listener
	for _, s := range socks {
		isL := (s.proto == "tcp" && s.state == 0x0A) || (s.proto == "udp" && s.rpt == 0 && s.lport > 0)
		if !isL {
			continue
		}
		addr := s.lip
		if ip := net.ParseIP(s.lip); ip != nil && ip.IsUnspecified() {
			addr = "*"
		}
		key := s.proto + "|" + strconv.Itoa(s.lport) + "|" + addr
		if seen[key] {
			continue
		}
		seen[key] = true
		pid := owners[s.inode]
		proc := ""
		if pid > 0 {
			proc = commOf(pid, names)
		}
		if s.proto == "tcp" {
			listenPorts[s.lport] = true
		}
		listen = append(listen, model.Listener{
			Proto: s.proto, Addr: addr, Port: s.lport, PID: pid, Proc: proc,
			Service: svcLabel(s.lport, proc, true), Exposed: addr == "*" || !isLoop(addr),
		})
	}
	sort.Slice(listen, func(i, j int) bool {
		if listen[i].Proto != listen[j].Proto {
			return listen[i].Proto < listen[j].Proto
		}
		return listen[i].Port < listen[j].Port
	})

	// Conexões estabelecidas, agrupadas
	type ck struct {
		dir, proc, rip string
		port           int
	}
	agg := map[ck]*model.Conn{}
	for _, s := range socks {
		if s.proto != "tcp" || s.state != 0x01 {
			continue
		}
		pid := owners[s.inode]
		proc := ""
		if pid > 0 {
			proc = commOf(pid, names)
		}
		var k ck
		var svc string
		if listenPorts[s.lport] {
			k = ck{"in", proc, s.rip, s.lport}
			svc = svcLabel(s.lport, proc, true)
		} else {
			k = ck{"out", proc, s.rip, s.rpt}
			svc = svcLabel(s.rpt, proc, false)
		}
		if g := agg[k]; g != nil {
			g.Count++
			continue
		}
		agg[k] = &model.Conn{Dir: k.dir, Proc: proc, Remote: s.rip, RPort: s.rpt, LPort: s.lport, Service: svc, Count: 1}
	}
	conns := make([]model.Conn, 0, len(agg))
	for _, g := range agg {
		conns = append(conns, *g)
	}
	sort.Slice(conns, func(i, j int) bool { return conns[i].Count > conns[j].Count })
	if len(conns) > 150 {
		conns = conns[:150]
	}

	return listen, conns, probeWeb(listen, extraProbes)
}

// ---------- testes HTTP locais ----------

func probeWeb(listen []model.Listener, extra []string) []model.Probe {
	type target struct {
		url, proc string
		port      int
	}
	var targets []target
	seen := map[int]bool{}
	for _, l := range listen {
		if l.Proto != "tcp" || seen[l.Port] || !(webPorts[l.Port] || webProcs[l.Proc]) {
			continue
		}
		seen[l.Port] = true
		host := "127.0.0.1"
		if l.Addr != "*" {
			host = l.Addr
		}
		scheme := "http"
		if l.Port == 443 || l.Port == 8443 || l.Port == 9443 {
			scheme = "https"
		}
		targets = append(targets, target{scheme + "://" + net.JoinHostPort(host, strconv.Itoa(l.Port)) + "/", l.Proc, l.Port})
	}
	for _, u := range extra {
		if u = strings.TrimSpace(u); u != "" {
			targets = append(targets, target{u, "custom", 0})
		}
	}
	if len(targets) == 0 {
		return nil
	}
	cl := &http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DisableKeepAlives: true},
	}
	out := make([]model.Probe, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t target) {
			defer wg.Done()
			p := model.Probe{URL: t.url, Port: t.port, Proc: t.proc}
			req, _ := http.NewRequest("GET", t.url, nil)
			req.Header.Set("User-Agent", "lynx-agent")
			st := time.Now()
			resp, err := cl.Do(req)
			p.Ms = int(time.Since(st).Milliseconds())
			if err != nil {
				e := err.Error()
				switch {
				case strings.Contains(e, "timeout") || strings.Contains(e, "deadline"):
					p.Err = "timeout"
				case strings.Contains(e, "refused"):
					p.Err = "recusada"
				default:
					p.Err = "resposta inválida"
				}
			} else {
				p.Status = resp.StatusCode
				resp.Body.Close()
			}
			out[i] = p
		}(i, t)
	}
	wg.Wait()
	return out
}
