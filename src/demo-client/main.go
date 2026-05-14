// demo-client (Week 6) — plain HTTP caller.
//
// In Week 5 this file opened the SPIRE Workload API, built an mTLS
// *tls.Config, and made HTTPS calls with SPIFFE-ID-pinned peer
// verification. ~120 lines of identity plumbing.
//
// In Week 6 the istio-proxy sidecar in this pod intercepts our outbound
// traffic, originates mTLS to the demo-server sidecar using SPIRE-issued
// SVIDs, and the response is decrypted before the body reaches us. From
// this binary's point of view, we're calling a plain HTTP service in
// 2010. All ~30 lines.
//
// The forever-loop pattern is preserved so the Phase D revocation demo
// remains observable.
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	// http (not https) on port 80 — the sidecar upgrades to mTLS for us.
	target := envOr("TARGET_URL", "http://demo-server.demo-server.svc.cluster.local/")
	hostname, _ := os.Hostname()
	log.Printf("demo-client starting (pod=%s) → %s (plain HTTP; mesh handles mTLS)", hostname, target)

	c := &http.Client{Timeout: 10 * time.Second}
	for {
		if err := call(c, target); err != nil {
			log.Printf("FAIL: %v", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func call(c *http.Client, url string) error {
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d body=%q", resp.StatusCode, body)
	}
	// Trim trailing newline so logs stay one-line.
	s := string(body)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	log.Printf("OK ← %s", s)
	return nil
}
