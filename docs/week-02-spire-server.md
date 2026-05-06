# Week 2 — SPIRE Server + Node Attestor (PSAT)

**Status:** Complete
**Date:** 2026-05-06
**Trust domain established:** `p5.local`

---

## Goal

Stand up the SPIRE control plane as the cryptographic identity authority for
the cluster. By the end of this week the cluster has a self-bootstrapped CA
under the trust domain `p5.local`, ready to issue SVIDs to agents (Week 3) and
workloads (Week 4+).

Everything is deployed via the GitOps pipeline from Week 1 — no `kubectl apply`
of SPIRE manifests; all changes flow through Git → ArgoCD.

---

## Concepts introduced

| Term | Plain meaning |
| --- | --- |
| **SPIFFE** | The written specification for workload identity (`spiffe://<trust-domain>/<path>`). Just a standard. |
| **SPIRE** | The reference implementation that follows SPIFFE. Server + Agent. |
| **SVID** | SPIFFE Verifiable Identity Document — a short-lived X.509 cert with a SPIFFE URI in its SAN. |
| **Trust domain** | The unique namespace for all identities in this cluster. Ours is `p5.local`. |
| **Node attestation** | The process by which SPIRE proves a node is real before issuing it an identity. |
| **PSAT** | Projected Service Account Token — the K8s-native node attestor. Each agent presents a projected K8s SA token; the server validates it with the K8s `TokenReview` API. |

---

## Architecture (end of Week 2)

```
                      Kubernetes cluster (kind: p5-dev)
                      ┌───────────────────────────────────────────┐
                      │                                           │
                      │   namespace: argocd                       │
                      │   ┌─────────────────────────────────┐     │
                      │   │ ArgoCD (from Week 1)            │     │
                      │   │ watches GitHub                  │     │
                      │   └─────────────┬───────────────────┘     │
                      │                 │                         │
                      │                 │ deploys                 │
                      │                 ▼                         │
                      │   namespace: spire-server                 │
                      │   ┌─────────────────────────────────┐     │
                      │   │ StatefulSet spire-server (1)    │     │
                      │   │   ├── pod spire-server-0        │     │
                      │   │   │   image: spire-server:1.14.6│     │
                      │   │   │   gRPC :8081  health :8080  │     │
                      │   │   └── PVC spire-data 1Gi        │     │
                      │   │       (SQLite + signing keys)   │     │
                      │   ├── ServiceAccount spire-server   │     │
                      │   ├── ClusterRole + Binding         │     │
                      │   │     (tokenreviews + nodes/pods) │     │
                      │   ├── ConfigMap spire-server-config │     │
                      │   └── Service spire-server          │     │
                      │       (headless ClusterIP :8081)    │     │
                      │   └─────────────────────────────────┘     │
                      │                                           │
                      └───────────────────────────────────────────┘
```

The SPIRE Server is the only thing running in the `spire-server` namespace
this week. It is the **issuer of identities** for everything in upcoming
weeks (agents in W3, workloads in W4, mTLS in W5, Vault auth in W6, etc.).

---

## Files added to the GitOps repo

```
infrastructure/spire/
├── kustomization.yaml      # bundles all six resources for ArgoCD
├── namespace.yaml          # spire-server namespace
├── serviceaccount.yaml     # SPIRE Server's K8s identity
├── rbac.yaml               # ClusterRole + Binding (tokenreviews, nodes, pods)
├── configmap.yaml          # HCL config — trust domain, plugins
├── statefulset.yaml        # the Server pod itself + 1Gi PVC
└── service.yaml            # headless ClusterIP service on :8081

clusters/dev/bootstrap/
└── spire-server.yaml       # ArgoCD Application pointing at infrastructure/spire/
```

### Key configuration choices

| Decision | Choice | Why |
| --- | --- | --- |
| Trust domain | `p5.local` | Clearly project-scoped; no clash with public DNS. |
| K8s cluster name (PSAT) | `p5-dev` | Matches the kind cluster name. The PSAT plugin ties tokens to this name. |
| Image / version | `ghcr.io/spiffe/spire-server:1.14.6` | Latest stable at time of writing — pinned for reproducibility. |
| Workload type | `StatefulSet` (not Deployment) | Server has identity (CA private key, SQLite DB) that must survive pod restarts on the same volume. |
| Service type | Headless ClusterIP | StatefulSet pods need stable per-pod DNS for agents in Week 3. |
| DataStore | SQLite on PVC | Single-replica is fine for now. HA + Postgres = later. |
| KeyManager | `disk` | Simplest. Production would use HSM or cloud KMS. |
| `service_account_allow_list` | `spire-server:spire-agent` | Pre-staged for Week 3 — only agents using that SA in that namespace can attest. |

---

## How it was deployed

The pipeline is identical to Week 1's demo-app deployment:

1. Wrote the seven YAML files locally.
2. `git add infrastructure/spire/ clusters/dev/bootstrap/spire-server.yaml`
3. `git commit -m "Week 2: deploy SPIRE Server via GitOps"`
4. `git push origin main`
5. `kubectl apply -f clusters/dev/bootstrap/spire-server.yaml`
   (one-time bootstrap of the Application — every change after this flows through Git only).
6. ArgoCD pulled the manifests and reconciled the cluster.

**Once the Application existed in ArgoCD, no further `kubectl` was used to
modify SPIRE.** Even when we hit a sync issue (see Lessons learned), the fix
was edited in Git and pushed.

---

## Verification

Inside the running pod:

```bash
# 1. Health check
kubectl exec -n spire-server spire-server-0 -- /opt/spire/bin/spire-server healthcheck
# Server is healthy.

# 2. The trust bundle (root CA the cluster will trust)
kubectl exec -n spire-server spire-server-0 -- /opt/spire/bin/spire-server bundle show

# 3. Registered entries (none yet — Week 3+)
kubectl exec -n spire-server spire-server-0 -- /opt/spire/bin/spire-server entry show
# Found 0 entries

# 4. Attested agents (none yet — Week 3)
kubectl exec -n spire-server spire-server-0 -- /opt/spire/bin/spire-server agent list
# No attested agents found

# 5. Mint a test SVID to prove issuance works
kubectl exec -n spire-server spire-server-0 -- /opt/spire/bin/spire-server x509 mint \
  -spiffeID spiffe://p5.local/test/healthcheck -ttl 60s
# X509-SVID returned with SPIFFE URI in the SAN extension.
```

RBAC verification (from any kubectl context):

```bash
kubectl auth can-i create tokenreviews \
  --as=system:serviceaccount:spire-server:spire-server
# yes
```

Entry CRUD round-trip (from inside the pod):

```bash
# create
spire-server entry create \
  -spiffeID spiffe://p5.local/test/demo-workload \
  -parentID spiffe://p5.local/spire/agent/k8s_psat/p5-dev/PLACEHOLDER \
  -selector k8s:ns:demo \
  -selector k8s:pod-label:app:demo-app

# show -> Found 1 entry
# delete -> Deleted 1 entries successfully
# show -> Found 0 entries
```

Server logs confirm bootstrap:

```
"Configured plugin" plugin_name=k8s_psat plugin_type=NodeAttestor
"Plugin loaded"     plugin_name=k8s_psat plugin_type=NodeAttestor
"Signed X509 SVID"  spiffe_id="spiffe://p5.local/spire/server"
"Starting Server APIs" address="[::]:8081" network=tcp
"Serving health checks" address="0.0.0.0:8080"
```

---

## Lessons learned

### `ServerSideApply=true` causes cosmetic `OutOfSync` for StatefulSets

Initially the Application was set with `syncOptions: [ServerSideApply=true]`
(carried over from the ArgoCD install where it was needed for >256 KB CRDs).
After deploy the Application showed `Sync: OutOfSync` while `Health: Healthy` —
the StatefulSet was working perfectly, but the K8s API server added defaults
during server-side apply that ArgoCD's diff calculator interpreted as drift.

**Fix:** removed `ServerSideApply=true` from the Application's syncOptions and
pushed via Git. After the next reconcile: `Sync: Synced`. No `kubectl edit` —
the fix flowed through Git, which is the whole point of GitOps.

### Distroless images have no shell

`kubectl exec ... -- /bin/sh` returns `stat /bin/sh: no such file or directory`.
The SPIRE image is `FROM scratch` — no shell, no `ls`, no debugging tools.
This is a security feature (smaller attack surface), not a bug. Use the
SPIRE CLI (`/opt/spire/bin/spire-server ...`) instead.

### StatefulSet vs Deployment is not interchangeable here

A Deployment would lose the CA private key on pod replacement (PVC is not
auto-attached to a stateless pod). The first restart would force every
agent to re-attest and every workload to lose its identity. StatefulSet
guarantees the same pod name (`spire-server-0`) returns to the same PVC.

---

## What's next (Week 3)

Deploy SPIRE Agents as a DaemonSet (one per node):

- New SA `spire-agent` in the `spire-server` namespace (matches our
  `service_account_allow_list` pre-stage).
- Agents attest via PSAT to our server at
  `spire-server.spire-server.svc.cluster.local:8081`.
- Once attested, agents will deliver workload SVIDs via the SPIFFE Workload
  API socket mounted into pods.

The verification target for Week 3:
```bash
spire-server agent list
# 3 attested agents (one per node: control-plane, worker, worker2)
```

---

## References

- SPIFFE specification: <https://github.com/spiffe/spiffe>
- SPIRE docs: <https://spiffe.io/docs/latest/>
- PSAT NodeAttestor: <https://github.com/spiffe/spire/blob/main/doc/plugin_server_nodeattestor_k8s_psat.md>
- This week's commit range: `9e6547e..91249e0`
