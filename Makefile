# Flyke monorepo tasks. Requires Go >= 1.26, uv, and buf (for `make gen`).
GO_MODULES := api flyke cli
UI_DEPS    := ui/node_modules/.package-lock.json
GOBIN      := $(shell go env GOPATH)/bin

.PHONY: all build ui ui-dev gen fmt test test-go test-py test-ui lint dev demo clean bench bench-smoke bench-stress bench-images

all: build

## ui: build the React dashboard (embedded into ./bin/flyke by `make build`)
$(UI_DEPS): ui/package.json
	cd ui && npm ci

ui: $(UI_DEPS)
	cd ui && npm run build

## ui-dev: hot-reloading dashboard on :5173, proxying the API to FLYKE_SERVER (default 127.0.0.1:8742)
ui-dev: $(UI_DEPS)
	cd ui && npm run dev

## build: compile the `flyke` CLI (with the dashboard) and the standalone control plane into ./bin
build: ui
	cd cli   && go build -o ../bin/flyke ./cmd/flyke
	cd flyke && go build -o ../bin/flyke-server ./cmd/flyke-server

## gen: regenerate protocol code from api/proto
gen:
	cd api && PATH="$(GOBIN):$$PATH" buf lint && PATH="$(GOBIN):$$PATH" buf generate

## dev: install the Python SDK and worker (editable) into ./.venv
dev:
	uv sync --python 3.12
	uv run pre-commit install

## fmt: lint and format all Python code with ruff (the same hooks run on every commit)
fmt:
	uv run pre-commit run --all-files

test: test-go test-py test-ui

test-go:
	@for m in $(GO_MODULES); do (cd $$m && go vet ./... && go test -race -count=1 ./...) || exit 1; done

test-py:
	uv run pytest -q

test-ui: $(UI_DEPS)
	cd ui && npm run typecheck

lint:
	cd api && PATH="$(GOBIN):$$PATH" buf lint && buf breaking --against '.git#subdir=api' || true

## demo: run the example deploy end to end with a local control plane + worker
demo: build dev
	./bin/flyke run examples/deploy.py:DeployService --service=api -f

## bench: production-readiness gate (needs Docker). Every scenario must pass.
##   make bench                       # profile "gate" (default)
##   make bench RUN=TestLiveness      # only matching scenarios
##   make bench FLYKE_BENCH_URL=http://staging:8742 FLYKE_BENCH_ADMIN_TOKEN=…   # external server
BENCH_PROFILE ?= gate
RUN ?= .
bench:
	@cd benchmark && FLYKE_BENCH_PROFILE=$(BENCH_PROFILE) go test ./scenarios -run '$(RUN)' -count=1 -timeout 90m $(BENCH_FLAGS); \
	  status=$$?; sed -n '/^```/q;p' results/latest/summary.md 2>/dev/null; \
	  echo "full report: benchmark/results/latest/summary.md"; exit $$status

## bench-smoke: every scenario at small scale (~5 min), for iterating
bench-smoke:
	$(MAKE) bench BENCH_PROFILE=smoke

## bench-stress: large scale (10k runs, 400k tasks), to find the limits
bench-stress:
	$(MAKE) bench BENCH_PROFILE=stress RUN='TestLoad_'

## bench-images: (re)build the images the benchmarks run (done automatically by `make bench`)
bench-images:
	docker build -f benchmark/docker/server.Dockerfile -t flyke-bench/server:local .
	docker build -f workers/python/Dockerfile -t flyke-bench/worker:local .

clean:
	rm -rf bin flyke-data ui/dist/app
