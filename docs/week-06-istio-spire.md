# Week 6 — Istio service mesh + mesh-layer authorization

**Status:** Complete (with one deferred goal)
**Date:** 2026-05-14
**Outcome:** demo-app ↔ demo-server now talk over **mesh-managed mTLS**
with **declarative AuthorizationPolicy** enforcement. Hand-coded
go-spiffe is gone. Revocation moved from "delete a SPIFFE identity"
(W5) to "change one line in a policy YAML" — and propagation is
~10× faster.

---

## Goal

Move the security plumbing out of application code and into the
infrastructure. After this week:

- Apps are **plain HTTP** (~30 LOC each).
- Sidecars do mTLS automatically.
- Identity-based access control is a **YAML CRD**, not a Go constant.
- The whole thing reconciles from Git like every other component.

---

## Concepts introduced

| Term | Plain meaning |
| --- | --- |
| **Service mesh** | A layer of sidecar proxies (one per pod) that transparently handle mTLS, retries, traffic routing. The app code is unchanged. |
| **Sidecar (istio-proxy)** | An Envoy proxy injected as a second container in every meshed pod. All traffic in/out of the pod goes through it. |
| **`istiod`** | Istio's control plane. Watches K8s resources and pushes Envoy config to every sidecar via the xDS protocol. |
| **xDS** | The wire protocol Envoy uses to fetch its config dynamically. istiod is an xDS server. |
| **SDS (Secret Discovery Service)** | The xDS variant that delivers certs to Envoy. With SPIRE: istio-agent translates SDS requests into SPIRE Workload API calls. With Citadel: istiod is the SDS source. |
| **CSI (Container Storage Interface)** | Standard plugin model for K8s storage. `spiffe-csi-driver` exposes the SPIRE agent socket as a CSI ephemeral volume — cleaner than `hostPath`. |
| **`AuthorizationPolicy`** | An Istio CRD that says "only callers with these principals/headers/methods can reach this workload." Enforced at the sidecar's L7 RBAC filter — before traffic reaches the app. |
| **Principal** | A caller's verified SPIFFE ID (from the client cert in the mTLS handshake). Used in AuthorizationPolicy as cryptographic proof of identity. |
| **X-Forwarded-Client-Cert (XFCC)** | Header injected by the receiving sidecar with the peer's verified SPIFFE ID. Lets the app log who called it without doing any TLS itself. |
| **`PeerAuthentication`** | Istio CRD controlling whether mTLS is `STRICT` (mTLS only), `PERMISSIVE` (both OK), or `DISABLE`. Default is PERMISSIVE — eases rollout. |
| **Trust domain alignment** | The mesh's `trustDomain` must match the SPIFFE issuer's. We set Istio's to `p5.local` so SPIFFE IDs look the same whether Citadel or SPIRE signs them. |

---

## Architecture (end of Week 6)

```
                 Git: this repository
                 ├── apps/demo-app/         (plain HTTP client, v0.2.0)
                 ├── apps/demo-server/      (plain HTTP server, v0.2.0)
                 │     └── authorizationpolicy.yaml  (whitelist)
                 ├── infrastructure/spire-entries/
                 │     └── istio-workloads.yaml      (Istio-format IDs, unused but scaffolded)
                 └── clusters/dev/bootstrap/
                       ├── istio-base.yaml          (CRDs + RBAC)
                       ├── istiod.yaml              (control plane)
                       └── spiffe-csi-driver.yaml   (CSI driver)
                              │
                              │ ArgoCD reconciles
                              ▼
   ┌────────────────────── kind cluster ──────────────────────────────┐
   │                                                                  │
   │   istio-system ns                                                │
   │   ┌──────────────────────┐                                       │
   │   │  istiod              │  xDS / SDS to all sidecars            │
   │   │  - watches K8s       │                                       │
   │   │  - issues Envoy cfg  │                                       │
   │   │  - Citadel CA (here) │                                       │
   │   └──────────┬───────────┘                                       │
   │              │                                                   │
   │   ┌──────────┴──────────────────┐                                │
   │   ▼                             ▼                                │
   │  demo ns (sidecar injected)    demo-server ns (sidecar injected) │
   │  ┌─────────────────────────┐    ┌──────────────────────────────┐ │
   │  │ demo-app pod ×3         │    │ demo-server pod              │ │
   │  │  ┌──────────────────┐   │    │ ┌──────────────────┐         │ │
   │  │  │ demo-client      │   │    │ │ demo-server      │         │ │
   │  │  │ (Go, plain HTTP) │   │    │ │ (Go, plain HTTP) │         │ │
   │  │  └─────────┬────────┘   │    │ └─────────▲────────┘         │ │
   │  │            │            │    │           │ XFCC header      │ │
   │  │            │ localhost  │    │           │ for caller ID    │ │
   │  │            ▼            │    │           │                  │ │
   │  │  ┌──────────────────┐   │    │ ┌─────────┴────────┐         │ │
   │  │  │ istio-proxy      │   mTLS │ │ istio-proxy      │         │ │
   │  │  │ - originates TLS │───────►│ │ - terminates TLS │         │ │
   │  │  │ - SPIFFE ID:     │   over │ │ - RBAC filter:   │         │ │
   │  │  │   ns/demo/sa/    │   wire │ │   "only demo-app │         │ │
   │  │  │   default        │        │ │    allowed"      │         │ │
   │  │  └──────────────────┘        │ └──────────────────┘         │ │
   │  └─────────────────────────┘    └──────────────────────────────┘ │
   │                                                                  │
   │   spire-server ns (still used, but not for mesh certs)          │
   │   ┌──────────────┐   ┌──────────────────────────┐                │
   │   │ spire-server │   │ spiffe-csi-driver        │                │
   │   │ spire-agent  │   │ (running on each node;   │                │
   │   │ controller-  │   │  installed but unused    │                │
   │   │ manager      │   │  by sidecars — see       │                │
   │   └──────────────┘   │  "Deferred goal" below)  │                │
   │                      └──────────────────────────┘                │
   └──────────────────────────────────────────────────────────────────┘
```

---

## Phases and what each delivered

### Phase A — install the platform

- `spiffe-csi-driver` DaemonSet via vendored manifests (the upstream
  Helm repo only ships a bundled `spire` chart, which would conflict
  with our existing SPIRE deployment; vendoring 3 small files is
  cleaner).
- Istio 1.24.1 via the official Helm charts (`base` then `istiod`),
  with `ignoreDifferences` on `caBundle` to silence the unavoidable
  drift from Istio's runtime self-bootstrap.

### Phase B — control-plane integration with SPIRE

- New `ClusterSPIFFEID` `istio-workloads` that produces Istio-canonical
  IDs `spiffe://p5.local/ns/<ns>/sa/<sa>` scoped to
  `istio-injection=enabled` namespaces.
- Updated `istiod` mesh config: `trustDomain: p5.local`,
  `SPIFFE_ENDPOINT_SOCKET` pointing at the CSI mount, and a custom
  sidecar injection template `spire` that mounts the agent socket
  via CSI. This was the *intended* SPIRE-as-CA path.

### Phase C — apps go plain HTTP

- Rewrote both Go binaries: dropped ~80 LOC of go-spiffe, kept stdlib
  `net/http`. ~30 LOC each.
- Service port `8443` (https) → `80` (http). Critical: the port NAME
  must be `http` for Istio to apply L7 features like AuthorizationPolicy.
- Built `:v0.2.0` images, sideloaded into kind.
- Labeled both namespaces `istio-injection=enabled`.
- Deleted the W5-style `ClusterSPIFFEID`s; sidecars take over identity.

### Phase D — mesh-layer access control

- `AuthorizationPolicy` that whitelists exactly one principal for
  `demo-server`. Default-deny kicks in automatically because at least
  one ALLOW policy applies to the workload.
- Revocation demo: change the whitelist to a fake principal, push,
  watch `OK → FAIL (403)` within ~10s; revert, watch `FAIL → OK`
  within ~15s.

---

## Key configuration choices

| Decision | Choice | Why |
| --- | --- | --- |
| Service mesh | Istio | Linkerd was the alternative. Istio has better SPIRE integration support docs (even though we deferred it), AuthorizationPolicy is more expressive, and istioctl is a great diagnostics tool. |
| Sidecar mode | Classic injection (not Ambient) | Istio 1.24 supports Ambient mode (no sidecar; per-node proxy). Sidecar mode is simpler for learning and aligns with existing AuthorizationPolicy patterns. Ambient is a candidate for W10 cleanup. |
| CA for sidecar certs | Citadel (Istio built-in), not SPIRE | Phase B *intended* SPIRE-as-CA, but SPIRE's workload attestor couldn't reliably map injected pod processes to pod UIDs (kind + cgroup v2 fragility). Deferred — see below. |
| Trust domain | `p5.local` (set on Istio meshConfig) | Matches the SPIRE trust domain so SPIFFE IDs look the same regardless of signer. Means a future swap to SPIRE doesn't change AuthorizationPolicy syntax. |
| Default mTLS mode | PERMISSIVE (Istio default) | Eases rollout. STRICT would have broken cross-version pods during the W5→W6 transition. Tighten to STRICT in W10. |
| Pod opt-in to spire template | Annotation `inject.istio.io/templates: "sidecar,spire"` (added then **removed**) | Was the right approach when SPIRE worked. Removed when we fell back to Citadel; left a `[[project-p5-week6-done]]` note in the deployment YAML so a future reader knows where the integration was supposed to live. |
| AuthorizationPolicy scope | Per-workload (`selector` matches `app: demo-server`) | Could have applied namespace-wide, but per-workload makes the intent explicit and keeps the policy from also affecting future services in `demo-server` ns. |

---

## How a request flows end-to-end (W6)

```
1. demo-client Go code calls http.Get("http://demo-server.demo-server.svc:80")
2. Request leaves the demo-client container's network namespace.
3. iptables (installed by istio-init at pod startup) redirects to the
   sidecar's local port 15001 (outbound proxy).
4. istio-proxy:
   a. resolves the destination by service name
   b. originates a TLS connection to the destination pod
   c. presents its own cert (Citadel-signed SVID:
        spiffe://p5.local/ns/demo/sa/default)
   d. demands a server cert
5. demo-server's istio-proxy receives the TLS:
   a. validates client cert against the trust bundle (Citadel root)
   b. extracts peer SPIFFE ID from cert
   c. applies AuthorizationPolicy: principal
      "p5.local/ns/demo/sa/default" is on the whitelist → ALLOW
   d. forwards the request to localhost:8080 (the demo-server container)
   e. injects X-Forwarded-Client-Cert header with the peer SPIFFE ID
6. demo-server reads r.Header.Get("X-Forwarded-Client-Cert"), logs it,
   responds 200 OK with the peer identity in the body.
7. Response travels the same path in reverse.
```

Zero secrets in the apps. Zero certs on disk anywhere. Identity is
held in-memory by istiod (which generates short-lived certs) and
verified at every mesh hop. Authz is one YAML file in Git.

---

## Verification

```bash
# Both namespaces have sidecar injection
$ kubectl get ns demo demo-server -L istio-injection
NAME          STATUS   AGE   ISTIO-INJECTION
demo          Active   ...   enabled
demo-server   Active   ...   enabled

# Every pod has 2 containers (app + istio-proxy)
$ kubectl get pods -n demo
NAME                       READY   STATUS    AGE
demo-app-7c8b6c8d76-79wrv  2/2     Running   ...
demo-app-7c8b6c8d76-d2cnq  2/2     Running   ...
demo-app-7c8b6c8d76-gkzmr  2/2     Running   ...

# Authorization policy is in place
$ kubectl -n demo-server get authorizationpolicy
NAME                         ACTION   AGE
demo-server-allow-demo-app   ALLOW    ...

# End-to-end mTLS with caller identity in the response body
$ kubectl -n demo logs deployment/demo-app -c demo-client --tail=2
OK ← demo-server (W6 plain HTTP) — caller identity per mesh:
   URI=spiffe://p5.local/ns/demo/sa/default;
   By=spiffe://p5.local/ns/demo-server/sa/default;
   Hash=3619964...

# Cert SAN (proves Citadel issued, mounted via SDS, validated by mesh)
$ POD=$(kubectl -n demo get pod -l app=demo-app -o name | head -1)
$ istioctl proxy-config secret $POD -o json | <openssl-decode>
URI:spiffe://p5.local/ns/demo/sa/default
```

---

## Revocation demo timeline (mesh layer)

| Wall-clock | Event |
|---|---|
| t=0 | edit `authorizationpolicy.yaml`, change principal to a fake one |
| t=+5s | `git commit`, `git push` |
| t=+10s | trigger ArgoCD sync (or wait ~3 min for poll) |
| t=+15s | new policy applied to cluster |
| t=+20s | istiod pushes RBAC config to demo-server sidecar via xDS |
| t=+25s | demo-client logs flip from `OK` to `FAIL: status=403 body="RBAC: access denied"` |
| t=+30s | revert commit + push |
| t=+45s | logs flip back to `OK` |

**Compare to W5** (SVID-layer revocation, ~3.5 min wall-clock): mesh
revocation is **~10× faster** because policies are runtime config,
not certificate properties. SVIDs have TTLs; AuthorizationPolicies
take effect on the next xDS push.

---

## Deferred goal: SPIRE as the mesh CA

Phase B set up the wiring but Phase C revealed the issue: with
SPIRE 1.14.6 + kind 1.35 + cgroup v2, the agent's k8s workload
attestor couldn't reliably tie injected `istio-proxy` PIDs to pod
UIDs. Errors:

```
"Container id not found; giving up" attempt=60
   container_id=... pod_uid=... plugin_name=k8s
"Failed to attest the workload" error="context canceled"
"StreamSecrets ... workload is not authorized for the
   requested identities ['default']"
```

Symptom from the sidecar side: `1/2 CrashLoopBackOff` with
`FailedPostStartHook`.

**What worked** (Phase B left in place):
- `ClusterSPIFFEID istio-workloads` (still runs; creates unused entries)
- `meshConfig.trustDomain: p5.local`
- `SPIFFE_ENDPOINT_SOCKET` setting in proxy metadata
- The `spire` sidecar injection template

**What didn't**:
- Sidecars opting into the `spire` template via annotation —
  attestation fails consistently. Removed from deployments.

**Path forward** (for a later week):
- Upgrade SPIRE to 1.15+ which has reworked container detection
- Or switch the agent's k8s attestor to use the docker/containerd
  CRI socket directly (skipping the kubelet API)
- Or use a non-kind cluster (k3s, minikube, real) for the W6 demo

The W6 thesis — *mesh-managed mTLS + declarative authZ* — is met
either way. SPIRE-as-CA is a *unification* goal, not a *correctness*
goal.

---

## Lessons learned

### `clusters/dev/bootstrap/` files don't auto-update

Editing an ArgoCD `Application` YAML in Git and committing **does
nothing** in our setup — ArgoCD only watches the source the
Application points at, not the Application file itself. We
must `kubectl apply -f` the Application after editing. The
permanent fix is an App-of-Apps (W10 cleanup).

### Helm + ArgoCD has unavoidable cosmetic drift

Even with `ignoreDifferences` and `ServerSideApply`, Istio's webhook
configs report `OutOfSync` because istiod patches the `caBundle`
itself at runtime. This is `OutOfSync / Healthy` — annoying but
benign. Real production runs accept this.

### Trust domain alignment is non-negotiable

Istio defaults to `cluster.local`; SPIRE defaults to whatever you
configured (we use `p5.local`). Mismatch → all mTLS handshakes fail
with "trust domain mismatch" or "SAN does not match expected". Set
`meshConfig.trustDomain` to match SPIRE explicitly. Spent debugging
time we shouldn't have to.

### The `spiffe://` prefix in AuthorizationPolicy.principals is a trap

Istio's policy translator **prepends** `spiffe://` to every principal
string. Writing the full `spiffe://...` in the YAML produces
`spiffe://spiffe://...` in the rendered Envoy RBAC — never matches.
The correct form is **just `<trust-domain>/ns/<ns>/sa/<sa>`**, no
prefix. This is documented inconsistently across Istio docs/blogs
and easy to get wrong.

### Port-name protocol detection matters

If the Service port is named `https` Istio assumes the app does its
own TLS and falls back to L4 routing — AuthorizationPolicy matching
by HTTP fields silently doesn't apply. Naming the port `http` was
the difference between "policy works" and "policy looks correct,
silently bypassed." General rule: **always name the port for what the
app speaks, not what's on the wire**.

### SPIRE+Istio integration is fragile on kind

Multiple subtle issues: socket filename collision
(`spire-agent.sock` vs `socket`), readOnly CSI mount preventing
istio-agent's own SDS startup, attestor failing to find container IDs.
Each individually solvable; in aggregate, hours of debugging. Not
representative of a production-on-GKE setup, but worth knowing if
your local dev is kind.

### Mesh revocation is FAST

Phase D's revocation demo was ~10x faster than W5's. That's because
identity (cert) revocation in SPIRE is bounded by SVID TTL, but
authorization revocation in Istio is bounded by xDS push latency
(seconds). For "this team's access is being shut off NOW" scenarios,
mesh policies are the right tool.

---

## Behavioural change worth noting

| | Before (W5) | After (W6) |
| --- | --- | --- |
| Where mTLS happens | In `main.go` via go-spiffe | In istio-proxy sidecar (Envoy) |
| App code lines | ~80 (mTLS) + ~40 (Go HTTP) per binary | ~30 (plain HTTP) per binary |
| App dependencies | `go-spiffe v2.5.0` + transitives | stdlib only (`net/http`) |
| Auth check | `tlsconfig.AuthorizeID()` in handshake | `AuthorizationPolicy` CRD in cluster |
| Auth check location | Code path before HTTP handler | RBAC filter in sidecar; never reaches app |
| Revocation latency | ~minutes (SVID TTL) | ~seconds (xDS push) |
| Restart pods to revoke | Yes, to drop cached SVID | No — policy is runtime |
| Auth signal | Cryptographic, in TLS handshake | Cryptographic, in TLS handshake AND replayed as HTTP header for the app |
| Container count per pod | 1 | 2 (app + istio-proxy) |

---

## What's next (Week 7)

The mesh is wired and authZ is declarative. The remaining elephant
in the room is **static credentials**: AWS keys, DB passwords, etc.
that today still live in K8s Secrets or env vars. W7 brings in
HashiCorp Vault and wires it with SPIFFE auth:

1. Install Vault.
2. Configure Vault's `cert` or `jwt` auth method to accept SPIFFE
   IDs / JWT-SVIDs.
3. Have demo-server request a short-lived AWS credential at runtime
   by presenting its SPIFFE identity.
4. Demonstrate: with no static creds anywhere, the workload still
   talks to AWS — and removing the SPIFFE-to-Vault policy cuts off
   that access instantly.

W7 brings the SPIRE side of the project back into the spotlight —
since Vault talks **directly** to SPIRE, we sidestep the Istio
attestor issue and get to use SPIRE properly again.

---

## References

- Istio AuthorizationPolicy: <https://istio.io/latest/docs/reference/config/security/authorization-policy/>
- Istio + SPIRE integration: <https://istio.io/latest/docs/ops/integrations/spire/>
- spiffe-csi-driver: <https://github.com/spiffe/spiffe-csi>
- This week's commit range: `4df2731..ea2b6dc`
