VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/oyaguma3/simwifi/internal/cli.version=$(VERSION)
STATICCHECK_VERSION ?= v0.8.1
GOFLAGS_BUILD := -trimpath -ldflags "$(LDFLAGS)"
export CGO_ENABLED := 0

.PHONY: all build build-e2e build-all test lint clean

all: lint test build

build:
	go build $(GOFLAGS_BUILD) -o bin/simwifi .

# E2E 用（Milenage バックエンド入りの simwifi と、hlr_auc_gw 代替の hlrgw）。リリースには使わない
# ミニ PC が arm64 なら GOARCH=arm64 を付ける
build-e2e:
	go build $(GOFLAGS_BUILD) -tags e2e -o bin/simwifi-e2e .
	go build $(GOFLAGS_BUILD) -o bin/hlrgw ./test/e2e/hlrgw

build-all:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS_BUILD) -o dist/simwifi-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS_BUILD) -o dist/simwifi-linux-arm64 .

test:
	CGO_ENABLED=1 go test -race ./...
	go test -tags e2e ./...

lint:
	go vet ./...
	go vet -tags e2e ./...
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

clean:
	rm -rf bin dist
