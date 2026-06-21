package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"xray-manager/internal/models"
	"xray-manager/internal/util"
)

// ListRules returns all routing rules.
func (a *App) ListRules(w http.ResponseWriter, r *http.Request) {
	rules := a.store.RoutingRules()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rules)
}

// CreateRule adds a new routing rule, regenerates config, and restarts xray if running.
func (a *App) CreateRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Priority  int    `json:"priority"`
		Type      string `json:"type"`
		Condition string `json:"condition"`
		Action    string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rule := models.RoutingRule{
		ID:        util.NewID(),
		Name:      req.Name,
		Priority:  req.Priority,
		Type:      models.RoutingRuleType(req.Type),
		Condition: req.Condition,
		Action:    models.RoutingAction(req.Action),
		Enabled:   true,
		CreatedAt: time.Now(),
	}

	if err := a.store.AddRoutingRule(rule); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.xray.SetRoutingRules(a.store.RoutingRules())
	if a.xray.IsRunning() {
		if err := a.xray.Restart(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(rule)
}

// UpdateRule modifies a routing rule, regenerates config, and restarts xray if running.
func (a *App) UpdateRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name      string `json:"name"`
		Priority  int    `json:"priority"`
		Type      string `json:"type"`
		Condition string `json:"condition"`
		Action    string `json:"action"`
		Enabled   bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := a.store.UpdateRoutingRule(id, func(r *models.RoutingRule) {
		r.Name = req.Name
		r.Priority = req.Priority
		r.Type = models.RoutingRuleType(req.Type)
		r.Condition = req.Condition
		r.Action = models.RoutingAction(req.Action)
		r.Enabled = req.Enabled
	})
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	a.xray.SetRoutingRules(a.store.RoutingRules())
	if a.xray.IsRunning() {
		if err := a.xray.Restart(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	rule, _ := a.store.RoutingRule(id)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rule)
}

// DeleteRule removes a routing rule, regenerates config, and restarts xray if running.
func (a *App) DeleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.store.DeleteRoutingRule(id); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	a.xray.SetRoutingRules(a.store.RoutingRules())
	if a.xray.IsRunning() {
		if err := a.xray.Restart(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ReorderRules updates priorities of multiple rules, regenerates config, and restarts xray if running.
func (a *App) ReorderRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rules []struct {
			ID       string `json:"id"`
			Priority int    `json:"priority"`
		} `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	for _, item := range req.Rules {
		a.store.UpdateRoutingRule(item.ID, func(r *models.RoutingRule) {
			r.Priority = item.Priority
		})
	}

	a.xray.SetRoutingRules(a.store.RoutingRules())
	if a.xray.IsRunning() {
		if err := a.xray.Restart(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
