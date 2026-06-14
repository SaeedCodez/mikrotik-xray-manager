package handlers

import (
	"net/http"
	"time"

	"xray-manager/internal/models"
	"xray-manager/internal/storage"
	"xray-manager/internal/subscription"
	"xray-manager/internal/util"
)

// ListSubscriptions returns all subscriptions.
func (a *App) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs := a.store.Subscriptions()
	if subs == nil {
		subs = []models.Subscription{}
	}
	writeJSON(w, http.StatusOK, subs)
}

// AddSubscription stores a new subscription (name + url).
func (a *App) AddSubscription(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" || body.URL == "" {
		writeError(w, http.StatusBadRequest, "name and url are required")
		return
	}
	sub := models.Subscription{
		ID:        util.NewID(),
		Name:      body.Name,
		URL:       body.URL,
		LastFetch: time.Time{},
	}
	if err := a.store.AddSubscription(sub); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

// DeleteSubscription removes a subscription; ?keepProxies=false also removes its proxies.
func (a *App) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	keep := r.URL.Query().Get("keepProxies") != "false" // default: keep

	removed := 0
	if !keep {
		n, err := a.store.DeleteProxiesBySub(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		removed = n
	}
	if err := a.store.DeleteSubscription(id); err != nil {
		if err == storage.ErrNotFound {
			writeError(w, http.StatusNotFound, "subscription not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "removedProxies": removed})
}

// RefreshSubscription fetches a subscription URL and reconciles its proxies.
func (a *App) RefreshSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := a.refreshOne(id)
	if err != nil {
		if err == storage.ErrNotFound {
			writeError(w, http.StatusNotFound, "subscription not found")
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// RefreshAllSubscriptions refreshes every subscription sequentially.
func (a *App) RefreshAllSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs := a.store.Subscriptions()
	results := make([]refreshResult, 0, len(subs))
	for _, s := range subs {
		res, err := a.refreshOne(s.ID)
		if err != nil {
			results = append(results, refreshResult{ID: s.ID, Name: s.Name, Error: err.Error()})
			continue
		}
		results = append(results, res)
	}
	writeJSON(w, http.StatusOK, results)
}

type refreshResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Count   int    `json:"count"`
	Error   string `json:"error,omitempty"`
}

func (a *App) refreshOne(id string) (refreshResult, error) {
	sub, err := a.store.Subscription(id)
	if err != nil {
		return refreshResult{}, err
	}
	proxies, fetchErr := subscription.FetchAndParse(sub.URL)
	if fetchErr != nil {
		// Record the failure but keep existing proxies.
		_ = a.store.UpdateSubscription(id, func(s *models.Subscription) {
			s.Error = fetchErr.Error()
			s.LastFetch = time.Now()
		})
		return refreshResult{}, fetchErr
	}

	// Tag every fetched proxy with this subscription.
	for i := range proxies {
		proxies[i].SubscriptionID = id
	}
	added, removed, err := a.store.ReplaceSubProxies(id, proxies)
	if err != nil {
		return refreshResult{}, err
	}
	count := len(proxies)
	_ = a.store.UpdateSubscription(id, func(s *models.Subscription) {
		s.Error = ""
		s.Count = count
		s.LastFetch = time.Now()
	})
	return refreshResult{ID: id, Name: sub.Name, Added: added, Removed: removed, Count: count}, nil
}
