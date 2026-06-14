package handlers

import (
	"net/http"

	"xray-manager/internal/models"
	"xray-manager/internal/parser"
	"xray-manager/internal/storage"
)

// ListProxies returns all proxies with IsActive populated.
func (a *App) ListProxies(w http.ResponseWriter, r *http.Request) {
	activeID := a.store.Active().ProxyID
	proxies := a.store.Proxies()
	for i := range proxies {
		proxies[i].IsActive = proxies[i].ID == activeID
	}
	if proxies == nil {
		proxies = []models.Proxy{}
	}
	writeJSON(w, http.StatusOK, proxies)
}

// AddProxy parses a single raw share-link and stores it.
func (a *App) AddProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RawURL string `json:"raw_url"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p, err := parser.Parse(body.RawURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.store.AddProxy(p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// ImportProxies parses a batch of raw links, skipping malformed ones.
func (a *App) ImportProxies(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URLs []string `json:"urls"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var added []models.Proxy
	for _, raw := range body.URLs {
		if p, err := parser.Parse(raw); err == nil {
			added = append(added, p)
		}
	}
	if len(added) > 0 {
		if err := a.store.AddProxies(added); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if added == nil {
		added = []models.Proxy{}
	}
	writeJSON(w, http.StatusCreated, added)
}

// DeleteProxy removes a proxy by ID.
func (a *App) DeleteProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.store.DeleteProxy(id); err != nil {
		if err == storage.ErrNotFound {
			writeError(w, http.StatusNotFound, "proxy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
