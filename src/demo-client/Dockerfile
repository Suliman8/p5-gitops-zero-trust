# =============================================================================
# Stage 1 — builder
# =============================================================================
# Same builder stage as demo-server's Dockerfile — see that file for the
# rationale behind each flag (CGO_ENABLED=0, -ldflags, -trimpath, etc.).
# Kept structurally identical so the two are easy to diff.
FROM golang:1.22.11-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -trimpath \
    -o /out/demo-client \
    .

# =============================================================================
# Stage 2 — runtime (distroless)
# =============================================================================
# Same distroless base as demo-server. The :nonroot tag preconfigures UID
# 65532 so the container can never run as root even if the K8s manifest
# forgets to set runAsNonRoot.
#
# Note: NO `EXPOSE` line here. demo-client is a pure egress workload — it
# dials out, it never listens. EXPOSE is metadata only (K8s ignores it),
# but leaving it out makes the file honest: anyone reading this Dockerfile
# can see at a glance that this is a client, not a server.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/demo-client /demo-client

ENTRYPOINT ["/demo-client"]
