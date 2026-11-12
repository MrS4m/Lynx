// lynx-server: recebe métricas dos agentes, avalia alertas e serve o painel web.
package main

import (
	"context"
	"crypto/subtle"
	"embed"
	_ "embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

//go:embed web
var webFS embed.FS

//go:embed install.sh.tmpl
var installTmpl string

//go:embed install.ps1.tmpl
var installPS1Tmpl string

//go:embed agent.ps1
var agentPS1 []byte

type App struct {
	store    *Store
	token    string
	binDir   string
	dashUser string
	dashPass string
	tvKey    string
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	hc := flag.Bool("healthcheck", false, "consulta /healthz local e sai (uso no Docker)")
	flag.Parse()
	addr := env("ADDR", ":8080")
	if *hc {
		port := addr[strings.LastIndex(addr, ":")+1:]
		c := http.Client{Timeout: 3 * time.Second}
		resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}

	token := os.Getenv("TOKEN")
	if token == "" {
		log.Fatal("defina a variável TOKEN (segredo usado pelos agentes para enviar dados)")
	}
	app := &App{
		store:    NewStore(env("DATA_DIR", "./data")),
		token:    token,
		binDir:   env("BIN_DIR", "./bin"),
		dashUser: os.Getenv("DASH_USER"),
		dashPass: os.Getenv("DASH_PASS"),
		tvKey:    os.Getenv("TV_KEY"),
	}
	if app.dashUser != "" && app.dashPass == "" {
		log.Fatal("DASH_USER definido sem DASH_PASS")
	}
	if err := app.store.Load(); err != nil {
		log.Printf("estado anterior não carregado (%v); começando vazio", err)
	}

	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServer(http.FS(sub))

	mux := http.NewServeMux()
	// públicas
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("POST /api/ingest", app.handleIngest)
	mux.HandleFunc("GET /install.sh", app.handleInstall)
	mux.HandleFunc("GET /install.ps1", app.handleInstallPS1)
	mux.HandleFunc("GET /download/{name}", app.handleDownload)
	// painel
	mux.HandleFunc("GET /api/overview", app.store.handleOverview)
	mux.HandleFunc("GET /api/hosts/{name}", app.store.handleHost)
	mux.HandleFunc("GET /api/alerts", app.store.handleAlerts)
	mux.HandleFunc("GET /api/logs", app.store.handleLogs)
	mux.HandleFunc("DELETE /api/hosts/{name}", app.store.handleForget)
	mux.HandleFunc("DELETE /api/alerts/{id}", app.store.handleDismiss)
	mux.Handle("/", cacheHeaders(static))

	srv := &http.Server{Addr: addr, Handler: app.guard(mux), ReadHeaderTimeout: 10 * time.Second}

	go runWebhook(app.store, os.Getenv("WEBHOOK_URL"))
	go func() {
		t := time.NewTicker(10 * time.Second)
		for range t.C {
			app.store.CheckOffline()
		}
	}()
	go func() {
		t := time.NewTicker(60 * time.Second)
		for range t.C {
			app.store.mu.RLock()
			d := app.store.dirty
			app.store.mu.RUnlock()
			if d {
				if err := app.store.Save(); err != nil {
					log.Println("erro ao salvar estado:", err)
				}
			}
		}
	}()

	go func() {
		log.Printf("lynx-server %s escutando em %s", version, addr)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	if err := app.store.Save(); err != nil {
		log.Println("erro ao salvar estado:", err)
	}
}

func cacheHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/vendor/") || strings.HasPrefix(r.URL.Path, "/fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}

// guard protege o painel com login (DASH_USER/DASH_PASS). Exceções:
//   - ingestão, instalador, download e healthz (têm proteção própria ou não expõem dados);
//   - arquivos estáticos (css/js/fontes) que não contêm dados;
//   - modo TV: GET na página e em /api/overview e /api/alerts com ?key=TV_KEY (somente leitura).
func (a *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if a.dashUser == "" || p == "/healthz" || p == "/api/ingest" || p == "/install.sh" || p == "/install.ps1" ||
			strings.HasPrefix(p, "/download/") || strings.HasPrefix(p, "/vendor/") || strings.HasPrefix(p, "/fonts/") ||
			p == "/style.css" || p == "/app.js" || p == "/favicon.png" || p == "/logo.png" {
			next.ServeHTTP(w, r)
			return
		}
		if u, pw, ok := r.BasicAuth(); ok &&
			subtle.ConstantTimeCompare([]byte(u), []byte(a.dashUser)) == 1 &&
			subtle.ConstantTimeCompare([]byte(pw), []byte(a.dashPass)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if a.tvKey != "" && r.Method == http.MethodGet && (p == "/" || p == "/index.html" || p == "/api/overview" || p == "/api/alerts") {
			if k := r.URL.Query().Get("key"); subtle.ConstantTimeCompare([]byte(k), []byte(a.tvKey)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="Lynx", charset="UTF-8"`)
		http.Error(w, "autenticação necessária", http.StatusUnauthorized)
	})
}
