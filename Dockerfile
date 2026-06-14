# ---- build stage ----
FROM golang:1.23-alpine AS builder
WORKDIR /app

# No external dependencies — copy the module file and the source, then build.
# web/static is embedded into the binary via //go:embed, so nothing else is needed.
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o xray-manager .

# ---- runtime stage ----
FROM alpine:3.19
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/xray-manager .

ENV APP_PORT=8080
EXPOSE 8080

# The web UI. xray-core itself should be mounted in (see README) so the app can
# manage it: -v /usr/local/bin/xray:/usr/local/bin/xray
CMD ["./xray-manager"]
