.PHONY: build run clean

BINARY := hermes-controller

build:
	go build -o $(BINARY) ./cmd/controller

run:
	go run ./cmd/controller

clean:
	rm -f $(BINARY)
