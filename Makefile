BIN     := bin/polarisx
PKG     := ./...
GO      ?= go

.PHONY: all build test vet fmt parity fixture clean run

all: fmt vet test build

build:
	$(GO) build -trimpath -ldflags "-s -w" -o $(BIN) .

# 测试与时区无关（夹具生成固定 UTC，比对按语义做），所以不强制 TZ
test:
	$(GO) test $(PKG)

vet:
	$(GO) vet $(PKG)

fmt:
	gofmt -w .

# 用 Python 版重新生成 parity 夹具（改了两边任一实现都要跑）
fixture:
	python3 tools/mkfixture.py

# 迁移验收：Go 版输出必须与 Python 版一致
parity: fixture
	$(GO) test ./internal/panel -run "TestParity|TestSubscriptionParity|TestClashParity|TestSingboxParity|TestQuotaParity|TestHTTPParity" -v

run: build
	./$(BIN) doctor

clean:
	rm -rf bin
