// Package handlers wires HTTP routes to the app's services.
package handlers

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"sync"

	"xray-manager/internal/auth"
	"xray-manager/internal/config"
	"xray-manager/internal/health"
	"xray-manager/internal/storage"
	"xray-manager/internal/xray"
)

// App holds shared dependencies for all handlers.
type App struct {
	cfg    *config.Config
	store  *storage.Store
	auth   *auth.Manager
	xray   *xray.Manager
	prober *health.Prober
	static fs.FS

	// testMu serializes test-all runs (one at a time → 409 otherwise).
	testMu      sync.Mutex
	testRunning bool
}

// New builds the App and its dependencies.
func New(cfg *config.Config, store *storage.Store, am *auth.Manager, xm *xray.Manager, static fs.FS) *App {
	prober := &health.Prober{
		Binary:  cfg.XrayBinary,
		Config:  xray.BuildTestConfig,
		TestURL: cfg.HealthTestURL,
	}
	return &App{cfg: cfg, store: store, auth: am, xray: xm, prober: prober, static: static}
}

// Handler returns the root http.Handler (API + static SPA).
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	// --- public auth routes ---
	mux.HandleFunc("POST /api/auth/login", a.Login)
	mux.HandleFunc("POST /api/auth/logout", a.Logout)
	mux.HandleFunc("GET /api/auth/status", a.AuthStatus)

	// --- protected API routes ---
	api := http.NewServeMux()
	api.HandleFunc("GET /api/proxies", a.ListProxies)
	api.HandleFunc("POST /api/proxies", a.AddProxy)
	api.HandleFunc("POST /api/proxies/import", a.ImportProxies)
	api.HandleFunc("DELETE /api/proxies/{id}", a.DeleteProxy)

	api.HandleFunc("GET /api/subscriptions", a.ListSubscriptions)
	api.HandleFunc("POST /api/subscriptions", a.AddSubscription)
	api.HandleFunc("DELETE /api/subscriptions/{id}", a.DeleteSubscription)
	api.HandleFunc("POST /api/subscriptions/{id}/refresh", a.RefreshSubscription)
	api.HandleFunc("POST /api/subscriptions/refresh-all", a.RefreshAllSubscriptions)

	api.HandleFunc("POST /api/health/test/{id}", a.TestProxy)
	api.HandleFunc("GET /api/health/test-all", a.TestAll) // SSE (EventSource = GET)

	api.HandleFunc("GET /api/xray/status", a.XrayStatus)
	api.HandleFunc("POST /api/xray/activate/{id}", a.ActivateProxy)
	api.HandleFunc("POST /api/xray/restart", a.RestartXray)
	api.HandleFunc("POST /api/xray/start", a.StartXray)
	api.HandleFunc("POST /api/xray/stop", a.StopXray)
	api.HandleFunc("GET /api/xray/logs", a.XrayLogs) // SSE
	api.HandleFunc("GET /api/xray/dns", a.GetDNS)
	api.HandleFunc("PUT /api/xray/dns", a.UpdateDNS)

	api.HandleFunc("POST /api/connection/test", a.TestConnection)

	mux.Handle("/api/", a.auth.Middleware(api))

	// --- static SPA (everything else) ---
	mux.Handle("/", a.spaHandler())

	return logRequests(mux)
}

// spaHandler serves embedded static assets, falling back to index.html.
func (a *App) spaHandler() http.Handler {
	fileServer := http.FileServer(http.FS(a.static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve known files directly; otherwise fall back to the SPA shell.
		if r.URL.Path != "/" {
			if f, err := a.static.Open(trimLeadingSlash(r.URL.Path)); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		serveIndex(w, r, a.static)
	})
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}

func serveIndex(w http.ResponseWriter, r *http.Request, static fs.FS) {
	f, err := static.Open("index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, f)
}

// ---------- response helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(dst)
}

func (a *App) secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
