# Build the relay image and deploy the Compose stack to the Raspberry Pi.
#   make setup  PI=pi@raspberrypi.local   create /opt/dd-remote on the Pi (once)
#   make load   PI=pi@raspberrypi.local   build the image and copy it to the Pi
#   make deploy PI=pi@raspberrypi.local   load, then (re)start the stack
#   make deploy PLATFORM=linux/arm/v7     32-bit Pi OS
#   make logs   PI=pi@raspberrypi.local   follow the relay and tunnel logs
PI       ?= pi@raspberrypi.local
PLATFORM ?= linux/arm64
DIR      ?= /opt/dd-remote
IMAGE    := ddremote-relay:latest

.PHONY: test image load setup deploy logs

test:
	cd ../relay && go vet ./... && go test ./...

image:
	docker buildx build --platform $(PLATFORM) -t $(IMAGE) --load ../relay

setup:
	ssh $(PI) 'sudo mkdir -p $(DIR)/cloudflared && sudo chown -R $$(id -un): $(DIR)'
	scp compose.yaml $(PI):$(DIR)/compose.yaml

load: image
	docker save $(IMAGE) | gzip | ssh $(PI) 'gunzip | docker load'

deploy: load
	scp compose.yaml $(PI):$(DIR)/compose.yaml
	ssh $(PI) 'cd $(DIR) && docker compose up -d --remove-orphans && docker image prune -f >/dev/null && docker compose ps'

logs:
	ssh -t $(PI) 'cd $(DIR) && docker compose logs -f --tail=100'
