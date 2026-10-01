VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/oyaguma3/simwifi/internal/cli.version=$(VERSION)
GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_VERSION ?= v2.14.0
GOFLAGS_BUILD := -trimpath -ldflags "$(LDFLAGS)"
export CGO_ENABLED := 0

.PHONY: all build build-e2e dist test lint clean

all: lint test build

build:
	go build $(GOFLAGS_BUILD) -o bin/simwifi .

# E2E 用（Milenage バックエンド入りの simwifi と、hlr_auc_gw 代替の hlrgw）。リリースには使わない
# ミニ PC が arm64 なら GOARCH=arm64 を付ける
build-e2e:
	go build $(GOFLAGS_BUILD) -tags e2e -o bin/simwifi-e2e .
	go build $(GOFLAGS_BUILD) -o bin/hlrgw ./test/e2e/hlrgw

# リリース用のパッケージ: dist/simwifi-$(VERSION)-linux-{amd64,arm64}.tar.gz と SHA256SUMS
# 中身は静的バイナリ、LICENSE、README.md、contrib/。E2E 用のバイナリは含めない。
# 同じコミットからは同じ tar.gz ができるよう、所有者・時刻（コミット時刻）・格納順を固定する
DIST_ARCHES := amd64 arm64
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || date +%s)

dist:
	rm -rf dist
	set -e; for arch in $(DIST_ARCHES); do \
		name=simwifi-$(VERSION)-linux-$$arch; \
		mkdir -p dist/$$name; \
		GOOS=linux GOARCH=$$arch go build $(GOFLAGS_BUILD) -o dist/$$name/simwifi .; \
		cp LICENSE README.md dist/$$name/; \
		cp -r contrib dist/$$name/; \
		tar -C dist --sort=name --owner=0 --group=0 --numeric-owner --mode=u+rwX,go+rX,go-w --mtime=@$(SOURCE_DATE_EPOCH) \
			-cf - $$name | gzip -n > dist/$$name.tar.gz; \
		rm -rf dist/$$name; \
	done
	cd dist && sha256sum *.tar.gz > SHA256SUMS

test:
	CGO_ENABLED=1 go test -race ./...
	go test -tags e2e ./...

# golangci-lint（staticcheck を含む。設定は .golangci.yml）。
# Go 1.27 のコードを解析するには、Go 1.27 以降でビルドされた v2.13.0 以降が必要:
#   go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
lint:
	go vet ./...
	go vet -tags e2e ./...
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) run --build-tags e2e ./...

clean:
	rm -rf bin dist
