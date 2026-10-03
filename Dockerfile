# ---- Build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

# Build the static binary (no cgo, no shell).
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILDTIME=unknown
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILDTIME}" \
    -o /out/nearby-fire-alert ./cmd/nearby-fire-alert

# Pre-create the runtime data directory (distroless has no shell, so it cannot
# run `mkdir`; the directory is created and chowned here, then copied in).
RUN mkdir -p /out/data && chown -R 65532:65532 /out/data

# ---- Runtime stage ----
# Distroless static: non-root, no shell, single static binary.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/nearby-fire-alert /bin/nearby-fire-alert
COPY --from=build --chown=65532:65532 /out/data /data
WORKDIR /data

USER nonroot:nonroot

EXPOSE 18789
ENTRYPOINT ["/bin/nearby-fire-alert"]
