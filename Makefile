BINARY=spec-viewer
MODULE=github.com/bzon/spec-viewer

.PHONY: build test install clean

build:
	go build -o $(BINARY) ./cmd/spec-viewer/

test:
	go test ./...

install: build
	go install ./cmd/spec-viewer/

clean:
	rm -f $(BINARY)
	go clean
