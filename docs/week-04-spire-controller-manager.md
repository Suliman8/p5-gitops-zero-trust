# Week 4 — SPIRE Controller Manager + GitOps-native workload entries

**Status:** Complete
**Date:** 2026-05-11
**Outcome:** A pod fetched a real SVID issued from a registration entry that
exists only as YAML in Git. The `spire-server entry create` CLI is no longer
in the loop.

---

## Goal

Move workload identity definitions out of the SPIRE Server's SQLite DB
and into Git, where they can be reviewed, versioned, and reconciled by
ArgoCD like every other resource in the cluster.

Three milestones:

1. `ClusterSPIFFEID` and `ClusterStaticEntry` CRDs registered in the cluster.
2. The SPIRE Controller Manager runs as a sidecar in the `spire-server`
   pod and translates those CRDs into SPIRE registration entries.
3. A pod matched by a `ClusterSPIFFEID` gets issued a real SVID — with
   nothing entered by hand on the Server.

---

## Concepts introduced

| Term | Plain meaning |
| --- | --- |
| **CRD** | Custom Resource Definition — extends the Kubernetes API with new types. ArgoCD treats CRs like any other resource. |
| **`ClusterSPIFFEID`** | A rule that says "every pod matching these label selectors should be issued an SVID with SPIFFE ID *X*." Generates one SPIRE entry **per matched pod**, scoped by `k8s:pod-uid`. |
| **`ClusterStaticEntry`** | A literal entry, the same shape as `spire-server entry create`. Used for things like node aliases where the SPIFFE ID is fixed. |
| **`ClusterFederatedTrustDomain`** | Federation across trust domains. Not used yet — will matter in cross-cluster work. |
| **Admin API socket** | The privileged Unix socket SPIRE Server serves locally for entry CRUD. The controller manager talks to this. |
| **Sidecar pattern** | Two containers in one pod, sharing volumes. Lets the controller manager reach the admin socket over a local file path instead of over the network. |
| **Per-pod entry** | New model where one CRD rule generates one SPIRE entry per matched pod, keyed by `k8s:pod-uid`. Stricter than the old `k8s:ns + k8s:sa` selector pair, and self-reaping when pods are deleted. |

---

## Architecture (end of Week 4)

```
                          Git: infrastructure/spire-entries/
                          ├── node-alias.yaml    (ClusterStaticEntry)
                          └── demo-app.yaml      (ClusterSPIFFEID)
                                       │
                                       │ ArgoCD reconciles
                                       ▼
                          Kubernetes cluster (kind: p5-dev)
                          ┌─────────────────────────────────────────────┐
                          │   namespace: spire-server                   │
                          │   ┌───────────────────────────────────┐     │
                          │   │ pod: spire-server-0  (2 containers)│     │
                          │   │                                   │     │
                          │   │ ┌─────────────────┐               │     │
                          │   │ │ spire-server    │  unix socket  │     │
                          │   │ │ (Week 2)        │◀──/tmp/spire- │     │
                          │   │ └─────────────────┘  server/      │     │
                          │   │                       private/    │     │
                          │   │                       api.sock    │     │
                          │   │ ┌─────────────────┐       ▲       │     │
                          │   │ │ spire-controller│       │       │     │
                          │   │ │ -manager (NEW)  │───────┘       │     │
                          │   │ └─────────────────┘               │     │
                          │   └────┬──────────────────────────────┘     │
                          │        │ K8s watch (CRDs + pods)            │
                          │        ▼                                    │
                          │  ┌─────────────────┐   spire-server gRPC    │
                          │  │ ClusterSPIFFEID │      ▲                 │
                          │  │ ClusterStatic.. │      │                 │
                          │  └─────────────────┘      │                 │
                          │                           │                 │
                          │   namespace: demo         │                 │
                          │   ┌───────────────────────┴────────┐        │
                          │   │ demo-app pods (3)              │        │
                          │   │   each gets a unique entry     │        │
                          │   │   keyed by k8s:pod-uid:<uid>   │        │
                          │   │   SPIFFE ID:                   │        │
                          │   │   spiffe://p5.local/demo-app   │        │
                          │   └────────────────────────────────┘        │
                          └─────────────────────────────────────────────┘
```

---

## Files added / changed

```
infrastructure/spire-controller-manager/
├── kustomization.yaml          # bundles crds + config + rbac
├── crds/
│   ├── kustomization.yaml
│   ├── spire.spiffe.io_clusterspiffeids.yaml
│   ├── spire.spiffe.io_clusterstaticentries.yaml
│   └── spire.spiffe.io_clusterfederatedtrustdomains.yaml
├── config.yaml                 # ControllerManagerConfig ConfigMap
└── rbac.yaml                   # ClusterRole/Binding + leader-election Role

infrastructure/spire-entries/
├── kustomization.yaml
├── node-alias.yaml             # ClusterStaticEntry (was imperative)
└── demo-app.yaml               # ClusterSPIFFEID (was imperative)

infrastructure/spire/
├── configmap.yaml              # (comment-only change — admin socket defaults)
└── statefulset.yaml            # added spire-controller-manager sidecar +
                                # emptyDir for the shared admin socket

clusters/dev/bootstrap/
├── spire-controller-manager.yaml   # ArgoCD App (uses ServerSideApply for
│                                   # the >256 KB kubebuilder CRDs)
└── spire-entries.yaml              # ArgoCD App for the entry CRs
```

### Key configuration choices

| Decision | Choice | Why |
| --- | --- | --- |
| Deployment shape | sidecar in spire-server StatefulSet | Controller has no useful life if Server is down. Unix socket beats TCP + cert auth. Matches the official Helm-chart pattern. |
| Image / version | `ghcr.io/spiffe/spire-controller-manager:0.6.4` | Latest stable at time of writing; compatible with SPIRE 1.14.6. |
| Webhook | disabled via `ENABLE_WEBHOOKS=false` | The webhook needs SPIRE-issued TLS certs to bootstrap. Disabling it keeps the wiring simple — validation still happens at reconcile time and surfaces in CR `status`. |
| `parentIDTemplate` | `spiffe://{{.TrustDomain}}/k8s-cluster/{{.ClusterName}}` | Default template uses `{{.NodeMeta.UID}}` (the K8s Node UID), which does not match the SPIRE-generated UUID embedded in actually-attested agent SVIDs → workloads would get "no identity issued." Chaining to the node alias works for every PSAT agent in the cluster. |
| `validatingWebhookConfigurationName` | set (required field), but webhook disabled | The binary errors out at startup if this is empty even when ENABLE_WEBHOOKS=false. Set to its default name so flipping the webhook on later doesn't require a config migration. |
| Admin socket | `/tmp/spire-server/private/api.sock` | This is the SPIRE 1.14 default for `server.socket_path`. No override needed — just mount an emptyDir at that path so the sidecar can see the same file. |

---

## How a CRD becomes an SVID

```
1. You commit infrastructure/spire-entries/demo-app.yaml (a ClusterSPIFFEID).
2. ArgoCD sees the change → applies it to the cluster.
3. The spire-controller-manager container, watching the SPIRE CRDs,
   sees the new ClusterSPIFFEID.
4. It walks the pod list, finds 3 pods in namespace demo, and emits one
   SPIRE registration entry per pod via the admin socket:
       parent:    spiffe://p5.local/k8s-cluster/p5-dev   (the node alias)
       spiffeID:  spiffe://p5.local/demo-app
       selector:  k8s:pod-uid:<that pod's UID>
5. The Server gossips the new entries down to every attested agent.
6. A demo-app pod opens the agent's Workload API socket → agent does
   workload attestation (hostPID inspect → ask K8s for pod metadata →
   selectors including k8s:pod-uid:<uid>).
7. Agent matches the entry, mints an X.509 SVID, hands it back.
```

The chain that makes this safe:

```
SPIRE root CA  →  spire/server SVID  →  node-alias entry  →  demo-app entry  →  pod
   (W2)              (W2)                 (this week)         (this week)      (runs)
```

---

## Verification

```bash
# CRs declared in Git, visible in cluster
$ kubectl get clusterspiffeids,clusterstaticentries
NAME                                       AGE
clusterspiffeid.spire.spiffe.io/demo-app   2m
NAME                                                               AGE
clusterstaticentry.spire.spiffe.io/k8s-cluster-p5-dev-node-alias   2m

# Controller materialized them as SPIRE entries (3 demo-app + 1 alias)
$ kubectl -n spire-server exec spire-server-0 -c spire-server -- \
    /opt/spire/bin/spire-server entry show
Found 4 entries
  spiffe://p5.local/demo-app  selectors=k8s:pod-uid:<uid-1>
  spiffe://p5.local/demo-app  selectors=k8s:pod-uid:<uid-2>
  spiffe://p5.local/demo-app  selectors=k8s:pod-uid:<uid-3>
  spiffe://p5.local/k8s-cluster/p5-dev  selectors=k8s_psat:cluster:p5-dev

# A test pod in `demo` fetched its SVID
SPIFFE ID:        spiffe://p5.local/demo-app
SVID Valid Until: 2026-05-11 08:54:00 +0000 UTC
CA #4 Valid Until: 2026-05-12 06:57:37 +0000 UTC
```

---

## Lessons learned

### `admin_socket_path` is the wrong field name in SPIRE 1.14

Initial config used `admin_socket_path = "..."` inside the `server { }`
block. SPIRE 1.14 errored out with `Unknown configuration detected` —
the field is named `socket_path`, **and** its default value is already
`/tmp/spire-server/private/api.sock`. The fix was simply to delete the
override and let the default apply.

### The controller manager GCs unmanaged entries on first start

When the new sidecar came up, its very first reconcile pass deleted the
two Week-3 imperative entries (`spiffe://p5.local/k8s-cluster/p5-dev`
and `spiffe://p5.local/demo-app`) — "I don't have a CRD for those, so
they must be drift." This is the *correct* behaviour for a GitOps
controller and is the reason we had to land the CRDs immediately
after the sidecar started; otherwise workloads lose their SVIDs in the
gap.

### Default `parentIDTemplate` does not match PSAT-attested agents

The default template renders parents as
`spiffe://<td>/spire/agent/k8s_psat/<cluster>/{{.NodeMeta.UID}}` — but
that `NodeMeta.UID` is the **Kubernetes Node object UID**, not the
SPIRE-generated UUID in the actually-attested agent's SVID path. The
defaults are designed for environments that attest agents differently.
Setting `parentIDTemplate` to the node alias (`k8s-cluster/<cluster>`)
keeps the chain working without enumerating individual agents.

### `api fetch x509` races the controller on a fresh pod

A one-shot `spire-agent api fetch x509` in a brand-new test pod loses
the race: the agent has not yet received the entry the controller just
created. `api watch` is the right tool — it streams updates and waits
until the SVID arrives (took ~4 s in our run). Operational reality: any
real workload should retry the Workload API call until it gets a SVID,
which is what production SPIFFE libraries do automatically.

### Webhooks need bootstrap certs to function

The controller manager defaults to running an admission webhook server
to validate CRs at write time. That webhook needs TLS certs, which the
controller can mint **from SPIRE itself** — chicken-and-egg if SPIRE
isn't fully up. Setting `ENABLE_WEBHOOKS=false` is the simple escape
hatch; validation still happens at reconcile time. Reviving the
webhook is a candidate for a later week.

---

## Behavioural change worth noting

| | Before (Week 3) | After (Week 4) |
| --- | --- | --- |
| Where entries live | SPIRE Server's SQLite DB | Git → CRDs → SPIRE |
| How a new workload gets identity | Operator runs `spire-server entry create ...` | Engineer commits a `ClusterSPIFFEID` YAML, PR review, merge |
| What happens on cluster delete | Entries vanish (DB on PVC) | Entries reappear on next ArgoCD sync |
| What happens on pod delete | Entry stays as garbage | Entry auto-reaped (was keyed by `k8s:pod-uid`) |
| Selector granularity | `k8s:ns:demo + k8s:sa:default` (loose) | `k8s:pod-uid:<uid>` (one entry per pod) |

---

## What's next (Week 5)

The plumbing now exists for *workloads to actually use their SVIDs*:

1. Wire the `demo-app` Deployment to mount the agent socket, so the app
   itself can call `FetchX509SVID` (currently only test pods do this).
2. Add a second service ("demo-server") and have demo-app call it over
   **SPIFFE mTLS** — each side verifies the other's SPIFFE ID, no shared
   secrets, no API keys, no JWTs.
3. Demonstrate revoking access by deleting one side's `ClusterSPIFFEID`
   and watching the connection fail.

That's the actual zero-trust payoff. Everything until now was setup.

---

## References

- spire-controller-manager v0.6.4: <https://github.com/spiffe/spire-controller-manager/tree/v0.6.4>
- ControllerManagerConfig schema: <https://github.com/spiffe/spire-controller-manager/blob/v0.6.4/api/v1alpha1/controllermanagerconfig_types.go>
- SPIRE Server config reference (1.14): <https://github.com/spiffe/spire/blob/v1.14.6/doc/spire_server.md>
- This week's commit range: `bccc739..967e457`
