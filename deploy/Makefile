# Build and deploy the relay to the Raspberry Pi.
#   make build              cross-compile for 64-bit Pi OS (arm64)
#   make build GOARCH=arm   32-bit Pi OS
#   make deploy PI=pi@raspberrypi.local
PI     ?= pi@raspberrypi.local
GOARCH ?= arm64
GOARM  ?= 7
BIN    := ../relay/relay-linux-$(GOARCH)

.PHONY: build deploy install-service test

test:
	cd ../relay && go vet ./... && go test ./...

build:
	cd ../relay && GOOS=linux GOARCH=$(GOARCH) GOARM=$(GOARM) CGO_ENABLED=0 \
		go build -trimpath -ldflags="-s -w" -o relay-linux-$(GOARCH) ./cmd/relay

deploy: build
	scp $(BIN) $(PI):/tmp/relay
	ssh $(PI) 'sudo install -m 755 /tmp/relay /usr/local/bin/relay && sudo systemctl restart relay && systemctl --no-pager status relay | head -5'

install-service:
	scp relay.service $(PI):/tmp/relay.service
	ssh $(PI) 'id relay >/dev/null 2>&1 || sudo useradd --system --no-create-home --shell /usr/sbin/nologin relay; \
		sudo install -m 644 /tmp/relay.service /etc/systemd/system/relay.service && \
		sudo mkdir -p /etc/relay && sudo systemctl daemon-reload && sudo systemctl enable relay'
