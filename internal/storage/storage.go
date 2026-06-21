// Package storage persists app state as JSON files guarded by a mutex.
package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"xray-manager/internal/models"
)

// Store is a thread-safe JSON-file-backed data store.
type Store struct {
	mu      sync.RWMutex
	dataDir string

	proxies  []models.Proxy
	subs     []models.Subscription
	active   models.ActiveProxy
	settings models.Settings
	rules    []models.RoutingRule
}

// ErrNotFound is returned when an entity ID does not exist.
var ErrNotFound = errors.New("not found")

// DefaultDNSServers is used when no DNS configuration has been saved yet.
func DefaultDNSServers() []string { return []string{"1.1.1.1", "1.0.0.1", "8.8.8.8"} }

// New opens (and if necessary creates) the data files under dataDir.
func New(dataDir string) (*Store, error) {
	s := &Store{dataDir: dataDir}
	if err := os.MkdirAll(filepath.Join(dataDir, "xray"), 0o755); err != nil {
		return nil, err
	}
	if err := s.load(&s.proxies, s.path("proxies.json"), []models.Proxy{}); err != nil {
		return nil, err
	}
	if err := s.load(&s.subs, s.path("subscriptions.json"), []models.Subscription{}); err != nil {
		return nil, err
	}
	if err := s.load(&s.active, s.path("active.json"), models.ActiveProxy{}); err != nil {
		return nil, err
	}
	if err := s.load(&s.settings, s.path("settings.json"), models.Settings{DNSServers: DefaultDNSServers()}); err != nil {
		return nil, err
	}
	if len(s.settings.DNSServers) == 0 {
		s.settings.DNSServers = DefaultDNSServers()
	}
	if err := s.load(&s.rules, s.path("routing.json"), []models.RoutingRule{}); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.dataDir, name) }

// load reads a JSON file into dst; if the file is missing it writes the default.
func (s *Store) load(dst any, path string, def any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := writeJSON(path, def); err != nil {
			return err
		}
		data, err = json.Marshal(def)
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------- Proxies ----------

// Proxies returns a copy of all proxies.
func (s *Store) Proxies() []models.Proxy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Proxy, len(s.proxies))
	copy(out, s.proxies)
	return out
}

// Proxy returns a single proxy by ID.
func (s *Store) Proxy(id string) (models.Proxy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.proxies {
		if p.ID == id {
			return p, nil
		}
	}
	return models.Proxy{}, ErrNotFound
}

// AddProxy appends a proxy and persists.
func (s *Store) AddProxy(p models.Proxy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxies = append(s.proxies, p)
	return s.persistProxies()
}

// AddProxies appends many proxies and persists once.
func (s *Store) AddProxies(ps []models.Proxy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxies = append(s.proxies, ps...)
	return s.persistProxies()
}

// UpdateProxy applies fn to the proxy with the given ID and persists.
func (s *Store) UpdateProxy(id string, fn func(*models.Proxy)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.proxies {
		if s.proxies[i].ID == id {
			fn(&s.proxies[i])
			return s.persistProxies()
		}
	}
	return ErrNotFound
}

// DeleteProxy removes a proxy by ID and persists.
func (s *Store) DeleteProxy(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.proxies[:0]
	found := false
	for _, p := range s.proxies {
		if p.ID == id {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return ErrNotFound
	}
	s.proxies = out
	if s.active.ProxyID == id {
		s.active = models.ActiveProxy{}
		_ = writeJSON(s.path("active.json"), s.active)
	}
	return s.persistProxies()
}

// DeleteProxiesBySub removes every proxy that came from subID. Returns count removed.
func (s *Store) DeleteProxiesBySub(subID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.proxies[:0]
	removed := 0
	for _, p := range s.proxies {
		if p.SubscriptionID == subID {
			removed++
			if s.active.ProxyID == p.ID {
				s.active = models.ActiveProxy{}
				_ = writeJSON(s.path("active.json"), s.active)
			}
			continue
		}
		out = append(out, p)
	}
	s.proxies = out
	return removed, s.persistProxies()
}

// ReplaceSubProxies swaps out all proxies for a subscription with a fresh set,
// preserving runtime latency for endpoints that still exist. Returns added/removed.
func (s *Store) ReplaceSubProxies(subID string, fresh []models.Proxy) (added, removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Index existing proxies for this sub by a stable endpoint key.
	prev := map[string]models.Proxy{}
	var kept []models.Proxy
	for _, p := range s.proxies {
		if p.SubscriptionID == subID {
			prev[endpointKey(p)] = p
		} else {
			kept = append(kept, p)
		}
	}

	for i := range fresh {
		k := endpointKey(fresh[i])
		if old, ok := prev[k]; ok {
			// Carry over runtime + identity so the UI stays stable.
			fresh[i].ID = old.ID
			fresh[i].Latency = old.Latency
			fresh[i].LastTested = old.LastTested
			delete(prev, k)
		} else {
			added++
		}
	}
	removed = len(prev)

	s.proxies = append(kept, fresh...)
	return added, removed, s.persistProxies()
}

func endpointKey(p models.Proxy) string {
	return string(p.Protocol) + "|" + p.Address + "|" + itoa(p.Port) + "|" + p.UUID + p.Password
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func (s *Store) persistProxies() error {
	return writeJSON(s.path("proxies.json"), s.proxies)
}

// ---------- Subscriptions ----------

// Subscriptions returns a copy of all subscriptions.
func (s *Store) Subscriptions() []models.Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Subscription, len(s.subs))
	copy(out, s.subs)
	return out
}

// Subscription returns a single subscription by ID.
func (s *Store) Subscription(id string) (models.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, x := range s.subs {
		if x.ID == id {
			return x, nil
		}
	}
	return models.Subscription{}, ErrNotFound
}

// AddSubscription appends a subscription and persists.
func (s *Store) AddSubscription(sub models.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = append(s.subs, sub)
	return s.persistSubs()
}

// UpdateSubscription applies fn to the subscription with the given ID and persists.
func (s *Store) UpdateSubscription(id string, fn func(*models.Subscription)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.subs {
		if s.subs[i].ID == id {
			fn(&s.subs[i])
			return s.persistSubs()
		}
	}
	return ErrNotFound
}

// DeleteSubscription removes a subscription by ID and persists.
func (s *Store) DeleteSubscription(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.subs[:0]
	found := false
	for _, x := range s.subs {
		if x.ID == id {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		return ErrNotFound
	}
	s.subs = out
	return s.persistSubs()
}

func (s *Store) persistSubs() error {
	return writeJSON(s.path("subscriptions.json"), s.subs)
}

// ---------- Active proxy ----------

// Active returns the currently active proxy record.
func (s *Store) Active() models.ActiveProxy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

// SetActive records the active proxy and persists.
func (s *Store) SetActive(a models.ActiveProxy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = a
	return writeJSON(s.path("active.json"), a)
}

// ---------- Settings ----------

// Settings returns a copy of the persisted settings.
func (s *Store) Settings() models.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.settings
	out.DNSServers = append([]string(nil), s.settings.DNSServers...)
	return out
}

// SetDNSServers replaces the DNS server list and persists.
func (s *Store) SetDNSServers(servers []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.DNSServers = append([]string(nil), servers...)
	return writeJSON(s.path("settings.json"), s.settings)
}

// ---------- Routing Rules ----------

// RoutingRules returns a copy of all routing rules, sorted by priority.
func (s *Store) RoutingRules() []models.RoutingRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.RoutingRule, len(s.rules))
	copy(out, s.rules)
	return out
}

// RoutingRule returns a single routing rule by ID.
func (s *Store) RoutingRule(id string) (models.RoutingRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.rules {
		if r.ID == id {
			return r, nil
		}
	}
	return models.RoutingRule{}, ErrNotFound
}

// AddRoutingRule appends a routing rule and persists.
func (s *Store) AddRoutingRule(r models.RoutingRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, r)
	return s.persistRules()
}

// UpdateRoutingRule applies fn to the routing rule with the given ID and persists.
func (s *Store) UpdateRoutingRule(id string, fn func(*models.RoutingRule)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rules {
		if s.rules[i].ID == id {
			fn(&s.rules[i])
			return s.persistRules()
		}
	}
	return ErrNotFound
}

// DeleteRoutingRule removes a routing rule by ID and persists.
func (s *Store) DeleteRoutingRule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.rules[:0]
	found := false
	for _, r := range s.rules {
		if r.ID == id {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return ErrNotFound
	}
	s.rules = out
	return s.persistRules()
}

// ReplaceRoutingRules replaces all routing rules with a new set and persists.
func (s *Store) ReplaceRoutingRules(rules []models.RoutingRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append([]models.RoutingRule(nil), rules...)
	return s.persistRules()
}

func (s *Store) persistRules() error {
	return writeJSON(s.path("routing.json"), s.rules)
}
