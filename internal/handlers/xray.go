package handlers

import (
	"fmt"
	"net/http"
	"time"

	"xray-manager/internal/models"
)

// statusResponse augments xray.Status with names/ports for the UI.
type statusResponse struct {
	Running        bool   `json:"running"`
	ActiveProxyID  string `json:"activeProxyId"`
	ActiveName     string `json:"activeName"`
	ActiveEndpoint string `json:"activeEndpoint"`
	PID            int    `json:"pid"`
	Uptime         int64  `json:"uptime"`
	BinaryOK       bool   `json:"binaryOk"`
	Warning        string `json:"warning,omitempty"`
	SocksPort      int    `json:"socksPort"`
	HTTPPort       int    `json:"httpPort"`
}

// XrayStatus returns the current process + active-proxy status.
func (a *App) XrayStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.buildStatus())
}

func (a *App) buildStatus() statusResponse {
	st := a.xray.Status()
	activeID := a.store.Active().ProxyID
	resp := statusResponse{
		Running:       st.Running,
		ActiveProxyID: activeID,
		PID:           st.PID,
		Uptime:        st.UptimeSeconds,
		BinaryOK:      st.BinaryOK,
		Warning:       st.Warning,
		SocksPort:     a.xray.SocksPort(),
		HTTPPort:      a.xray.HTTPPort(),
	}
	if activeID != "" {
		if p, err := a.store.Proxy(activeID); err == nil {
			resp.ActiveName = p.Name
			resp.ActiveEndpoint = fmt.Sprintf("%s:%d", p.Address, p.Port)
		}
	}
	return resp
}

// ActivateProxy makes a proxy active and applies it (restart if running).
func (a *App) ActivateProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := a.store.Proxy(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "proxy not found")
		return
	}
	if err := a.store.SetActive(models.ActiveProxy{ProxyID: id, SetAt: time.Now()}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.xray.Activate(&p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": a.buildStatus()})
}

// StartXray writes the active proxy's config and starts the process.
func (a *App) StartXray(w http.ResponseWriter, r *http.Request) {
	activeID := a.store.Active().ProxyID
	if activeID == "" {
		writeError(w, http.StatusBadRequest, "no active proxy — select one first")
		return
	}
	p, err := a.store.Proxy(activeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "active proxy no longer exists")
		return
	}
	if err := a.xray.WriteConfig(&p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.xray.SetActiveID(activeID)
	if err := a.xray.Start(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": a.buildStatus()})
}

// RestartXray restarts the process.
func (a *App) RestartXray(w http.ResponseWriter, r *http.Request) {
	if err := a.xray.Restart(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": a.buildStatus()})
}

// StopXray stops the process.
func (a *App) StopXray(w http.ResponseWriter, r *http.Request) {
	if err := a.xray.Stop(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": a.buildStatus()})
}

// XrayLogs streams xray log lines over SSE (recent buffer first, then live).
func (a *App) XrayLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Replay recent buffer.
	for _, line := range a.xray.RecentLogs() {
		writeSSE(w, line)
	}
	flusher.Flush()

	ch, cancel := a.xray.Subscribe()
	defer cancel()

	// Heartbeat keeps the connection alive through proxies.
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, line)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, line string) {
	// JSON-encode so newlines/quotes are safe in the data field.
	fmt.Fprintf(w, "data: %s\n\n", jsonString(line))
}

func jsonString(s string) string {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for _, r := range s {
		switch r {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			b = append(b, string(r)...)
		}
	}
	b = append(b, '"')
	return string(b)
}
