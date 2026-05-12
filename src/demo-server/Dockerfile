# =============================================================================
# Stage 1 — builder
# =============================================================================
# We use the official golang image just long enough to compile the binary.
# Everything in this stage is THROWN AWAY at the end — the only thing that
# survives into the final image is the single compiled binary copied below.
#
# Why pin to 1.22.11-alpine?
#   - 1.22 matches the version in go.mod (toolchain compatibility).
#   - -alpine is smaller than the default debian-based golang image, which
#     means faster builds and less data over the wire.
#   - Pinning to a patch version (.11) means the build is reproducible:
#     six months from now this Dockerfile produces the same artifact.
FROM golang:1.22.11-alpine AS builder

WORKDIR /src

# Copy go.mod/go.sum FIRST and run `go mod download` BEFORE copying the rest
# of the source. This is a Docker layer-caching trick: if only main.go changes
# but dependencies don't, the `go mod download` layer is reused from cache.
# Without this, every code change re-downloads all deps. Big speed-up.
COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./

# CGO_ENABLED=0 → static binary (no libc dependency).
#   This is what lets us use the distroless/static image below — that image
#   has no glibc, no musl, nothing. A CGO-enabled binary would crash with
#   "exec format error" or "no such file or directory" trying to load libc.
#
# GOOS=linux  → cross-compile target. Even though we're building INSIDE a
#   linux container, this makes the intent explicit and protects against
#   the build running in a different environment later.
#
# -ldflags="-s -w" → strip the symbol table and DWARF debug info from the
#   binary. Shrinks the binary ~30%, and makes reverse-engineering harder
#   (an attacker who gets the binary can't see function names easily).
#   We can always rebuild with symbols if we need to debug.
#
# -trimpath → remove the local build path from the binary. Without this,
#   panic stack traces would leak "/home/kali/Desktop/..." paths.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -trimpath \
    -o /out/demo-server \
    .

# =============================================================================
# Stage 2 — runtime (distroless)
# =============================================================================
# gcr.io/distroless/static-debian12:nonroot is a 2MB image with:
#   - No shell (/bin/sh does not exist)
#   - No package manager
#   - No coreutils (ls, cat, etc. do not exist)
#   - Just glibc-free runtime essentials + CA certificates
#
# The :nonroot tag preconfigures USER 65532 (a non-root UID) so even if
# someone forgets to set securityContext.runAsNonRoot in the K8s manifest,
# the container won't run as root.
#
# Why this matters for a SPIFFE workload:
#   - If our Go code has an RCE bug and the attacker pops a shell — there
#     is no shell to pop. They get RCE in the Go process and that's it.
#   - They can't `cat /etc/passwd`, can't `curl` an exfil endpoint, can't
#     download a kubectl binary. The container is a single binary + libc-less
#     runtime, full stop.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/demo-server /demo-server

# Document the port. This is metadata only — Kubernetes ignores EXPOSE and
# uses the containerPort in the manifest instead. But it's helpful for
# `docker run` and for human readers.
EXPOSE 8443

# Use the binary as the entrypoint so `docker run <image>` just runs it.
# No `sh -c` wrapper means signals (SIGTERM on pod delete) are delivered
# directly to the Go process, which lets it shut down cleanly.
ENTRYPOINT ["/demo-server"]
