package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// connTestResult is the response of a single connection test routed through the
// running xray HTTP inbound (i.e. through the active proxy).
type connTestResult struct {
	OK            bool              `json:"ok"`
	URL           string            `json:"url"`
	FinalURL      string            `json:"finalUrl,omitempty"`
	Status        int               `json:"status"`
	StatusText    string            `json:"statusText"`
	Proto         string            `json:"proto,omitempty"`
	Latency       int64             `json:"latency"` // ms (time to first byte)
	ContentType   string            `json:"contentType,omitempty"`
	ContentLength int64             `json:"contentLength"`
	Server        string            `json:"server,omitempty"`
	Via           string            `json:"via,omitempty"` // active proxy name
	Headers       map[string]string `json:"headers,omitempty"`
	Error         string            `json:"error,omitempty"`
}

// TestConnection routes a GET request for the given URL through the running xray
// HTTP proxy and reports timing, status, and response metadata.
func (a *App) TestConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	target := normalizeTestURL(body.URL)
	if target == "" {
		writeError(w, http.StatusBadRequest, "enter a URL to test")
		return
	}
	if u, err := url.ParseRequestURI(target); err != nil || u.Host == "" {
		writeError(w, http.StatusBadRequest, "that doesn't look like a valid URL")
		return
	}
	if !a.xray.IsRunning() {
		writeError(w, http.StatusConflict, "xray isn't running — start it first")
		return
	}

	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", a.xray.HTTPPort()))
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			DisableKeepAlives: true,
		},
	}

	res := connTestResult{URL: target, Via: a.activeProxyName()}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	req.Header.Set("User-Agent", "XrayManager-ConnectionTest/1.0")
	req.Header.Set("Accept", "*/*")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		res.Latency = time.Since(start).Milliseconds()
		res.Error = cleanProxyError(err)
		writeJSON(w, http.StatusOK, res)
		return
	}
	defer resp.Body.Close()
	res.Latency = time.Since(start).Milliseconds()

	// Drain a bounded chunk so we can report a size for chunked responses.
	n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	res.OK = resp.StatusCode < 400
	res.Status = resp.StatusCode
	res.StatusText = http.StatusText(resp.StatusCode)
	res.Proto = resp.Proto
	res.ContentType = resp.Header.Get("Content-Type")
	res.Server = resp.Header.Get("Server")
	res.ContentLength = resp.ContentLength
	if res.ContentLength < 0 {
		res.ContentLength = n
	}
	if resp.Request != nil && resp.Request.URL != nil {
		res.FinalURL = resp.Request.URL.String()
	}
	res.Headers = flattenHeaders(resp.Header)

	writeJSON(w, http.StatusOK, res)
}

// activeProxyName returns the display name of the active proxy, if any.
func (a *App) activeProxyName() string {
	if id := a.store.Active().ProxyID; id != "" {
		if p, err := a.store.Proxy(id); err == nil {
			return p.Name
		}
	}
	return ""
}

// normalizeTestURL trims input and assumes https:// when no scheme is present.
func normalizeTestURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return raw
}

// flattenHeaders joins multi-value headers into a single comma-separated string.
func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// cleanProxyError strips the noisy `Get "url":` prefix Go puts on client errors.
func cleanProxyError(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, `": `); i >= 0 {
		msg = msg[i+3:]
	}
	return strings.TrimSpace(msg)
}
