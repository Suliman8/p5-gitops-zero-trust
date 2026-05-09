# Week 3 — SPIRE Agents + Workload Attestation

**Status:** Complete
**Date:** 2026-05-09
**Outcome:** A pod fetched a real X.509 SVID via the agent socket — zero secrets in Git.

---

## Goal

Deploy a SPIRE Agent on every node, prove each agent's identity to the
Server (PSAT attestation), then issue a workload SVID to a pod by matching
on namespace + ServiceAccount selectors.

Three milestones:

1. `spire-server agent list` → "Found 3 attested agents"
2. A registration entry maps `(ns=demo, sa=default)` → `spiffe://p5.local/demo-app`
3. A pod in `demo` ns runs `spire-agent api fetch x509` and gets back a real SVID.

Everything is GitOps-driven. No `kubectl apply` of SPIRE manifests — only
the one-time bootstrap of the ArgoCD `Application`.

---

## Concepts introduced

| Term | Plain meaning |
| --- | --- |
| **DaemonSet** | A workload that K8s schedules exactly once on every node. Right shape for the agent — each workload pod must reach the agent on its own node via a hostPath socket. |
| **Workload API** | The local Unix socket where the agent serves SVIDs to workloads. Any pod that mounts the socket can ask "give me my SVID." |
| **Workload attestation** | The process the agent uses to determine *which* workload is asking. It inspects the caller's PID via `hostPID` and asks the K8s API for that pod's metadata (ns, SA, labels). |
| **Selectors** | The "fingerprint" used to match a pod to a registration entry. Examples: `k8s:ns:demo`, `k8s:sa:default`, `unix:uid:1000`. |
| **Registration entry** | The rule: "any workload that matches *these selectors* should be issued *this SPIFFE ID*, signed by *this parent*." |
| **Node alias** | A grouping mechanism that lets a single registration entry apply to all agents in a cluster (otherwise we'd have to enumerate each agent). |
| **SVID rotation** | Agents automatically renew SVIDs before expiration (~half the TTL by default). The example SVID rotated every ~30 min in our run. |

---

## Architecture (end of Week 3)

```
                      Kubernetes cluster (kind: p5-dev)
                      ┌─────────────────────────────────────────────────┐
                      │                                                 │
                      │   namespace: spire-server                       │
                      │   ┌───────────────────────────┐                 │
                      │   │ StatefulSet spire-server  │ (Week 2)        │
                      │   │  registration entries:    │                 │
                      │   │   - node alias            │                 │
                      │   │       k8s-cluster/p5-dev  │                 │
                      │   │   - workload entry        │                 │
                      │   │       demo-app            │                 │
                      │   └─────────────┬─────────────┘                 │
                      │                 │ gRPC :8081                    │
                      │                 │ (PSAT)                        │
                      │                 ▼                               │
                      │   ┌───────────────────────────────────┐         │
                      │   │ DaemonSet spire-agent  (Week 3)   │         │
                      │   │   pod on control-plane            │         │
                      │   │   pod on worker                   │         │
                      │   │   pod on worker2                  │         │
                      │   │     - hostNetwork: true           │         │
                      │   │     - hostPID: true               │         │
                      │   │     - hostPath socket at          │         │
                      │   │       /run/spire/agent-sockets/   │         │
                      │   └───────────────────────────────────┘         │
                      │                 ▲                               │
                      │                 │ unix socket                   │
                      │                 │ (Workload API)                │
                      │   namespace: demo                               │
                      │   ┌───────────────────────────────────┐         │
                      │   │ pod (sa=default)                  │         │
                      │   │   mounts /run/spire/agent-sockets │         │
                      │   │   → fetches X.509 SVID            │         │
                      │   │   → identity:                     │         │
                      │   │     spiffe://p5.local/demo-app    │         │
                      │   └───────────────────────────────────┘         │
                      │                                                 │
                      └─────────────────────────────────────────────────┘
```

The agent is the link between the Server (which has the CA) and the
workload pods (which need short-lived certs). It never holds long-lived
secrets — it itself rotates its own SVID every hour.

---

## Files added to the GitOps repo

```
infrastructure/spire-agent/
├── kustomization.yaml         # bundles the five resources for ArgoCD
├── serviceaccount.yaml        # spire-agent SA (target of PSAT attestation)
├── rbac.yaml                  # ClusterRole + Binding (pods, nodes, nodes/proxy)
├── configmap.yaml             # HCL agent config — server addr, attestors, plugins
└── daemonset.yaml             # one agent pod per node, hostNetwork+hostPID+hostPath

clusters/dev/bootstrap/
└── spire-agent.yaml           # ArgoCD Application pointing at infrastructure/spire-agent/
```

### Key configuration choices

| Decision | Choice | Why |
| --- | --- | --- |
| Workload type | `DaemonSet` | Need exactly one agent per node so pod sockets resolve locally. Deployment can't guarantee node placement; StatefulSet is for ordered identity. |
| `hostPID: true` | yes | Workload attestor inspects the caller's PID to find its pod. Without host PID it can't see workload processes. |
| `hostNetwork: true` | yes (initially false → fixed) | The K8s workload attestor talks to kubelet/API on the node. With `hostNetwork: false`, `127.0.0.1` resolves to the agent's own loopback — kubelet unreachable. |
| Tolerations | `node-role.kubernetes.io/control-plane:NoSchedule` | So one agent runs on the control-plane node too (otherwise only 2 nodes covered). |
| Socket mount | hostPath `/run/spire/agent-sockets` | A directory on the actual node, shared between the agent and any workload pod that mounts the same path. |
| Image | `ghcr.io/spiffe/spire-agent:1.14.6` | Pinned to match Server. |
| imagePullPolicy | `IfNotPresent` | Recovery lesson from W1/W2 — pinned tags are immutable; avoid registry calls when DNS is flaky. |
| `insecure_bootstrap` | `true` | Learning shortcut. Production: pre-distribute the Server's CA bundle so the very first agent connection is verified. |
| KeyManager | `memory` | Agent's signing key is regenerated on pod restart; that's fine because a fresh SVID is issued anyway. |

---

## How attestation chained together

```
1. K8s creates a projected SA token (audience=spire-server, ~1h TTL)
   for each spire-agent pod.

2. Agent boots, reads the token, opens gRPC to spire-server:8081.

3. Server sees the token, calls K8s TokenReview API:
       "Is this token real, audience=spire-server, valid?"
   K8s replies "yes, sa=spire-agent in ns=spire-server."

4. Server matches that against service_account_allow_list
   (spire-server:spire-agent → allow). Issues the agent its node SVID:
       spiffe://p5.local/spire/agent/k8s_psat/p5-dev/<uuid>

5. Agent stores the SVID in memory and starts serving the local
   Workload API socket.

6. Workload pod opens the socket, calls FetchX509SVID.

7. Agent inspects caller's PID (hostPID) → resolves to a K8s pod
   via the API server's /nodes/<n>/proxy/pods endpoint
   → namespace + ServiceAccount become selectors
   k8s:ns:demo, k8s:sa:default.

8. Agent matches selectors against entries it received from the Server.
   If it matches an entry, it relays a fresh X.509 SVID to the workload.
```

---

## Verification

```bash
# 3 agents attested to the Server
kubectl -n spire-server exec spire-server-0 -- \
    /opt/spire/bin/spire-server agent list
# Found 3 attested agents

# Registered entries (1 node alias + 1 workload)
kubectl -n spire-server exec spire-server-0 -- \
    /opt/spire/bin/spire-server entry show
# Found 2 entries:
#   spiffe://p5.local/k8s-cluster/p5-dev   (parent: spire/server)
#   spiffe://p5.local/demo-app             (parent: k8s-cluster/p5-dev)

# Workload SVID fetched from inside a pod with the right selectors
kubectl -n demo run svid-tester \
    --image=ghcr.io/spiffe/spire-agent:1.14.6 ...
# Logs:
#   SPIFFE ID:  spiffe://p5.local/demo-app
#   SVID Valid Until:  <now + ~1h>
```

---

## Lessons learned

### `hostNetwork: false` breaks the K8s workload attestor

The agent's K8s WorkloadAttestor calls the kubelet/API server on
`127.0.0.1:10250` to look up pod metadata. With `hostNetwork: false`,
that resolves to the **agent pod's own loopback** — not the node — and
kubelet is unreachable. Result: every workload that asks for an SVID
gets `PermissionDenied: no identity issued`.

**Fix:** set `hostNetwork: true` (and keep `dnsPolicy: ClusterFirstWithHostNet`
so the agent can still resolve the Server's headless service name).
Pushed via Git, ArgoCD redeployed.

### Agent 1.14+ uses the API-server proxy for kubelet — needs `nodes/proxy` RBAC

After fixing hostNetwork, the next error was:
```
unexpected status code on pods response: 403 Forbidden
(verb=get, resource=nodes, subresource=[pods proxy])
```

The 1.14 series defaults to a "new container locator" that calls
`GET /api/v1/nodes/<node>/proxy/pods` through the K8s API server (not
kubelet directly). That requires the `nodes/proxy` resource permission,
which we hadn't granted.

**Fix:** added `nodes/proxy` (verbs: `get`) to the spire-agent ClusterRole.
Pushed via Git.

### `kind load docker-image` recurring bug

Same `ctr: content digest sha256:… not found` issue as Week 2. Workaround
is now muscle memory: `docker save … | docker exec -i <node> ctr import -`
per node.

### Distroless images: no `sleep`, no `/bin/sh`

The first svid-tester pod used `command: ["sleep", "3600"]` to keep
itself alive. The `spire-agent` image is `FROM scratch` — no `sleep`, no
shell, just the agent binary. Switched to running
`/opt/spire/bin/spire-agent api fetch x509 …` directly so the pod runs,
prints the SVID, and exits.

### Imperative entries are the GitOps debt for next week

`spire-server entry create` is fine for learning, but the entries don't
live in Git — recreating the cluster wipes them. **Week 4 should adopt
SPIRE Controller Manager** so entries are declared as `ClusterSPIFFEID`
CRDs in `infrastructure/` and reconciled by ArgoCD like everything else.

---

## What's next (Week 4)

- Move workload entries into Git via SPIRE Controller Manager (CRDs).
- Wire the demo-app Deployment to mount the agent socket and consume its
  SVID for real (currently demo-app doesn't yet do anything with SPIFFE).
- Start using SVIDs for mTLS between two services so identity is
  *enforced*, not just *issued*.

---

## References

- SPIRE Agent docs: <https://spiffe.io/docs/latest/deploying/spire_agent/>
- K8s WorkloadAttestor: <https://github.com/spiffe/spire/blob/main/doc/plugin_agent_workloadattestor_k8s.md>
- SPIRE Controller Manager: <https://github.com/spiffe/spire-controller-manager>
- This week's commit range: `c3d7bec..38b77c0`
