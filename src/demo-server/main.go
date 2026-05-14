// demo-server (Week 6) — plain HTTP.
//
// In Week 5 this file did SPIFFE mTLS by hand: it opened the SPIRE
// Workload API, fetched its SVID, ran a TLS server, and enforced a
// SPIFFE-ID whitelist on every caller.
//
// In Week 6 the Istio sidecar (istio-proxy) handles all of that
// transparently. We are back to a 25-line "hello world" HTTP server.
// Identity enforcement still happens — but in a YAML AuthorizationPolicy
// applied by Istio, not in this code. The security moves out of the app.
//
// The caller's SPIFFE identity is passed in as an HTTP header by the
// sidecar (X-Forwarded-Client-Cert) so the app can log who called it
// without dealing with TLS at all.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	hostname, _ := os.Hostname()
	log.Printf("demo-server starting on :8080 (pod=%s) — plain HTTP, mesh handles mTLS", hostname)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Istio injects X-Forwarded-Client-Cert when mTLS is in use.
		// The header carries the peer SPIFFE ID — useful for logging
		// even though authZ enforcement happens before this code runs.
		caller := r.Header.Get("X-Forwarded-Client-Cert")
		if caller == "" {
			caller = "<no XFCC header — request came in plain HTTP>"
		}
		log.Printf("call from %s", caller)
		fmt.Fprintf(w, "demo-server (W6 plain HTTP) — caller identity per mesh: %s\n", caller)
	})

	log.Fatal(http.ListenAndServe(":8080", nil))
}
