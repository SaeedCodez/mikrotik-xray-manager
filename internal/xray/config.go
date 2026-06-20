package xray

import (
	"encoding/json"
	"fmt"

	"xray-manager/internal/models"
)

// M is a shorthand for an ordered-irrelevant JSON object.
type M = map[string]interface{}

// GenerateConfig builds a full Xray config with the given proxy as the primary
// outbound plus socks/http inbounds and direct/block fallbacks.
func (m *Manager) GenerateConfig(p *models.Proxy) ([]byte, error) {
	outbound, err := proxyToOutbound(p)
	if err != nil {
		return nil, err
	}
	cfg := M{
		"log": M{"loglevel": "warning"},
		"dns": M{"servers": []interface{}{"1.1.1.1", "1.0.0.1", "8.8.8.8"}},
		"inbounds": []interface{}{
			M{
				"tag":      "socks",
				"port":     m.socksPort,
				"listen":   "0.0.0.0",
				"protocol": "socks",
				"settings": M{"auth": "noauth", "udp": true},
				"sniffing": M{"enabled": true, "destOverride": []interface{}{"http", "tls"}},
			},
			M{
				"tag":      "http",
				"port":     m.httpPort,
				"listen":   "0.0.0.0",
				"protocol": "http",
			},
		},
		"outbounds": []interface{}{
			outbound,
			M{"tag": "direct", "protocol": "freedom"},
			M{"tag": "block", "protocol": "blackhole"},
		},
		"routing": M{
			"domainStrategy": "IPIfNonMatch",
			"rules": []interface{}{
				M{"type": "field", "ip": []interface{}{"geoip:private"}, "outboundTag": "direct"},
			},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// BuildTestConfig generates a minimal config that routes a single HTTP inbound
// (127.0.0.1:httpPort) through p — used to probe real latency through the proxy
// without disturbing the main running instance. It is a plain function so the
// health package can depend on it without importing a Manager.
func BuildTestConfig(p *models.Proxy, httpPort int) ([]byte, error) {
	outbound, err := proxyToOutbound(p)
	if err != nil {
		return nil, err
	}
	cfg := M{
		"log": M{"loglevel": "warning"},
		"inbounds": []interface{}{
			M{
				"tag":      "http",
				"port":     httpPort,
				"listen":   "127.0.0.1",
				"protocol": "http",
			},
		},
		"outbounds": []interface{}{
			outbound,
			M{"tag": "direct", "protocol": "freedom"},
			M{"tag": "block", "protocol": "blackhole"},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// proxyToOutbound produces the Xray outbound object for a proxy's protocol.
func proxyToOutbound(p *models.Proxy) (M, error) {
	switch p.Protocol {
	case models.ProtoVMess:
		return vmessOutbound(p), nil
	case models.ProtoVLESS:
		return vlessOutbound(p), nil
	case models.ProtoTrojan:
		return trojanOutbound(p), nil
	case models.ProtoShadowsocks:
		return ssOutbound(p), nil
	case models.ProtoHysteria2:
		return hysteria2Outbound(p), nil
	default:
		return nil, fmt.Errorf("unsupported protocol %q", p.Protocol)
	}
}

func vmessOutbound(p *models.Proxy) M {
	user := M{"id": p.UUID, "alterId": p.AlterID, "security": orDefault(p.Security, "auto")}
	return M{
		"tag":      "proxy",
		"protocol": "vmess",
		"settings": M{
			"vnext": []interface{}{
				M{"address": p.Address, "port": p.Port, "users": []interface{}{user}},
			},
		},
		"streamSettings": streamSettings(p),
	}
}

func vlessOutbound(p *models.Proxy) M {
	user := M{"id": p.UUID, "encryption": "none"}
	if p.Flow != "" {
		user["flow"] = p.Flow
	}
	return M{
		"tag":      "proxy",
		"protocol": "vless",
		"settings": M{
			"vnext": []interface{}{
				M{"address": p.Address, "port": p.Port, "users": []interface{}{user}},
			},
		},
		"streamSettings": streamSettings(p),
	}
}

func trojanOutbound(p *models.Proxy) M {
	return M{
		"tag":      "proxy",
		"protocol": "trojan",
		"settings": M{
			"servers": []interface{}{
				M{"address": p.Address, "port": p.Port, "password": p.Password},
			},
		},
		"streamSettings": streamSettings(p),
	}
}

func ssOutbound(p *models.Proxy) M {
	return M{
		"tag":      "proxy",
		"protocol": "shadowsocks",
		"settings": M{
			"servers": []interface{}{
				M{"address": p.Address, "port": p.Port, "method": p.Method, "password": p.Password},
			},
		},
		"streamSettings": M{"network": "tcp"},
	}
}

// hysteria2Outbound emits the native hysteria2 outbound (Xray ≥1.8.x).
func hysteria2Outbound(p *models.Proxy) M {
	tls := M{"enabled": true, "serverName": orDefault(p.SNI, p.Address)}
	if p.Security == "tls-insecure" {
		tls["allowInsecure"] = true
	}
	return M{
		"tag":      "proxy",
		"protocol": "hysteria2",
		"settings": M{
			"servers": []interface{}{
				M{"address": p.Address, "port": p.Port, "password": p.Password},
			},
		},
		"streamSettings": M{"network": "tcp", "security": "tls", "tlsSettings": tls},
	}
}

// streamSettings builds the transport + TLS/Reality block shared by v* protocols.
func streamSettings(p *models.Proxy) M {
	network := orDefault(p.Network, "tcp")
	ss := M{"network": network}

	switch network {
	case "ws":
		hdr := M{}
		if p.Host != "" {
			hdr["Host"] = p.Host
		}
		ss["wsSettings"] = M{"path": orDefault(p.Path, "/"), "headers": hdr}
	case "grpc":
		ss["grpcSettings"] = M{"serviceName": p.Path}
	case "h2", "http":
		hosts := []interface{}{}
		if p.Host != "" {
			hosts = append(hosts, p.Host)
		}
		ss["httpSettings"] = M{"path": orDefault(p.Path, "/"), "host": hosts}
	}

	switch p.Security {
	case "reality":
		ss["security"] = "reality"
		ss["realitySettings"] = M{
			"serverName":  orDefault(p.SNI, p.Address),
			"fingerprint": orDefault(p.Fingerprint, "chrome"),
			"publicKey":   p.PublicKey,
			"shortId":     p.ShortID,
		}
	default:
		if p.TLS {
			tls := M{"serverName": orDefault(p.SNI, p.Address)}
			if p.Fingerprint != "" {
				tls["fingerprint"] = p.Fingerprint
			}
			ss["security"] = "tls"
			ss["tlsSettings"] = tls
		}
	}
	return ss
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
