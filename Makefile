.PHONY: dev build test web-install web-build clean

VERSION ?= v0.1.0

dev:
	go run ./cmd/herdrx-server

web-install:
	pnpm --dir web install

web-build:
	pnpm --dir web build
	rm -rf internal/webassets/dist
	cp -R web/dist internal/webassets/dist

build: web-build
	mkdir -p bin
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/herdrx ./cmd/herdrx
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/herdrx-server ./cmd/herdrx-server
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/herdrx-agent ./cmd/herdrx-agent

test:
	go test ./...
	pnpm --dir web test -- --run
	pnpm --dir web build

clean:
	rm -rf bin web/dist internal/webassets/dist
