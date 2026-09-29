# syntax=docker/dockerfile:1
# Relay image. Cross-compiles on the build machine, so building for the Pi is fast:
#   docker buildx build --platform linux/arm64 -t ddremote-relay .
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} CGO_ENABLED=0 \
	go build -trimpath -ldflags="-s -w" -o /relay ./cmd/relay

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /relay /relay
# Inside the container the relay must listen on all interfaces so cloudflared
# (another container) can reach it. compose.yaml publishes it on the Pi's 127.0.0.1 only.
ENV RELAY_LISTEN=:8080
EXPOSE 8080
ENTRYPOINT ["/relay"]
CMD ["serve"]
