FROM --platform=$BUILDPLATFORM golang:1.21.13-alpine3.20 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
	go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM alpine:3.20

RUN apk add --no-cache ca-certificates && \
	addgroup -S app && \
	adduser -S -G app -u 10001 app

WORKDIR /app

COPY --from=builder /out/gateway /app/gateway

ENV GIN_MODE=release \
	SKILL_STORAGE_ROOT=/var/lib/skillfun/skills

RUN mkdir -p /var/lib/skillfun/skills && \
	chown -R app:app /app /var/lib/skillfun

USER app

EXPOSE 5080

ENTRYPOINT ["/app/gateway"]
