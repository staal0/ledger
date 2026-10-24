// Ledger is a small self-hosted household budget app.
package main

import (
	"database/sql"
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed locales migrations static templates
var embedded embed.FS

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type Config struct {
	Addr         string
	BaseURL      string
	DSN          string
	LocalesDir   string
	OIDCIssuer   string
	OIDCClientID string
	OIDCSecret   string
}

type App struct {
	cfg     Config
	db      *sql.DB
	locales map[string]*Locale
	pages   map[string]*template.Template
	auth    oidcClient
}

func main() {
	cfg := Config{
		BaseURL:      strings.TrimSuffix(env("LEDGER_BASE_URL", "http://localhost:8080"), "/"),
		DSN:          os.Getenv("LEDGER_DB_DSN"),
		LocalesDir:   os.Getenv("LEDGER_LOCALES_DIR"),
		OIDCIssuer:   os.Getenv("OIDC_ISSUER"),
		OIDCClientID: os.Getenv("OIDC_CLIENT_ID"),
		OIDCSecret:   os.Getenv("OIDC_CLIENT_SECRET"),
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(env("LEDGER_ADDR", ":8080")))
	}
	if cfg.DSN == "" {
		log.Fatal("LEDGER_DB_DSN must be set")
	}
	if len(os.Args) > 1 {
		if os.Args[1] != "demo" {
			log.Fatalf("unknown command %q; usage: ledger [demo [en|da] | healthcheck]", os.Args[1])
		}
		lang := "en"
		if len(os.Args) > 2 {
			lang = os.Args[2]
		}
		runDemo(cfg.DSN, lang)
		return
	}
	local := cfg.OIDCIssuer == ""
	if local {
		// Local mode: no login, so only listen where nobody else can reach it.
		cfg.Addr = env("LEDGER_ADDR", "127.0.0.1:8080")
		host, _, _ := net.SplitHostPort(cfg.Addr)
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			log.Fatal("without OIDC_ISSUER, LEDGER_ADDR must be a loopback address such as 127.0.0.1:8080")
		}
		log.Print("OIDC_ISSUER is not set: running in local mode without login")
	} else {
		cfg.Addr = env("LEDGER_ADDR", ":8080")
		if cfg.OIDCClientID == "" {
			log.Fatal("OIDC_CLIENT_ID must be set")
		}
	}

	db, err := openDB(cfg.DSN)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	if err := migrateAll(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if local {
		if _, err := db.Exec("INSERT IGNORE INTO users (subject, name) VALUES ('local', 'Local')"); err != nil {
			log.Fatalf("local user: %v", err)
		}
	}
	locales, err := loadLocales(cfg.LocalesDir)
	if err != nil {
		log.Fatalf("locales: %v", err)
	}
	pages, err := parsePages()
	if err != nil {
		log.Fatalf("templates: %v", err)
	}

	app := &App{cfg: cfg, db: db, locales: locales, pages: pages}
	srv := &http.Server{Addr: cfg.Addr, Handler: app.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("ledger listening on %s", cfg.Addr)
	log.Fatal(srv.ListenAndServe())
}

func (a *App) routes() http.Handler {
	static, _ := fs.Sub(embedded, "static")
	public := http.NewServeMux()
	public.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	// Versioned URLs from asset(): the content never changes for a given URL.
	public.HandleFunc("GET /static/v/{hash}/{name}", func(w http.ResponseWriter, r *http.Request) {
		// Any other hash would cache the current file for a year under an outdated URL.
		if h, ok := assetHashes[r.PathValue("name")]; !ok || h != r.PathValue("hash") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFileFS(w, r, static, r.PathValue("name"))
	})
	public.HandleFunc("GET /healthz", a.healthz)
	public.HandleFunc("GET /login", a.login)
	public.HandleFunc("GET /auth/callback", a.callback)
	public.HandleFunc("POST /logout", a.logout)
	public.HandleFunc("GET /logged-out", a.loggedOut)

	private := http.NewServeMux()
	private.HandleFunc("GET /{$}", a.budgetIndex)
	private.HandleFunc("GET /budget/{$}", a.budgetIndex)
	private.HandleFunc("GET /budget/{year}", a.budgetPage)
	private.HandleFunc("POST /budget/{year}", a.budgetSave)
	private.HandleFunc("POST /budget/{year}/create", a.budgetCreate)
	private.HandleFunc("GET /budget/{year}/export", a.budgetExport)
	private.HandleFunc("GET /stats", a.statsPage)
	private.HandleFunc("GET /settings", a.settingsPage)
	private.HandleFunc("POST /settings", a.settingsSave)
	private.HandleFunc("POST /theme", a.themeSave)
	for _, p := range plugins {
		p.Routes(private, a)
	}
	public.Handle("/", a.requireUser(private))

	return http.NewCrossOriginProtection().Handler(public)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
