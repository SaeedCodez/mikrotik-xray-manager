package handlers

import (
	"net/http"
	"strings"
)

// GetDNS returns the DNS servers used when generating the xray config.
func (a *App) GetDNS(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"servers": a.xray.DNS()})
}

// UpdateDNS persists a new DNS server list, re-applies it to the active config,
// and restarts xray when it is running so the change takes effect immediately.
func (a *App) UpdateDNS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Servers []string `json:"servers"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	cleaned := cleanDNSServers(body.Servers)
	if len(cleaned) == 0 {
		writeError(w, http.StatusBadRequest, "add at least one DNS server")
		return
	}
	if len(cleaned) > 10 {
		writeError(w, http.StatusBadRequest, "too many DNS servers (max 10)")
		return
	}

	if err := a.store.SetDNSServers(cleaned); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.xray.SetDNS(cleaned)

	// Re-generate the live config and restart if a proxy is active/running so the
	// new resolvers apply without the user toggling anything else.
	restarted := false
	if activeID := a.store.Active().ProxyID; activeID != "" {
		if p, err := a.store.Proxy(activeID); err == nil {
			if err := a.xray.WriteConfig(&p); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if a.xray.IsRunning() {
				if err := a.xray.Restart(); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				restarted = true
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"servers":   cleaned,
		"restarted": restarted,
		"status":    a.buildStatus(),
	})
}

// cleanDNSServers trims, de-duplicates, and drops obviously-invalid entries
// (blank, whitespace-bearing, or absurdly long) while preserving order.
func cleanDNSServers(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 200 || strings.ContainsAny(s, " \t\r\n") {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
