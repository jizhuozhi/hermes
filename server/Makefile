.PHONY: build run clean frontend

BINARY := hermes-server

build:
	cd web && npm install && npm run build
	go build -o $(BINARY) ./cmd/server

build-go:
	go build -o $(BINARY) ./cmd/server

run:
	go run ./cmd/server -config config.yaml

clean:
	rm -f $(BINARY)
	rm -rf web/dist

frontend:
	cd web && npm install && npm run build
