// Command xray-manager serves a web UI for managing Xray-core proxy configs.
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"xray-manager/internal/auth"
	"xray-manager/internal/config"
	"xray-manager/internal/handlers"
	"xray-manager/internal/storage"
	"xray-manager/internal/xray"
)

//go:embed all:web/static
var staticFiles embed.FS

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[xray-manager] ")

	cfg := config.Load()

	store, err := storage.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	authMgr := auth.New(cfg.Password, cfg.SessionSecret)
	xrayMgr := xray.NewManager(cfg.XrayBinary, cfg.XrayConfigPath, cfg.InboundSocks, cfg.InboundHTTP)

	// Restore persisted DNS settings so generated configs match the UI.
	xrayMgr.SetDNS(store.Settings().DNSServers)

	// Restore persisted routing rules so generated configs match the UI.
	xrayMgr.SetRoutingRules(store.RoutingRules())

	// Restore last-known active proxy into the manager for status display.
	if active := store.Active().ProxyID; active != "" {
		xrayMgr.SetActiveID(active)
	}
	if !xrayMgr.BinaryAvailable() {
		log.Printf("WARNING: xray binary not found at %q — process controls will be disabled until it exists.", cfg.XrayBinary)
	}

	staticFS, err := fs.Sub(staticFiles, "web/static")
	if err != nil {
		log.Fatalf("embed: %v", err)
	}

	app := handlers.New(cfg, store, authMgr, xrayMgr, staticFS)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown.
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		log.Println("shutting down…")

		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("http shutdown: %v", err)
		}
		if err := xrayMgr.Stop(); err != nil {
			log.Printf("xray stop: %v", err)
		}
	}()

	log.Printf("listening on http://0.0.0.0:%d  (data: %s)", cfg.Port, cfg.DataDir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
	log.Println("bye")
}
