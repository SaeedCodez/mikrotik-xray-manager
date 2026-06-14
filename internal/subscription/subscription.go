// Package subscription fetches subscription URLs and parses them into proxies.
package subscription

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"xray-manager/internal/models"
	"xray-manager/internal/parser"
)

var client = &http.Client{Timeout: 15 * time.Second}

// FetchAndParse downloads url and parses every line into a proxy. Lines that
// fail to parse are skipped (partial failures don't fail the whole fetch).
func FetchAndParse(url string) ([]models.Proxy, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "v2rayN/6.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if err != nil {
		return nil, err
	}

	text := strings.TrimSpace(string(body))
	// Subscriptions are commonly base64-encoded as a whole; try to decode.
	if decoded, ok := tryBase64(text); ok {
		text = decoded
	}

	var proxies []models.Proxy
	for _, line := range strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r'
	}) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p, err := parser.Parse(line)
		if err != nil {
			continue // skip malformed entries
		}
		proxies = append(proxies, p)
	}
	return proxies, nil
}

// tryBase64 returns the decoded text if s decodes cleanly and looks like links.
func tryBase64(s string) (string, bool) {
	clean := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(s)
	clean = strings.ReplaceAll(clean, "-", "+")
	clean = strings.ReplaceAll(clean, "_", "/")
	if m := len(clean) % 4; m != 0 {
		clean += strings.Repeat("=", 4-m)
	}
	dec, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", false
	}
	out := string(dec)
	if strings.Contains(out, "://") {
		return out, true
	}
	return "", false
}
