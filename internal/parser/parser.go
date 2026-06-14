// Package parser turns share-link URLs (vmess/vless/trojan/ss/hy2) into Proxy structs.
package parser

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xray-manager/internal/models"
	"xray-manager/internal/util"
)

// Parse detects the scheme of raw and dispatches to the matching parser.
func Parse(raw string) (models.Proxy, error) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "vmess://"):
		return parseVMess(raw)
	case strings.HasPrefix(lower, "vless://"):
		return parseVLESS(raw)
	case strings.HasPrefix(lower, "trojan://"):
		return parseTrojan(raw)
	case strings.HasPrefix(lower, "ss://"):
		return parseShadowsocks(raw)
	case strings.HasPrefix(lower, "hy2://"), strings.HasPrefix(lower, "hysteria2://"):
		return parseHysteria2(raw)
	default:
		return models.Proxy{}, fmt.Errorf("unrecognized scheme in %q", truncate(raw, 24))
	}
}

// base64 decoding that tolerates standard/url encodings and missing padding.
func b64decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.StdEncoding.DecodeString(s)
}

// ---------- vmess ----------

type vmessLink struct {
	V    string      `json:"v"`
	PS   string      `json:"ps"`
	Add  string      `json:"add"`
	Port interface{} `json:"port"`
	ID   string      `json:"id"`
	Aid  interface{} `json:"aid"`
	Scy  string      `json:"scy"`
	Net  string      `json:"net"`
	Type string      `json:"type"`
	Host string      `json:"host"`
	Path string      `json:"path"`
	TLS  string      `json:"tls"`
	SNI  string      `json:"sni"`
}

func parseVMess(raw string) (models.Proxy, error) {
	payload := raw[len("vmess://"):]
	decoded, err := b64decode(payload)
	if err != nil {
		return models.Proxy{}, fmt.Errorf("vmess: base64 decode failed: %w", err)
	}
	var v vmessLink
	if err := json.Unmarshal(decoded, &v); err != nil {
		return models.Proxy{}, fmt.Errorf("vmess: invalid JSON payload: %w", err)
	}
	if v.Add == "" {
		return models.Proxy{}, fmt.Errorf("vmess: missing address")
	}
	p := base(models.ProtoVMess)
	p.Name = firstNonEmpty(v.PS, v.Add)
	p.Address = v.Add
	p.Port = toInt(v.Port)
	p.UUID = v.ID
	p.AlterID = toInt(v.Aid)
	p.Network = firstNonEmpty(strings.ToLower(v.Net), "tcp")
	p.Host = v.Host
	p.Path = v.Path
	p.Security = firstNonEmpty(v.Scy, "auto")
	p.TLS = strings.EqualFold(v.TLS, "tls")
	p.SNI = firstNonEmpty(v.SNI, v.Host)
	return p, nil
}

// ---------- vless ----------

func parseVLESS(raw string) (models.Proxy, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return models.Proxy{}, fmt.Errorf("vless: %w", err)
	}
	p := base(models.ProtoVLESS)
	p.UUID = u.User.Username()
	p.Address = u.Hostname()
	p.Port = atoiDefault(u.Port(), 443)
	q := u.Query()
	p.Network = firstNonEmpty(q.Get("type"), "tcp")
	p.Flow = q.Get("flow")
	p.SNI = firstNonEmpty(q.Get("sni"), q.Get("peer"))
	p.Host = firstNonEmpty(q.Get("host"), p.SNI)
	p.Path = firstNonEmpty(q.Get("path"), q.Get("serviceName"))
	p.PublicKey = q.Get("pbk")
	p.ShortID = q.Get("sid")
	p.Fingerprint = q.Get("fp")
	sec := strings.ToLower(q.Get("security"))
	p.Security = sec
	p.TLS = sec == "tls" || sec == "reality" || sec == "xtls"
	p.Name = firstNonEmpty(decodeFragment(u.Fragment), p.Address)
	if p.Address == "" {
		return models.Proxy{}, fmt.Errorf("vless: missing host")
	}
	return p, nil
}

// ---------- trojan ----------

func parseTrojan(raw string) (models.Proxy, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return models.Proxy{}, fmt.Errorf("trojan: %w", err)
	}
	p := base(models.ProtoTrojan)
	p.Password = u.User.Username()
	p.Address = u.Hostname()
	p.Port = atoiDefault(u.Port(), 443)
	q := u.Query()
	p.Network = firstNonEmpty(q.Get("type"), "tcp")
	p.SNI = firstNonEmpty(q.Get("sni"), q.Get("peer"))
	p.Host = firstNonEmpty(q.Get("host"), p.SNI)
	p.Path = firstNonEmpty(q.Get("path"), q.Get("serviceName"))
	p.Fingerprint = q.Get("fp")
	sec := strings.ToLower(q.Get("security"))
	// Trojan is TLS by default unless explicitly "none".
	p.TLS = sec != "none"
	p.Security = firstNonEmpty(sec, "tls")
	p.Name = firstNonEmpty(decodeFragment(u.Fragment), p.Address)
	if p.Address == "" {
		return models.Proxy{}, fmt.Errorf("trojan: missing host")
	}
	return p, nil
}

// ---------- shadowsocks ----------

func parseShadowsocks(raw string) (models.Proxy, error) {
	body := raw[len("ss://"):]
	name := ""
	if i := strings.Index(body, "#"); i >= 0 {
		name = decodeFragment(body[i+1:])
		body = body[:i]
	}
	// Strip any plugin query string (e.g. ?plugin=...).
	plugin := ""
	if i := strings.Index(body, "?"); i >= 0 {
		plugin = body[i+1:]
		body = body[:i]
	}

	var method, password, host string
	var port int

	if at := strings.LastIndex(body, "@"); at >= 0 {
		// Format: ss://BASE64(method:password)@host:port
		userinfo := body[:at]
		hostport := body[at+1:]
		dec, err := b64decode(userinfo)
		if err != nil {
			// Some links leave method:password in plain text.
			dec = []byte(userinfo)
		}
		mp := strings.SplitN(string(dec), ":", 2)
		if len(mp) != 2 {
			return models.Proxy{}, fmt.Errorf("ss: malformed method:password")
		}
		method, password = mp[0], mp[1]
		host, port, err = splitHostPort(hostport)
		if err != nil {
			return models.Proxy{}, fmt.Errorf("ss: %w", err)
		}
	} else {
		// Format: ss://BASE64(method:password@host:port)
		dec, err := b64decode(body)
		if err != nil {
			return models.Proxy{}, fmt.Errorf("ss: base64 decode failed: %w", err)
		}
		s := string(dec)
		at := strings.LastIndex(s, "@")
		if at < 0 {
			return models.Proxy{}, fmt.Errorf("ss: malformed payload")
		}
		mp := strings.SplitN(s[:at], ":", 2)
		if len(mp) != 2 {
			return models.Proxy{}, fmt.Errorf("ss: malformed method:password")
		}
		method, password = mp[0], mp[1]
		host, port, err = splitHostPort(s[at+1:])
		if err != nil {
			return models.Proxy{}, fmt.Errorf("ss: %w", err)
		}
	}

	p := base(models.ProtoShadowsocks)
	p.Method = method
	p.Password = password
	p.Address = host
	p.Port = port
	p.Network = "tcp"
	if strings.Contains(plugin, "obfs") {
		p.Host = pluginValue(plugin, "obfs-host")
	}
	p.Name = firstNonEmpty(name, host)
	if host == "" {
		return models.Proxy{}, fmt.Errorf("ss: missing host")
	}
	return p, nil
}

// ---------- hysteria2 ----------

func parseHysteria2(raw string) (models.Proxy, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return models.Proxy{}, fmt.Errorf("hy2: %w", err)
	}
	p := base(models.ProtoHysteria2)
	// Password may be in userinfo (user) or user:pass.
	if pw, ok := u.User.Password(); ok {
		p.Password = pw
	} else {
		p.Password = u.User.Username()
	}
	p.Address = u.Hostname()
	p.Port = atoiDefault(u.Port(), 443)
	q := u.Query()
	p.SNI = firstNonEmpty(q.Get("sni"), q.Get("peer"))
	p.Host = q.Get("obfs")
	p.TLS = true
	insecure := q.Get("insecure")
	p.Fingerprint = q.Get("pinSHA256")
	p.Security = "tls"
	if insecure == "1" || strings.EqualFold(insecure, "true") {
		p.Security = "tls-insecure"
	}
	p.Name = firstNonEmpty(decodeFragment(u.Fragment), p.Address)
	if p.Address == "" {
		return models.Proxy{}, fmt.Errorf("hy2: missing host")
	}
	return p, nil
}

// ---------- helpers ----------

func base(proto models.Protocol) models.Proxy {
	return models.Proxy{
		ID:       util.NewID(),
		Protocol: proto,
		AddedAt:  time.Now(),
	}
}

func splitHostPort(hp string) (string, int, error) {
	hp = strings.TrimSpace(hp)
	i := strings.LastIndex(hp, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("missing port in %q", hp)
	}
	host := strings.Trim(hp[:i], "[]") // tolerate IPv6 brackets
	port, err := strconv.Atoi(hp[i+1:])
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q", hp[i+1:])
	}
	return host, port, nil
}

func pluginValue(plugin, key string) string {
	for _, part := range strings.Split(plugin, ";") {
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 && kv[0] == key {
			return kv[1]
		}
	}
	return ""
}

func decodeFragment(s string) string {
	if d, err := url.QueryUnescape(s); err == nil {
		return strings.TrimSpace(d)
	}
	return strings.TrimSpace(s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(n))
		return i
	default:
		return 0
	}
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
