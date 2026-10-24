BIN     := bin/polarisx
PKG     := ./...
GO      ?= go

.PHONY: all build test vet fmt clean run dist

# 发布用：交叉编译多平台 + 校验和（CI 在打 tag 时调用，也可本地跑）
VERSION ?= dev
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
LDFLAGS := -s -w -X github.com/PPX-LuBing/polarisx/internal/panel.Version=$(VERSION)

dist:
	rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=""; \
	  [ "$$os" = windows ] && ext=".exe"; \
	  printf "  %-16s -> dist/polarisx-%s-%s%s\n" "$$p" "$$os" "$$arch" "$$ext"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath \
	    -ldflags "$(LDFLAGS)" -o dist/polarisx-$$os-$$arch$$ext . || exit 1; \
	done
	cd dist && sha256sum * > SHA256SUMS && cat SHA256SUMS

all: fmt vet test build

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

# 测试与时区无关（夹具生成固定 UTC，比对按语义做），所以不强制 TZ
test:
	$(GO) test $(PKG)

vet:
	$(GO) vet $(PKG)

fmt:
	gofmt -w $(shell git ls-files '*.go')

run: build
	./$(BIN) doctor

clean:
	rm -rf bin
