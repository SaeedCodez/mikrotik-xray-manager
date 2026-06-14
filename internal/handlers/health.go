package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"xray-manager/internal/health"
	"xray-manager/internal/models"
)

// TestProxy tests one proxy and persists its latency.
func (a *App) TestProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := a.store.Proxy(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "proxy not found")
		return
	}
	res := health.Test(&p)
	a.persistLatency(id, res.Latency)
	writeJSON(w, http.StatusOK, map[string]any{"latency": res.Latency, "error": res.Error})
}

// TestAll streams latency results over SSE as each proxy is tested.
func (a *App) TestAll(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// Only one test-all at a time.
	a.testMu.Lock()
	if a.testRunning {
		a.testMu.Unlock()
		writeError(w, http.StatusConflict, "a test run is already in progress")
		return
	}
	a.testRunning = true
	a.testMu.Unlock()
	defer func() {
		a.testMu.Lock()
		a.testRunning = false
		a.testMu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	proxies := a.store.Proxies()
	results := make(chan health.Result, len(proxies))
	go health.TestAll(proxies, 10, results)

	for res := range results {
		a.persistLatency(res.ProxyID, res.Latency)
		payload, _ := json.Marshal(res)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		default:
		}
	}
	fmt.Fprint(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}

// persistLatency stores a test result onto the proxy.
func (a *App) persistLatency(id string, latency int64) {
	now := time.Now()
	_ = a.store.UpdateProxy(id, func(p *models.Proxy) {
		p.Latency = latency
		p.LastTested = &now
	})
}
