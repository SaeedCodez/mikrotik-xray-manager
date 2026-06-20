# ---- build stage ----
FROM golang:1.23-alpine AS builder
WORKDIR /app

# curl + unzip are needed to fetch the xray-core release for this architecture.
RUN apk --no-cache add curl unzip

# No external Go dependencies — copy the module file and the source, then build.
# web/static is embedded into the binary via //go:embed, so nothing else is needed.
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o xray-manager .

# Download the matching xray-core binary for the architecture we're building on.
# Building natively on the target machine (e.g. the Raspberry Pi) means `uname -m`
# already reflects the right arch, so the image is fully self-contained — no need
# to install or mount xray on the host.
RUN set -eu; \
    case "$(uname -m)" in \
      x86_64|amd64)   asset="Xray-linux-64.zip" ;; \
      aarch64|arm64)  asset="Xray-linux-arm64-v8a.zip" ;; \
      armv7l)         asset="Xray-linux-arm32-v7a.zip" ;; \
      armv6l)         asset="Xray-linux-arm32-v6.zip" ;; \
      *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;; \
    esac; \
    echo "fetching xray-core: $asset"; \
    curl -fL --retry 3 -o /tmp/xray.zip \
      "https://github.com/XTLS/Xray-core/releases/latest/download/${asset}"; \
    unzip -o /tmp/xray.zip xray -d /usr/local/bin; \
    chmod 0755 /usr/local/bin/xray; \
    /usr/local/bin/xray version | head -n1

# ---- runtime stage ----
FROM alpine:3.19
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app

COPY --from=builder /app/xray-manager .
COPY --from=builder /usr/local/bin/xray /usr/local/bin/xray

ENV APP_PORT=8080 \
    DATA_DIR=/app/data \
    XRAY_BINARY=/usr/local/bin/xray

EXPOSE 8080 10808 10809
CMD ["./xray-manager"]
