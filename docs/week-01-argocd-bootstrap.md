# Week 1 — ArgoCD Bootstrap + GitOps Repo Structure

**Status:** Complete
**Date:** 2026-05-05 → 2026-05-06
**Outcome:** GitOps pipeline fully operational; demo workload deployed and self-healing proven.

---

## Goal

Lay the foundation for the rest of the project. By the end of Week 1 the
cluster is being driven entirely by Git: a push to `main` is the only
mechanism that may change cluster state. Manual `kubectl` changes are
automatically reverted. Everything that follows in Weeks 2–10 is deployed
through this same pipeline.

---

## Concepts introduced

| Term | Plain meaning |
| --- | --- |
| **Cloud vs cloud-native** | Cloud = where it runs (rented servers). Cloud-native = how it's built (containers, scaling, K8s). |
| **Image / container / pod / node / cluster** | Recipe → baked cake → plate K8s carries → kitchen → restaurant. |
| **Git vs GitHub** | Git = the tool (local, offline). GitHub = a website that hosts Git repos online. |
| **GitOps** | Git is the only source of truth; an in-cluster controller continuously reconciles cluster state to match Git. |
| **Pull-based GitOps** | The cluster pulls from GitHub; GitHub does NOT push to the cluster. Safer (no inbound from internet). |
| **ArgoCD** | The reference GitOps controller for K8s. 7 components, deployed in the `argocd` namespace. |
| **`Application` (CRD)** | The bridge — a YAML object telling ArgoCD which repo, path, branch, and namespace to reconcile. |
| **Kustomize** | Declarative way to bundle and patch K8s manifests. Built into `kubectl` (`apply -k`). |
| **Server-side apply** | Modern apply mode where the API server tracks field ownership instead of stuffing the previous version into a 256 KB annotation. Required for ArgoCD's large `ApplicationSets` CRD. |
| **Drift correction (self-heal)** | If a human mutates the cluster out-of-band, ArgoCD reverts the change to match Git. |

---

## Architecture (end of Week 1)

```
                           Your laptop
                       ┌──────────────────────────────────┐
                       │                                  │
                       │   git push origin main ──────────┼─────►  GitHub
                       │                                  │       Suliman8/
                       │                                  │       p5-gitops-zero-trust
                       │                                  │             ▲
                       │   browser ──► localhost:8453 ────┼──┐          │
                       └──────────────────────────────────┘  │          │ git clone
                                                              │          │ (every 3 min)
                       ┌──────────────────────────────────┐  │          │
                       │  kind cluster: p5-dev            │  │          │
                       │  (3 Docker containers as nodes)  │  │          │
                       │                                  │  │          │
                       │  control-plane                   │  │          │
                       │  ├── port mapping 8090→30080     │  │          │
                       │  └── port mapping 8453→30443  ◄──┼──┘          │
                       │                                  │             │
                       │  namespace: argocd               │             │
                       │  ├── argocd-server (UI/API) ─────┼─────────────┘
                       │  ├── argocd-application-controller (reconciler)
                       │  ├── argocd-repo-server (git clone worker)
                       │  ├── argocd-redis (cache)
                       │  ├── argocd-dex-server (auth)
                       │  ├── argocd-notifications-controller
                       │  └── argocd-applicationset-controller
                       │                                  │
                       │  namespace: demo                 │
                       │  ├── demo-app deployment (3 nginx pods)
                       │  └── demo-app service (ClusterIP)
                       └──────────────────────────────────┘
```

---

## Files added to the GitOps repo

```
.
├── README.md                                # Project overview, weekly plan
├── .gitignore                               # Secrets, kubeconfigs, OS junk
│
├── clusters/dev/
│   ├── kind-config.yaml                     # 3-node kind cluster definition
│   └── bootstrap/
│       └── demo-app.yaml                    # ArgoCD Application for demo-app
│
├── infrastructure/argocd/
│   └── kustomization.yaml                   # Pinned upstream ArgoCD v3.3.9 + NodePort patch
│
└── apps/demo-app/
    ├── namespace.yaml                       # `demo` namespace
    ├── deployment.yaml                      # nginx 1.27-alpine, replicas: 3
    └── service.yaml                         # ClusterIP on :80
```

### Repository layout principles

- **`clusters/<name>/bootstrap/`** — the one folder a new cluster's ArgoCD needs to bootstrap itself.
- **`infrastructure/`** — platform-team plumbing (ArgoCD, SPIRE, Istio, Vault…).
- **`apps/`** — workloads owned by product teams.
- **`docs/`** — architecture diagrams and per-week run-books.

This separation matters when the cluster grows: dozens of apps don't end up
mixed with platform components, and per-cluster choices (which apps each
cluster runs) live in `clusters/<name>/`.

### Key configuration choices

| Decision | Choice | Why |
| --- | --- | --- |
| Local K8s tool | `kind` (3 nodes) | Lightweight, real K8s, multi-node simulation. SPIRE node-attestor in W2 needs >1 node to be meaningful. |
| Host port mappings | 8090 → 30080, 8453 → 30443 | 8080/8443 collide with `taskflow-frontend` already running. |
| ArgoCD install method | Kustomize referencing pinned upstream `v3.3.9` | Tiny YAML in repo, exact version reproducibility, easy to layer patches. |
| ArgoCD UI exposure | `argocd-server` Service patched to `NodePort` 30443 | Uses our pre-mapped kind port → reachable at `https://localhost:8453` without `kubectl port-forward`. |
| Git identity | `Suliman Khan <sulimankhandawar537@gmail.com>` | Local repo config only — links commits to the `Suliman8` GitHub profile. |
| Repo visibility | Public | ArgoCD clones with no auth setup; suitable for a portfolio project. |
| Demo workload | nginx 1.27-alpine, replicas 3 | Tiny image, fast pull, enough to demonstrate scaling and self-heal. |

---

## How it was deployed

### One-time host preparation

Linux inotify limits were too low for running multiple kind clusters
side-by-side (an existing `taskflow-demo` cluster was already eating most
of the default 128 instances). System-wide raise:

```bash
sudo tee /etc/sysctl.d/99-kubernetes.conf > /dev/null <<'EOF'
fs.inotify.max_user_watches = 524288
fs.inotify.max_user_instances = 512
EOF
sudo sysctl --system
```

### Cluster + ArgoCD bootstrap

```bash
# 1. Local K8s
kind create cluster --config clusters/dev/kind-config.yaml

# 2. Install ArgoCD via Kustomize (server-side apply for the >256 KB CRD)
kubectl create namespace argocd
kubectl apply -k infrastructure/argocd/ --server-side --force-conflicts

# 3. ArgoCD CLI matching server version
curl -sSL -o ~/.local/bin/argocd \
  https://github.com/argoproj/argo-cd/releases/download/v3.3.9/argocd-linux-amd64
chmod +x ~/.local/bin/argocd

# 4. Initial admin password (auto-generated, lives only in a Secret)
kubectl -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' | base64 -d
```

### Push to GitHub and let ArgoCD take over

```bash
gh repo create p5-gitops-zero-trust --public --source . --remote origin --push
git add apps/demo-app/ clusters/dev/bootstrap/demo-app.yaml
git commit -m "Add demo-app manifests and ArgoCD Application"
git push origin main

# One-time: tell ArgoCD about the demo-app Application
kubectl apply -f clusters/dev/bootstrap/demo-app.yaml
```

After this, no further `kubectl apply` was used to manage demo-app — every
change went through Git.

---

## Verification

```bash
# 1. The 3-node cluster
kubectl get nodes
# control-plane + worker + worker2, all Ready

# 2. ArgoCD is healthy
kubectl get pods -n argocd
# 7 pods, all Running

# 3. The Application is Synced and Healthy
kubectl get application -n argocd demo-app
# SYNC=Synced  HEALTH=Healthy

# 4. The workload is running
kubectl get deploy,svc,pods -n demo
# deployment 3/3 ready, service ClusterIP, 3 nginx pods Running

# 5. nginx actually responds
kubectl run -it --rm test-curl --image=curlimages/curl --restart=Never \
  -- curl -sI http://demo-app.demo.svc.cluster.local | head -1
# HTTP/1.1 200 OK
```

### Self-heal tests (the proof GitOps works)

| # | Action taken out-of-band | Expected outcome | Result |
| --- | --- | --- | --- |
| 1 | `kubectl scale deploy demo-app --replicas=5` | ArgoCD reverts to 2 (Git's value at the time) | Reverted within ~30 s |
| 2 | `kubectl set image deploy/demo-app nginx=nginx:1.20-alpine` | ArgoCD reverts to 1.27-alpine | Reverted within ~30 s |
| 3 | `kubectl delete svc demo-app` | ArgoCD recreates the Service | Recreated |
| 4 | `kubectl delete deploy demo-app` | ArgoCD recreates the Deployment + pods | Recreated |
| 5 | Edited `replicas: 2` → `replicas: 3` in Git, commit, push | ArgoCD applies the change without fighting | Applied — pods scaled to 3 |

Tests 1–4 prove drift correction. Test 5 proves the *correct* way to change
state — through Git, not the cluster.

---

## Lessons learned

### inotify defaults are too low for multi-cluster local dev

Symptom: a second kind cluster's nodes failed to start with
`Failed to create control group inotify object: Too many open files`.
Root cause: Linux defaults `fs.inotify.max_user_instances=128`; the
existing cluster was already consuming most of them. Raised to 512
system-wide via `/etc/sysctl.d/99-kubernetes.conf`. Documented in the
[references file](../../.claude/projects/-home-kali-Desktop-DevOps--Advance-P5/memory/reference_local_environment.md)
so this isn't lost.

### Port collisions are a fact of life on a dev box

`8080` was already bound by another project. Switched the kind hostPort
mapping to `8090` (HTTP) and `8453` (HTTPS). Lesson: don't assume the
default ports are free; choose project-scoped ports up-front.

### The ArgoCD `ApplicationSets` CRD is over 256 KB

A standard `kubectl apply` stuffs the previous object into a
`last-applied-configuration` annotation, which is capped at 256 KB. The
`ApplicationSets` CRD exceeds that. Solution: install ArgoCD with
`--server-side --force-conflicts`. Server-side apply tracks field
ownership at the API-server level, with no client-side annotation.

### Use the GitHub no-reply email if privacy matters

Initially commits used a system-context email that didn't match the
GitHub account. Switched to the real GitHub email of `Suliman8`. GitHub
also offers a no-reply alias (`<id>+<user>@users.noreply.github.com`) that
keeps the real address private while still linking commits to the
profile — a one-line config change if needed later.

### Keep the bootstrap surface area small

The whole cluster — including ArgoCD itself — is described by Kustomize
files in this repo. Initial cluster bring-up is **one command**
(`kind create cluster`) plus **one apply**
(`kubectl apply -k infrastructure/argocd/ --server-side`). Everything
afterwards is `git push`. This is the model the project will follow for
the next 9 weeks.

---

## What's next (Week 2)

Stand up the SPIRE control plane in a new `spire-server` namespace,
deployed via the same GitOps pipeline. Trust domain `p5.local`. PSAT
node attestor configured for cluster name `p5-dev`. After Week 2 the
cluster has an issuer of cryptographic identities ready for agents to
join in Week 3.

See [`docs/week-02-spire-server.md`](week-02-spire-server.md).

---

## References

- ArgoCD: <https://argo-cd.readthedocs.io/>
- Kustomize: <https://kustomize.io/>
- kind: <https://kind.sigs.k8s.io/>
- Server-side apply: <https://kubernetes.io/docs/reference/using-api/server-side-apply/>
- This week's commit range: `ab54832..9e6547e` (initial repo through Test 6)
