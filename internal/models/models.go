// Package models defines the core data structures shared across the app.
package models

import "time"

// Protocol identifies the proxy transport scheme.
type Protocol string

const (
	ProtoVMess       Protocol = "vmess"
	ProtoVLESS       Protocol = "vless"
	ProtoTrojan      Protocol = "trojan"
	ProtoShadowsocks Protocol = "ss"
	ProtoHysteria2   Protocol = "hy2"
)

// Proxy is a single outbound proxy server configuration.
type Proxy struct {
	ID       string   `json:"id"`   // UUID
	Name     string   `json:"name"` // Display name
	Protocol Protocol `json:"protocol"`
	Address  string   `json:"address"`
	Port     int      `json:"port"`

	// Protocol-specific fields
	UUID        string `json:"uuid,omitempty"`
	Password    string `json:"password,omitempty"`
	Method      string `json:"method,omitempty"`  // For SS
	Network     string `json:"network,omitempty"` // tcp/ws/grpc/h2
	TLS         bool   `json:"tls"`
	SNI         string `json:"sni,omitempty"`
	Path        string `json:"path,omitempty"` // WS path / gRPC serviceName
	Host        string `json:"host,omitempty"`
	Flow        string `json:"flow,omitempty"`      // VLESS XTLS
	PublicKey   string `json:"publicKey,omitempty"` // VLESS Reality
	ShortID     string `json:"shortId,omitempty"`   // VLESS Reality
	Fingerprint string `json:"fingerprint,omitempty"`
	AlterID     int    `json:"alterId,omitempty"`  // VMess
	Security    string `json:"security,omitempty"` // VMess cipher / reality|tls flag

	// Metadata
	SubscriptionID string    `json:"subscriptionId,omitempty"` // Source subscription
	AddedAt        time.Time `json:"addedAt"`

	// Runtime (persisted so the UI keeps last-known latency across restarts)
	Latency    int64      `json:"latency"` // ms, -1 = error/timeout, 0 = untested
	IsActive   bool       `json:"isActive"`
	LastTested *time.Time `json:"lastTested,omitempty"`
}

// Subscription is a remote URL that yields a batch of proxies.
type Subscription struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	LastFetch time.Time `json:"lastFetch"`
	Count     int       `json:"count"` // Number of proxies fetched
	Error     string    `json:"error,omitempty"`
}

// ActiveProxy records which proxy is currently wired into the xray config.
type ActiveProxy struct {
	ProxyID string    `json:"proxyId"`
	SetAt   time.Time `json:"setAt"`
}

// Settings holds user-adjustable runtime settings persisted across restarts.
type Settings struct {
	// DNSServers are the resolvers written into the xray "dns" block. Each entry
	// may be a plain IP ("1.1.1.1"), "localhost", or a scheme'd resolver such as
	// "https://1.1.1.1/dns-query" or "tcp://8.8.8.8".
	DNSServers []string `json:"dnsServers"`
}
