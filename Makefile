# Deed developer tasks. `make check` is what CI runs (except the browser e2e and container jobs).
SHELL := bash
GO ?= go
PNPM ?= pnpm
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X github.com/aurimasniekis/go-deed/internal/buildinfo.Version=$(VERSION) \
	-X github.com/aurimasniekis/go-deed/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/aurimasniekis/go-deed/internal/buildinfo.Date=$(DATE)
GOLANGCI_VERSION ?= v2.15.0
DEED_API ?= http://127.0.0.1:8787
FUZZTIME ?= 10s
HAS_WEB := $(wildcard web/package.json)

.PHONY: all build run fmt fmt-check vet lint test race fuzz-smoke openapi openapi-check vuln secrets monitoring-check \
	web web-install web-build web-gen web-check web-test web-lint web-dev web-e2e check docker container-smoke e2e bench release-build clean

all: build

build: ## build bin/deed and bin/deed-agent (embeds internal/webui/dist as built)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/deed ./cmd/deed
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/deed-agent ./cmd/deed-agent

run: build ## run against deploy/dev/deed.yaml
	bin/deed serve -config deploy/dev/deed.yaml

fmt:
	gofmt -w cmd internal pkg

fmt-check:
	@out=$$(gofmt -l cmd internal pkg); if [[ -n "$$out" ]]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

lint:
	golangci-lint run --timeout 10m ./...

test:
	$(GO) test ./...

race: ## race tests (DEED_TEST_TX_MAX relaxes the 100 ms transaction guard on loaded runners)
	$(GO) test -race -count=1 ./...

fuzz-smoke:
	$(GO) test -run '^$$' -fuzz FuzzSanitize -fuzztime $(FUZZTIME) ./internal/icons/
	$(GO) test -run '^$$' -fuzz FuzzParse -fuzztime $(FUZZTIME) ./internal/tfimport/

openapi: ## regenerate api/openapi.json
	$(GO) run ./cmd/deed openapi > api/openapi.json.tmp && mv api/openapi.json.tmp api/openapi.json

openapi-check: ## fail when api/openapi.json is stale
	@$(GO) run ./cmd/deed openapi > /tmp/deed-openapi.json && cmp -s /tmp/deed-openapi.json api/openapi.json || \
		{ echo "api/openapi.json is stale: run make openapi"; exit 1; }

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

secrets:
	GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=color.ui GIT_CONFIG_VALUE_0=never GIT_CONFIG_KEY_1=color.diff GIT_CONFIG_VALUE_1=never \
		gitleaks git --redact --config .gitleaks.toml .

monitoring-check:
	deploy/monitoring/check.sh

web-install:
	$(PNPM) --dir web install --frozen-lockfile

web-gen: ## regenerate the typed API client from api/openapi.json
	$(PNPM) --dir web run gen:api

web-build: ## typecheck, vite build into internal/webui/dist, scripts/check-dist.mjs
	$(PNPM) --dir web run build

web-check:
ifneq ($(HAS_WEB),)
	$(PNPM) --dir web run typecheck
	$(PNPM) --dir web run lint
	$(PNPM) --dir web run test
else
	@echo "web/ not present; skipping"
endif

web: web-install web-build ## install dependencies and build the UI into internal/webui/dist

web-test: web-check ## typecheck, lint and unit tests

web-lint: ## ESLint (type-checked, react-hooks, jsx-a11y) and Prettier
	$(PNPM) --dir web run lint

web-dev: ## Vite dev server proxying /api and /auth to a running deed (DEED_API)
	DEED_API=$(DEED_API) $(PNPM) --dir web run dev

web-e2e: ## Playwright + axe against a fresh, seeded deed (builds bin/deed unless DEED_E2E_BINARY is set; run web-build first)
	$(PNPM) --dir web run e2e

check: fmt-check vet lint race openapi-check monitoring-check web-check ## everything CI checks without browsers or Docker

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE) -t deed:local .

container-smoke: docker
	scripts/container-smoke.sh deed:local

e2e: ## §18 end-to-end scenario against the dev stack (docs/VALIDATION.md)
	scripts/e2e.sh

bench: ## 10k entities / 50k relations latency report (docs/PERFORMANCE.md)
	DEED_PERF=1 DEED_TEST_TX_MAX=10m $(GO) test -run TestPerformance -v -count=1 -timeout 30m ./internal/perf/

release-build:
	scripts/release-build.sh -o dist

clean:
	rm -rf bin dist
