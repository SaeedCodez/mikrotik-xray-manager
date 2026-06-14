// Package health measures proxy latency — a real delay through the proxy when
// an xray binary is available, falling back to a TCP ping otherwise.
package health

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"xray-manager/internal/models"
)

const defaultTestURL = "http://www.gstatic.com/generate_204"

// Result is the outcome of testing a single proxy.
type Result struct {
	ProxyID string `json:"proxyId"`
	Latency int64  `json:"latency"` // ms, -1 on error
	Error   string `json:"error,omitempty"`
}

// ConfigFunc builds a minimal xray config that routes an HTTP inbound on
// 127.0.0.1:httpPort through the given proxy.
type ConfigFunc func(p *models.Proxy, httpPort int) ([]byte, error)

// Prober measures latency. When Binary + Config are set and the binary exists,
// it runs a real delay test through a temporary xray instance; otherwise it
// falls back to a TCP ping to the proxy endpoint.
type Prober struct {
	Binary  string     // xray binary path
	Config  ConfigFunc // builds a probe config for a chosen http port
	TestURL string     // URL fetched through the proxy (default: gstatic generate_204)
}

// TCPPing dials addr:port and returns the connect latency in milliseconds.
func TCPPing(address string, port int) (int64, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(address, fmt.Sprint(port)), 5*time.Second)
	if err != nil {
		return -1, err
	}
	_ = conn.Close()
	return time.Since(start).Milliseconds(), nil
}

// realDelayAvailable reports whether a real proxied test can be performed.
func (pr *Prober) realDelayAvailable() bool {
	if pr == nil || pr.Binary == "" || pr.Config == nil {
		return false
	}
	if _, err := os.Stat(pr.Binary); err == nil {
		return true
	}
	_, err := exec.LookPath(pr.Binary)
	return err == nil
}

// Probe measures latency for a single proxy.
func (pr *Prober) Probe(p *models.Proxy) Result {
	if pr.realDelayAvailable() {
		lat, err := realDelay(pr.Binary, p, pr.Config, pr.url())
		if err != nil {
			return Result{ProxyID: p.ID, Latency: -1, Error: err.Error()}
		}
		return Result{ProxyID: p.ID, Latency: lat}
	}
	// Fallback: TCP ping (binary unavailable).
	lat, err := TCPPing(p.Address, p.Port)
	r := Result{ProxyID: p.ID, Latency: lat}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

func (pr *Prober) url() string {
	if pr.TestURL != "" {
		return pr.TestURL
	}
	return defaultTestURL
}

// TestAll concurrently probes every proxy (max `concurrency` at once) and
// streams each Result to out as it completes. out is closed when all finish.
func (pr *Prober) TestAll(proxies []models.Proxy, concurrency int, out chan<- Result) {
	if concurrency < 1 {
		concurrency = 8
	}
	sem := make(chan struct{}, concurrency)
	done := make(chan struct{})
	for i := range proxies {
		p := proxies[i]
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; done <- struct{}{} }()
			out <- pr.Probe(&p)
		}()
	}
	for range proxies {
		<-done
	}
	close(out)
}

// realDelay starts a temporary xray instance with p as the outbound, fetches
// testURL through it, and returns the round-trip latency in milliseconds.
func realDelay(binary string, p *models.Proxy, cfgFn ConfigFunc, testURL string) (int64, error) {
	port, err := freePort()
	if err != nil {
		return -1, fmt.Errorf("no free port: %w", err)
	}
	cfg, err := cfgFn(p, port)
	if err != nil {
		return -1, fmt.Errorf("config: %w", err)
	}

	tmp, err := os.CreateTemp("", "xray-probe-*.json")
	if err != nil {
		return -1, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(cfg); err != nil {
		tmp.Close()
		return -1, err
	}
	tmp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, "run", "-c", tmpName)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("start xray: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	if err := waitPort(ctx, port, 4*time.Second); err != nil {
		if msg := firstLine(stderr.String()); msg != "" {
			return -1, fmt.Errorf("proxy did not start: %s", msg)
		}
		return -1, fmt.Errorf("proxy did not start in time")
	}

	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			DisableKeepAlives: true,
		},
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return -1, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
	latency := time.Since(start).Milliseconds()

	if resp.StatusCode >= 400 {
		return -1, fmt.Errorf("test URL returned HTTP %d", resp.StatusCode)
	}
	return latency, nil
}

// freePort asks the OS for an available TCP port on localhost.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitPort blocks until 127.0.0.1:port accepts a connection or the timeout hits.
func waitPort(ctx context.Context, port int, timeout time.Duration) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(80 * time.Millisecond)
	}
	return fmt.Errorf("timeout")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
