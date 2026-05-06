# Project 5 — GitOps-Native Zero-Trust Kubernetes (SPIFFE / SPIRE)

> ArgoCD · SPIRE · Istio · Vault — Eliminating network trust assumptions across a cluster

## What this project is

A learning project that turns a normal "trust-everyone" Kubernetes cluster into a
**zero-trust** cluster where:

- Every workload has its own **cryptographic identity** (SPIFFE/SPIRE)
- All pod-to-pod traffic is encrypted by default (**mTLS** via service mesh)
- **No static credentials** live in the cluster — Vault hands out short-lived secrets on demand
- Authorization is **identity-based**, not namespace-based (OPA/Gatekeeper)
- The whole cluster is declared in this **Git repo** and reconciled by **ArgoCD**

## Repository layout

| Path | Purpose |
| --- | --- |
| `clusters/<name>/bootstrap/` | The "root app" — single ArgoCD Application that bootstraps everything else (App-of-Apps pattern) |
| `infrastructure/` | Cluster-wide platform components: ArgoCD, SPIRE, Istio, Vault, Gatekeeper |
| `apps/` | Workload applications (the "tenants") |
| `docs/` | Architecture diagrams and operator run-book |

## Weekly plan

| Week | Goal |
| --- | --- |
| W1 | ArgoCD bootstrap + GitOps repo structure |
| W2 | SPIRE server + node attestor |
| W3 | SPIRE agents + workload attestation |
| W4 | Istio/Linkerd install + SPIFFE integration |
| W5 | mTLS everywhere + AuthorizationPolicy |
| W6 | Vault + SPIFFE auth (kill static credentials) |
| W7 | OPA/Gatekeeper identity-based authZ |
| W8 | Drift detection + auto-reconcile tests |
| W9 | Chaos test — attacker pod isolation proof |
| W10 | Architecture diagrams + run-book |

## Local environment

- Kubernetes: `kind` (3-node cluster — 1 control-plane + 2 workers)
- Tooling: kubectl, helm, git, docker, argocd CLI

## How GitOps works here

1. You change a YAML file in this repo and commit it.
2. ArgoCD (running in the cluster) sees the change.
3. ArgoCD applies it to the cluster automatically.
4. If anyone changes the cluster manually, ArgoCD reverts it back to match Git.

**Git is the only source of truth. The cluster is just a reflection.**
