# syntax=docker/dockerfile:1
# Lynx: servidor + painel (e os binários do agente, servidos em /download).

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=0.1.0
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/lynx-server ./cmd/server \
 && for a in amd64 arm64; do \
      GOOS=linux GOARCH=$a go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/bin/lynx-agent-linux-$a ./cmd/agent; \
    done

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S lynx && adduser -S -G lynx -u 10001 lynx \
 && mkdir -p /data && chown lynx:lynx /data
COPY --from=build /out/lynx-server /app/lynx-server
COPY --from=build /out/bin /app/bin
ENV ADDR=:8080 DATA_DIR=/data BIN_DIR=/app/bin
USER lynx
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=4s --retries=3 CMD ["/app/lynx-server", "-healthcheck"]
ENTRYPOINT ["/app/lynx-server"]
