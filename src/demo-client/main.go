// demo-client repeatedly calls demo-server over SPIFFE mTLS.
//
// Zero secrets in this binary. No certs on disk. No bearer token in env.
// Both the client identity AND the server-trust policy come from the
// SPIRE agent on the same node, over a Unix socket.
//
// The point of running in a loop (rather than one-shot + exit) is to make
// the Week-5 revocation demo legible: you can `kubectl logs -f` this pod
// and literally watch the line transition from "OK" to "handshake failed"
// the moment the ClusterSPIFFEID gets deleted from Git.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// Defaults are sensible for our cluster layout but every knob is overridable
// via env vars — the K8s Deployment will set these explicitly so the binary
// itself stays environment-agnostic.
const (
	defaultSocketPath = "unix:///run/spire/agent-sockets/spire-agent.sock"
	defaultTargetURL  = "https://demo-server.demo-server.svc.cluster.local:8443/"
	defaultServerID   = "spiffe://p5.local/demo-server"
	defaultInterval   = 5 * time.Second
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	socketPath := envOr("SPIFFE_ENDPOINT_SOCKET", defaultSocketPath)
	targetURL := envOr("TARGET_URL", defaultTargetURL)
	serverIDStr := envOr("EXPECTED_SERVER_ID", defaultServerID)

	// Parse the expected server SPIFFE ID at startup. If somebody sets a
	// malformed env var, we want to fail loudly on boot — not silently
	// on every request.
	expectedServer := spiffeid.RequireFromString(serverIDStr)

	ctx := context.Background()

	// X509Source is the long-lived stream to the SPIRE agent. It does three
	// things for us simultaneously:
	//   1. Hands us our OWN SVID (used as the TLS client cert below)
	//   2. Hands us the TRUST BUNDLE (used to verify the server's cert)
	//   3. Keeps both refreshed in the background — SVIDs rotate every
	//      hour by default, the source picks up new ones automatically.
	//
	// Without this stream, we would have to call FetchX509SVID before every
	// request, which would (a) hammer the agent and (b) miss rotations.
	source, err := workloadapi.NewX509Source(ctx,
		workloadapi.WithClientOptions(workloadapi.WithAddr(socketPath)))
	if err != nil {
		log.Fatalf("failed to open Workload API at %s: %v", socketPath, err)
	}
	defer source.Close()

	// tlsconfig.MTLSClientConfig produces a *tls.Config wired up like this:
	//   - Certificates callback returns OUR current SVID (source #1)
	//   - RootCAs is replaced by a VerifyPeerCertificate that walks the
	//     SPIFFE trust bundle (source #2)
	//   - The AuthorizeID matcher rejects ANY server SVID whose SPIFFE ID
	//     is not `expectedServer`. Wrong ID → handshake aborts, our HTTP
	//     client gets a TLS error, we never send our request body.
	//
	// This is the zero-trust enforcement point on OUR side: we refuse to
	// even speak to a server we don't recognize, regardless of DNS games
	// or IP spoofing.
	tlsCfg := tlsconfig.MTLSClientConfig(
		source, // for our own client cert
		source, // for the trust bundle (verify server's cert chain)
		tlsconfig.AuthorizeID(expectedServer),
	)

	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   10 * time.Second,
	}

	hostname, _ := os.Hostname()
	log.Printf("demo-client starting (pod=%s) → target=%s expecting=%s",
		hostname, targetURL, expectedServer)

	// Forever-loop. Errors are logged but never fatal — we want to keep
	// running so the revocation demo can OBSERVE failures, not just see
	// the pod CrashLoopBackOff.
	ticker := time.NewTicker(defaultInterval)
	defer ticker.Stop()
	// Fire one call immediately so logs aren't empty for the first 5s.
	for ; ; <-ticker.C {
		if err := callOnce(httpClient, targetURL); err != nil {
			log.Printf("FAIL: %v", err)
		}
	}
}

func callOnce(c *http.Client, url string) error {
	// Plain old http.Get — the magic is entirely in the *http.Client's
	// Transport.TLSClientConfig. If you didn't read this comment, you
	// would never guess that this single line is doing:
	//   * mTLS with auto-rotating certs
	//   * SPIFFE-ID-pinned server authentication
	//   * Trust bundle refresh from a separate process
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d body=%q", resp.StatusCode, body)
	}

	// On success, also print the server's verified SPIFFE ID for clarity.
	// This proves the cert chain was checked end-to-end.
	var serverID string
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		if u := resp.TLS.PeerCertificates[0].URIs; len(u) > 0 {
			serverID = u[0].String()
		}
	}
	log.Printf("OK ← from=%s body=%s", serverID, sanitize(string(body)))
	return nil
}

// sanitize trims trailing newlines so logs stay one-line per event.
func sanitize(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
