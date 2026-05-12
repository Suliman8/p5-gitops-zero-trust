# Week 5 — SPIFFE mTLS end-to-end + identity revocation via Git

**Status:** Complete
**Date:** 2026-05-12
**Outcome:** Two Go workloads (demo-app → demo-server) talk over mTLS where
each side cryptographically verifies the other's SPIFFE ID. Access can be
revoked by deleting a single line from a Git file — proven live by watching
the calling pods go from `OK` to "silently unable to start" within 30s.

---

## Goal

Stop using "we're in the same cluster" as an authorisation signal.

Three milestones:

1. demo-app and demo-server present **SVIDs** to each other on every call;
   each side rejects the connection if the peer's SPIFFE ID isn't on its
   whitelist.
2. Both binaries are containerised with **distroless** runtime images — no
   shell, no package manager, runs as UID 65532.
3. Deleting a workload's `ClusterSPIFFEID` from Git removes its access. No
   network rule changes. No firewall edits. Just a commit.

---

## Concepts introduced

| Term | Plain meaning |
| --- | --- |
| **SVID** | "SPIFFE Verifiable Identity Document" — a short-lived X.509 cert that carries a SPIFFE ID (`spiffe://trust-domain/path`) in the SAN. Issued by the SPIRE agent over its Workload API. |
| **Workload API** | Unix socket exposed by the SPIRE agent on each node. Workloads dial it to get their SVID and the trust bundle. No auth tokens needed — the agent identifies callers by pod UID via its attestor. |
| **X509Source** | The go-spiffe object that opens a long-lived stream to the Workload API and keeps the SVID + trust bundle fresh. Plug it into a `*tls.Config` and TLS handshakes "just work" with auto-rotating certs. |
| **mTLS (mutual TLS)** | Standard TLS, but both sides present certs. With SPIFFE: both certs are SVIDs, and each side enforces a SPIFFE-ID whitelist on the peer cert before the handshake completes. |
| **`tlsconfig.AuthorizeID`** | The whitelist function from go-spiffe. Adds a callback that rejects any peer cert whose SPIFFE ID doesn't match. Enforced at the TLS layer — failed handshakes never reach application code. |
| **Distroless image** | A base image with no shell, no busybox, no package manager. `gcr.io/distroless/static-debian12:nonroot` is ~2 MB. Even if the Go binary has an RCE, the attacker has no `/bin/sh` to spawn. |
| **`hostPath` socket mount** | The pod and the agent are different pods on the same node; they share a Unix socket via hostPath. Mounted `readOnly: true` — the workload can talk to the agent but can't tamper with the socket directory. |

---

## Architecture (end of Week 5)

```
                         Git: this repository
                         ├── apps/demo-app/deployment.yaml       (client)
                         ├── apps/demo-server/                   (server)
                         └── infrastructure/spire-entries/
                             ├── demo-app.yaml      → spiffe://p5.local/demo-app
                             └── demo-server.yaml   → spiffe://p5.local/demo-server
                                       │
                                       │ ArgoCD reconciles
                                       ▼
                         ┌─────────────── kind cluster (p5-dev) ──────────────┐
                         │                                                    │
                         │  namespace: demo                                   │
                         │  ┌───────────────────────────────┐                 │
                         │  │ demo-app pod ×3 (Go client)   │                 │
                         │  │   1. fetch SVID from agent    │                 │
                         │  │   2. dial demo-server (mTLS)  │                 │
                         │  │   3. verify server SPIFFE ID  │                 │
                         │  │   4. log OK / FAIL, loop      │                 │
                         │  └────────────────┬──────────────┘                 │
                         │                   │ HTTPS :8443                    │
                         │                   │ mTLS, peer-pinned by SPIFFE ID │
                         │                   ▼                                │
                         │  namespace: demo-server                            │
                         │  ┌───────────────────────────────┐                 │
                         │  │ demo-server pod (Go HTTPS)    │                 │
                         │  │   - presents demo-server SVID │                 │
                         │  │   - whitelist: demo-app only  │                 │
                         │  │   - prints caller SPIFFE ID   │                 │
                         │  └───────────────────────────────┘                 │
                         │                                                    │
                         │  spire-agent DaemonSet (per node)                  │
                         │   └─ Unix socket at /run/spire/agent-sockets/      │
                         │      mounted read-only into both pods above        │
                         └────────────────────────────────────────────────────┘
```

---

## Files added / changed

```
src/demo-server/                              # built into demo-server:v0.1.0
├── main.go            (Go HTTPS server, mTLS via go-spiffe)
├── go.mod / go.sum
└── Dockerfile         (multi-stage → distroless/static-debian12:nonroot)

src/demo-client/                              # built into demo-client:v0.1.0
├── main.go            (forever-loop caller, env-configurable)
├── go.mod / go.sum
└── Dockerfile

apps/demo-server/                             # new ArgoCD-managed app
├── namespace.yaml
├── deployment.yaml    (1 replica, agent socket mount, locked-down)
└── service.yaml       (ClusterIP :8443)

apps/demo-app/                                # converted from nginx
├── deployment.yaml    (REWRITTEN: nginx → demo-client + socket mount)
└── service.yaml       (DELETED: client has no port to expose)

infrastructure/spire-entries/
├── demo-server.yaml   (NEW — ClusterSPIFFEID for the server)
└── kustomization.yaml (+1 line for demo-server.yaml)

clusters/dev/bootstrap/
└── demo-server.yaml   (NEW — ArgoCD Application)
```

### Key configuration choices

| Decision | Choice | Why |
| --- | --- | --- |
| Container base | `gcr.io/distroless/static-debian12:nonroot` | 2 MB, no shell. Even if the Go binary is exploited, the attacker has nothing to pivot with. The `:nonroot` tag enforces UID 65532 before the K8s manifest's `securityContext` is even evaluated. |
| Build | `CGO_ENABLED=0` + `-ldflags="-s -w"` + `-trimpath` | Static binary (no libc dep so distroless/static works), stripped symbols (~30% smaller, harder to reverse-engineer), no leaked source paths in panic traces. |
| Agent socket mount | `hostPath` with `readOnly: true` | The only K8s primitive that lets two pods on the same node share a Unix socket. Read-only because workloads should never write to the agent's directory. |
| Pod security | `runAsNonRoot`, `readOnlyRootFilesystem`, `drop: ALL`, `allowPrivilegeEscalation: false`, `seccompProfile: RuntimeDefault` | Layered defence. Each line is a separate kernel-enforced restriction. Set them all so a misconfig in one doesn't open everything. |
| Caller→server addressing | `https://demo-server.demo-server.svc.cluster.local:8443/` (env var) | Standard cluster DNS. Service is ClusterIP (internal-only) — the whole point is that callers must be SPIFFE-authenticated. |
| Whitelist enforcement | `tlsconfig.AuthorizeID(spiffeid)` on BOTH ends | At the TLS layer, not in HTTP handlers. A wrong peer's request never reaches our code — the handshake aborts. Forgetting an auth check in code is the #1 source of authZ bugs; this design makes that impossible. |
| Client retry strategy | Forever loop, errors logged not fatal | A `log.Fatal` on error would crash-loop the pod. We want to observe transitions: the revocation demo needs the pod to keep running while access is being cut. |
| Two namespaces (`demo`, `demo-server`) | Separate apps, separate ArgoCD Applications | Blast-radius isolation. Pausing one Application doesn't break the other. The CRDs use `namespaceSelector` so identity is auto-scoped to the right ns. |

---

## How a request flows end-to-end

```
1. demo-app pod starts.
2. go-spiffe opens workloadapi.NewX509Source against the agent socket.
3. The SPIRE agent identifies the caller by pod-UID, finds a matching
   registration entry (created by the controller manager from the
   ClusterSPIFFEID CRD), mints an X.509 SVID with
   SPIFFE ID = spiffe://p5.local/demo-app, streams it back.
4. demo-app builds a *tls.Config via tlsconfig.MTLSClientConfig:
   - presents its OWN SVID when asked for a client cert
   - verifies the SERVER's cert against the SPIRE trust bundle
   - AuthorizeID("spiffe://p5.local/demo-server") rejects any other ID
5. demo-app does http.Get(...). The TLS handshake:
   - client sends ClientHello
   - server sends its demo-server SVID
   - client validates → matches whitelist → OK
   - server's CertificateRequest demands a client cert
   - client sends its demo-app SVID
   - server validates → matches its own whitelist → OK
   - keys are derived, encrypted tunnel established
6. demo-app sends GET / over the tunnel.
7. demo-server's handler reads r.TLS.PeerCertificates, extracts the
   caller's SPIFFE ID, echoes it back in the body for clarity.
8. demo-app logs:
     OK ← from=spiffe://p5.local/demo-server
        body="demo-server says hi — your verified identity is
              spiffe://p5.local/demo-app"
```

Zero shared secrets. Zero certificates on disk. Zero bearer tokens. The
entire authentication state is held in-memory by the agent and rotated
automatically.

---

## The revocation demo (and what we learned)

### The naive attempt that didn't work

`kubectl delete clusterspiffeid demo-app` removed the CRD from the cluster
— but the demo-app logs kept showing `OK` indefinitely. Two reasons:

1. **ArgoCD's `selfHeal: true` restored the CRD within ~30s.** The cluster
   "fought back" because the file was still in Git. **Lesson:** in true
   GitOps, the cluster is downstream of Git — you cannot revoke by editing
   the cluster, only by editing Git.

2. Even after the CRD was gone, the demo-app pods still had their existing
   SVIDs cached. SVIDs default to ~1-hour TTLs; revocation in SPIFFE is
   "stop issuing new ones," not "invalidate the ones already out there."
   **Lesson:** SPIFFE revocation is bounded by SVID TTL. For tighter
   revocation windows, shorten the TTL (knob: `default_x509_svid_ttl` on
   the SPIRE Server).

### The correct revocation flow

1. **Edit Git:** comment out `demo-app.yaml` in
   `infrastructure/spire-entries/kustomization.yaml`.
2. **Commit + push.** ArgoCD picks up the change on its next sync (or
   force with `kubectl -n argocd patch app spire-entries ... operation:
   sync`).
3. **ArgoCD removes the CRD from the cluster.** Controller manager sees
   the deletion, removes the SPIRE registration entries for demo-app
   pod UIDs.
4. **Existing demo-app pods keep working** (cached SVID, until TTL).
5. **New demo-app pods cannot start** — `workloadapi.NewX509Source`
   blocks forever because the agent has nothing to issue. Pods show as
   `Running` with **zero log output**.
6. **Recovery:** `git revert HEAD && git push`. Within ~30s, entries
   reappear, blocked pods unblock, logs resume.

### Observed timings (this run)

| Event | T+ |
| --- | --- |
| `git push` of revoke commit | 0s |
| ArgoCD sync triggered | +5s |
| `ClusterSPIFFEID demo-app` deleted from cluster | +10s |
| SPIRE entries: 5 → 2 | +10s |
| (existing pods still OK due to SVID cache) | — |
| `kubectl delete pod -l app=demo-app` to force fresh fetch | +90s |
| New pods running but emitting zero logs (no SVID) | +120s |
| `git revert` + `git push` | +180s |
| Entries restored, pods log `OK ← ...` | +210s |

The whole revoke + restore cycle was 3.5 minutes, and the only commands
that touched the cluster directly were observation commands. Every
**state change** was a Git commit.

---

## Lessons learned

### `workloadapi.NewX509Source` blocks forever when no entry exists

The constructor doesn't time out by default. If the agent never gets a
matching entry, the call blocks indefinitely and the workload looks
"Running" with empty logs. For a production demo or a `livenessProbe`,
wrap the call with `context.WithTimeout` and log "no SVID after Ns" —
that makes the failure mode legible.

### `selfHeal: true` is a feature, not a bug, in this project

It makes the cluster impossible to drift from Git. The trade-off is that
debugging tricks like `kubectl delete` won't stick. For zero-trust work
that's the correct behaviour: the only authoritative source of "who can
talk to whom" is Git.

### SPIFFE revocation is not instant

Two separate clocks: (a) when does the agent stop issuing new SVIDs (next
sync, ~30s), and (b) when do existing SVIDs expire (TTL, default 1h).
Restarting a pod skips clock (b) entirely. For tighter "kill switch"
semantics, lower the default SVID TTL — but that costs agent CPU.

### Two ArgoCD Applications > one bundled Application

When we revoked demo-app's identity, the demo-server Application stayed
`Healthy` and demo-server kept running normally. If we had bundled both
under a single Application, the revocation would have shown up as an
"OutOfSync" warning on the joint app — which would have confused the
operator. One service = one Application is the simpler default.

### Distroless costs ~5 minutes of debugging, saves hours of CVE patching

The downside: no `kubectl exec demo-server -- /bin/sh` for ad-hoc poking.
The upside: zero CVE noise from busybox, no apk/apt to patch, no shell
escape vectors. For workloads that hold private keys, this is a clear
win — debugging happens through logs and metrics, not by attaching to
the container.

---

## Behavioural change worth noting

| | Before (Week 4) | After (Week 5) |
| --- | --- | --- |
| What demo-app does | Nothing (vanilla nginx, never serves a request) | Calls demo-server every 5s over mTLS |
| Authentication between services | None — nginx is open | mTLS, SPIFFE-ID-pinned both ways |
| Where credentials live | N/A | Memory only (X509Source); never on disk |
| Revoking a service's access | N/A | Delete one line from `kustomization.yaml`, commit |
| Container runtime | nginx:1.27-alpine (5 MB, has shell) | distroless (2 MB, no shell, no busybox) |
| Pod security context | Default (root, RW rootfs) | Non-root, ro rootfs, dropped caps, seccomp |

---

## What's next (Week 6)

We now have **two** services talking over manual SPIFFE mTLS — and the
SPIFFE primitives are working end-to-end. The next jump is to stop hand-
coding the TLS plumbing in every service and let a **service mesh** do it
transparently:

1. Install **Istio** (or Linkerd) wired up to consume SPIRE SVIDs via SPIFFE
   federation. The sidecar handles mTLS so application code doesn't import
   go-spiffe.
2. Define **AuthorizationPolicy** resources for identity-based authZ —
   e.g. "only `spiffe://p5.local/demo-app` may call demo-server." Same
   guarantee as today, but enforced by the mesh, not by application code.
3. Optionally: enable JWT-SVID issuance so non-HTTP workloads (e.g. a Vault
   sidecar in W7) can authenticate without TLS.

The original Project 5 plan called this "Week 4 — Istio/Linkerd install +
SPIFFE integration." We deviated to land the GitOps-native identity layer
(Week 4) and the hand-coded mTLS proof (Week 5) first. Mesh next.

---

## Verification

```bash
# Workloads up
$ kubectl get pods -n demo && kubectl get pods -n demo-server
demo-app-xxx   1/1 Running ×3
demo-server-xxx 1/1 Running

# Identity CRDs in Git, materialized as SPIRE entries
$ kubectl get clusterspiffeids
NAME          AGE
demo-app      ...
demo-server   ...

$ kubectl -n spire-server exec spire-server-0 -c spire-server -- \
    /opt/spire/bin/spire-server entry show | grep "^Found"
Found 5 entries  # 1 server + 3 client pods + 1 node alias

# mTLS working end-to-end
$ kubectl -n demo logs deployment/demo-app --tail=3
OK ← from=spiffe://p5.local/demo-server body=demo-server says hi —
     your verified identity is spiffe://p5.local/demo-app
OK ← from=spiffe://p5.local/demo-server ...
OK ← from=spiffe://p5.local/demo-server ...
```

---

## References

- go-spiffe v2: <https://github.com/spiffe/go-spiffe/tree/v2.5.0>
- `tlsconfig.MTLSClientConfig` / `MTLSServerConfig`:
  <https://pkg.go.dev/github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig>
- SPIFFE Workload API spec:
  <https://github.com/spiffe/spiffe/blob/main/standards/SPIFFE_Workload_API.md>
- Distroless images: <https://github.com/GoogleContainerTools/distroless>
- This week's commit range: `d2682a6..8d6269a`
