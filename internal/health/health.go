// Package health measures proxy reachability.
package health

import (
	"fmt"
	"net"
	"time"

	"xray-manager/internal/models"
)

// Result is the outcome of testing a single proxy.
type Result struct {
	ProxyID string `json:"proxyId"`
	Latency int64  `json:"latency"` // ms, -1 on error
	Error   string `json:"error,omitempty"`
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

// Test runs the best available check for a proxy. Currently a TCP ping, which
// is fast and works for every protocol; RealDelay (proxied HTTP) can be layered
// on later without changing callers.
func Test(p *models.Proxy) Result {
	lat, err := TCPPing(p.Address, p.Port)
	r := Result{ProxyID: p.ID, Latency: lat}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// TestAll concurrently tests every proxy (max `concurrency` at once) and streams
// each Result to out as it completes. out is closed when all tests finish.
func TestAll(proxies []models.Proxy, concurrency int, out chan<- Result) {
	if concurrency < 1 {
		concurrency = 10
	}
	sem := make(chan struct{}, concurrency)
	done := make(chan struct{})
	for i := range proxies {
		p := proxies[i]
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; done <- struct{}{} }()
			out <- Test(&p)
		}()
	}
	for range proxies {
		<-done
	}
	close(out)
}
