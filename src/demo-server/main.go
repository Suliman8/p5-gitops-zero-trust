// demo-server is an HTTPS service that only accepts requests from
// clients presenting a valid SPIFFE SVID matching `demo-client`.
//
// Zero secrets in this binary. No certs on disk. No password file. The
// server's own identity AND the set of who-may-call-me both come from
// the SPIRE agent running on the same node, reached over a Unix socket.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

func main() {
	ctx := context.Background()

	// Where to find the SPIRE Agent's Workload API. Same Unix socket
	// that we mounted into svid-test in Week 4. The "unix://" prefix
	// tells go-spiffe to dial a Unix socket rather than TCP.
	socketPath := "unix:///run/spire/agent-sockets/spire-agent.sock"

	// X509Source is the long-lived object that keeps a stream open to
	// the agent and gives us:
	//   - our own server cert+key  (rotated automatically before expiry)
	//   - the trust bundle to verify other peers
	// One Source serves BOTH roles below (server identity + peer auth).
	source, err := workloadapi.NewX509Source(ctx,
		workloadapi.WithClientOptions(workloadapi.WithAddr(socketPath)))
	if err != nil {
		log.Fatalf("failed to open Workload API: %v", err)
	}
	defer source.Close()

	// The SPIFFE ID we will accept as a caller. Anything else gets
	// rejected during the TLS handshake — never even reaches our handler.
	// This is the *zero-trust whitelist*: cluster IP, namespace, network
	// position — none of that matters. Only this SPIFFE ID gets in.
	allowedClient := spiffeid.RequireFromString("spiffe://p5.local/demo-app")

	// Standard Go http.Server with a TLS config that:
	//   - presents this workload's SVID as the server cert
	//   - requires the client to present an SVID, verified against the
	//     trust bundle, AND its SPIFFE ID must equal allowedClient
	server := &http.Server{
		Addr: ":8443",
		TLSConfig: tlsconfig.MTLSServerConfig(
			source,                              // for our server cert
			source,                              // for the trust bundle to verify clients
			tlsconfig.AuthorizeID(allowedClient), // who is allowed to call us
		),
		Handler: http.HandlerFunc(handle),
	}

	// "" + "" because the TLSConfig already supplies the cert via the
	// X509Source — Go's normal "load cert from file" path is bypassed.
	hostname, _ := os.Hostname()
	log.Printf("demo-server starting on :8443 (pod=%s) — accepting %s", hostname, allowedClient)
	log.Fatal(server.ListenAndServeTLS("", ""))
}

func handle(w http.ResponseWriter, r *http.Request) {
	// At this point the mTLS handshake already passed, which means the
	// caller is authenticated as `spiffe://p5.local/demo-client`. We can
	// dig out the exact SPIFFE ID for the response (and for logging).
	var clientID string
	if len(r.TLS.PeerCertificates) > 0 {
		if uri := r.TLS.PeerCertificates[0].URIs; len(uri) > 0 {
			clientID = uri[0].String()
		}
	}
	log.Printf("authenticated request from %s", clientID)
	fmt.Fprintf(w, "demo-server says hi — your verified identity is %s\n", clientID)
}
