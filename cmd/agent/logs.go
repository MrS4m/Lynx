package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"lynx/internal/model"
)

// Arquivos de log de aplicações monitorados por padrão (globs).
var defaultLogs = []string{
	"/var/log/nginx/error.log",
	"/var/log/apache2/error.log",
	"/var/log/httpd/error_log",
	"/var/log/postgresql/*.log",
	"/var/log/mysql/error.log",
	"/var/log/mariadb/mariadb.log",
	"/var/log/redis/redis-server.log",
	"/var/log/zabbix/zabbix_agentd.log",
	"/var/log/zabbix/zabbix_server.log",
	"/var/ossec/logs/ossec.log",
	"/var/log/tomcat*/catalina.out",
	"/opt/tomcat/logs/catalina.out",
}

// Logs do sistema em arquivo. Só são lidos se o journald não estiver ativo
// (senão as mesmas linhas chegariam duplicadas).
var systemLogs = []string{"/var/log/syslog", "/var/log/messages", "/var/log/kern.log", "/var/log/auth.log", "/var/log/secure"}

const (
	maxLinesPerPoll = 100
	maxChunk        = 256 << 10
	maxMsg          = 400
)

type tail struct {
	ino uint64
	off int64
}

type logger struct {
	all      bool
	globs    []string
	files    map[string]*tail
	started  bool
	lastGlob time.Time
	paths    []string
	journal  bool
	ch       chan model.LogLine
}

func newLogger(all bool, extra []string) *logger {
	l := &logger{all: all, files: map[string]*tail{}, ch: make(chan model.LogLine, 1000)}
	_, err := exec.LookPath("journalctl")
	_, err2 := os.Stat("/run/systemd/journal")
	l.journal = err == nil && err2 == nil
	l.globs = append(l.globs, defaultLogs...)
	if !l.journal {
		l.globs = append(l.globs, systemLogs...)
	}
	for _, e := range extra {
		if e = strings.TrimSpace(e); e != "" {
			l.globs = append(l.globs, e)
		}
	}
	return l
}

func (l *logger) start() {
	if l.journal {
		go l.runJournal()
	}
}

var critWords = []string{"out of memory", "oom-kill", "oom_kill", "oom killer", "kernel panic", "segfault", "fatal", "emergency", "i/o error", "corruption", "no space left"}
var errWords = []string{"error", "fail", "denied", "refused", "exception", "traceback", "critical", "timeout", "timed out", "unreachable", "invalid user", "cannot", "unable to"}

func classify(msg string) string {
	m := strings.ToLower(msg)
	for _, w := range critWords {
		if strings.Contains(m, w) {
			return "crit"
		}
	}
	for _, w := range errWords {
		if strings.Contains(m, w) {
			return "error"
		}
	}
	if strings.Contains(m, "warn") {
		return "warn"
	}
	return "info"
}

func clip(s string) string {
	s = strings.TrimRight(s, "\r\n ")
	if len(s) > maxMsg {
		s = s[:maxMsg] + "…"
	}
	return s
}

func (l *logger) runJournal() {
	for {
		cmd := exec.Command("journalctl", "-f", "-n", "0", "-p", "info", "-o", "json", "--no-pager")
		out, err := cmd.StdoutPipe()
		if err == nil && cmd.Start() == nil {
			sc := bufio.NewScanner(out)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				var je struct {
					Message  any    `json:"MESSAGE"`
					Priority string `json:"PRIORITY"`
					Unit     string `json:"_SYSTEMD_UNIT"`
					Ident    string `json:"SYSLOG_IDENTIFIER"`
					TS       string `json:"__REALTIME_TIMESTAMP"`
				}
				if json.Unmarshal(sc.Bytes(), &je) != nil {
					continue
				}
				msg, ok := je.Message.(string)
				if !ok || msg == "" {
					continue
				}
				lvl := classify(msg)
				if p, _ := strconv.Atoi(je.Priority); p <= 3 && (lvl == "info" || lvl == "warn") {
					lvl = "error"
				} else if p == 4 && lvl == "info" {
					lvl = "warn"
				}
				if lvl == "info" && !l.all {
					continue
				}
				src := je.Unit
				if src == "" {
					src = je.Ident
				}
				t := time.Now().UnixMilli()
				if us, err := strconv.ParseInt(je.TS, 10, 64); err == nil {
					t = us / 1000
				}
				select {
				case l.ch <- model.LogLine{T: t, Src: src, Level: lvl, Msg: clip(msg)}:
				default:
				}
			}
			cmd.Wait()
		}
		time.Sleep(15 * time.Second)
	}
}

func (l *logger) expand() []string {
	if time.Since(l.lastGlob) < 30*time.Second && l.paths != nil {
		return l.paths
	}
	seen := map[string]bool{}
	var out []string
	for _, g := range l.globs {
		ms, _ := filepath.Glob(g)
		for _, m := range ms {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	l.paths, l.lastGlob = out, time.Now()
	return out
}

// poll devolve as linhas novas desde a última chamada.
func (l *logger) poll() ([]model.LogLine, int) {
	var out []model.LogLine
	dropped := 0

	// journald
	for len(out) < maxLinesPerPoll {
		select {
		case ln := <-l.ch:
			out = append(out, ln)
			continue
		default:
		}
		break
	}

	// arquivos
	for _, p := range l.expand() {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		t := l.files[p]
		if t == nil {
			t = &tail{ino: st.Ino}
			if !l.started { // arquivos já existentes: começa do fim
				t.off = fi.Size()
			}
			l.files[p] = t
		}
		if t.ino != st.Ino || fi.Size() < t.off { // rotação
			t.ino, t.off = st.Ino, 0
		}
		size := fi.Size()
		if size == t.off {
			continue
		}
		if size-t.off > maxChunk { // backlog grande: pula para o final
			t.off = size - 64<<10
			dropped++
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		buf := make([]byte, size-t.off)
		n, _ := f.ReadAt(buf, t.off)
		f.Close()
		buf = buf[:n]
		cut := strings.LastIndexByte(string(buf), '\n')
		if cut < 0 {
			if n < maxChunk {
				continue // linha incompleta, espera completar
			}
			cut = n - 1
		}
		t.off += int64(cut + 1)
		src := filepath.Base(p)
		now := time.Now().UnixMilli()
		for _, line := range strings.Split(string(buf[:cut]), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			lvl := classify(line)
			if lvl == "info" && !l.all {
				continue
			}
			if len(out) >= maxLinesPerPoll {
				dropped++
				continue
			}
			out = append(out, model.LogLine{T: now, Src: src, Level: lvl, Msg: clip(line)})
		}
	}
	l.started = true
	return out, dropped
}

var _ = io.EOF
