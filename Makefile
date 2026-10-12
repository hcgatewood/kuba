.PHONY: build check clean cover demo demo_open e2e e2e-go e2e-python fmt gen help lint py-build py-install py-publish py-uninstall test tidy tools venv

K8S_VERSION := 1.37.0
ENVTEST := .cache/envtest/k8s/$(K8S_VERSION)-$(shell go env GOOS)-$(shell go env GOARCH)

TOOLS := .cache/tools/$(shell go env GOVERSION)
GOLANGCI_LINT_VERSION := v2.14.0
GOIMPORTS_REVISER_VERSION := v3.13.2
GOLANGCI_LINT := $(TOOLS)/golangci-lint-$(GOLANGCI_LINT_VERSION)
GOIMPORTS_REVISER := $(TOOLS)/goimports-reviser-$(GOIMPORTS_REVISER_VERSION)

# General

help: ## Print this help text
	@grep -Eh '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-14s\033[0m %s\n", $$1, $$2}'

demo: ## Create the demo
	vhs assets/demo.tape --output assets/demo.gif

demo_open: demo ## Create the demo then open it
	open assets/demo.gif

# Main

# Some Macs (seen with endpoint security software installed) launch big binaries from Go's own linker one at a time, at
# ~60ms each; the system linker's launch in parallel, at ~13ms. The e2e harness builds the Go kuba with these too.
BUILD_LDFLAGS := $(if $(filter darwin,$(shell go env GOOS)),-linkmode=external)

build: ## Build CLI binary
	go build -ldflags='$(BUILD_LDFLAGS)' -o bin/kuba ./cmd/kuba

test: ## Run unit tests
	go test ./...

gen: ## Generate code, e.g. kubectl's flag tables
	go generate ./...

clean: ## Remove build output (keeps .cache and .venv)
	rm -rf bin

# E2E tests

e2e: e2e-python e2e-go ## Run all e2e tests (optional: RUN=get/fuzzy UPDATE=1 V=1)

e2e-python: venv ## Run Python e2e tests (optional: RUN=get/fuzzy UPDATE=1 V=1)
e2e-go: ## Run Go e2e tests (optional: RUN=get/fuzzy UPDATE=1 V=1)

# Flags:
# - RUN=<path> selects scripts under e2e/testdata/script (sans extension); any prefix works (get, get/fuzzy, etc)
# - UPDATE=1 rewrites goldens
# - V=1 is verbose
e2e-python e2e-go: e2e-%: $(ENVTEST)
	KUBA_IMPL=$* KUBEBUILDER_ASSETS=$(CURDIR)/$(ENVTEST) E2E_LDFLAGS='$(BUILD_LDFLAGS)' go test ./e2e -count=1 -vet=off -run 'TestScript$(if $(RUN),/$(RUN))' $(if $(UPDATE),-update) $(if $(V),-v)

COVER := .cache/cover

# Failing scripts don't stop the report, since their runs still count toward coverage.
cover: $(ENVTEST) ## Measure Go coverage of unit and Go e2e tests together (optional: RUN=get/fuzzy)
	rm -rf $(COVER) && mkdir -p $(COVER)/unit $(COVER)/e2e
	go test -cover ./cmd/... ./internal/... -args -test.gocoverdir=$(CURDIR)/$(COVER)/unit
	-E2E_GOCOVERDIR=$(CURDIR)/$(COVER)/e2e KUBA_IMPL=go KUBEBUILDER_ASSETS=$(CURDIR)/$(ENVTEST) E2E_LDFLAGS='$(BUILD_LDFLAGS)' go test ./e2e -count=1 -vet=off -run 'TestScript$(if $(RUN),/$(RUN))'
	go tool covdata percent -i=$(COVER)/unit,$(COVER)/e2e
	go tool covdata textfmt -i=$(COVER)/unit,$(COVER)/e2e -o $(COVER)/cover.out
	go tool cover -html=$(COVER)/cover.out -o $(COVER)/cover.html
	@echo "Report: $(COVER)/cover.html"

venv: .venv/.stamp ## Create Python virtual environment and install dependencies

.venv/.stamp: pyproject.toml e2e/python-requirements.txt
	uv venv .venv --python 3.12 --allow-existing
	uv pip install --python .venv/bin/python -e . -r e2e/python-requirements.txt
	touch $@

$(ENVTEST):
	go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.2 use $(K8S_VERSION) --bin-dir .cache/envtest -p path

# Dev helpers

check: lint test ## Pass if lint and tests pass

fmt: $(GOIMPORTS_REVISER) $(GOLANGCI_LINT) ## Format all Go files
	$(GOIMPORTS_REVISER) -apply-to-generated-files ./...
	$(GOLANGCI_LINT) fmt

lint: $(GOIMPORTS_REVISER) $(GOLANGCI_LINT) ## Lint all Go files
	$(GOLANGCI_LINT) run
	@files=$$($(GOIMPORTS_REVISER) -list-diff -apply-to-generated-files ./...) || exit 1; \
		if [ -n "$$files" ]; then echo "Imports out of order (run make fmt):"; echo "$$files"; exit 1; fi

tools: $(GOIMPORTS_REVISER) $(GOLANGCI_LINT) ## Install dev tools

$(GOLANGCI_LINT):
	GOBIN=$(CURDIR)/$(TOOLS) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(TOOLS)/golangci-lint $@

$(GOIMPORTS_REVISER):
	GOBIN=$(CURDIR)/$(TOOLS) go install github.com/incu6us/goimports-reviser/v3@$(GOIMPORTS_REVISER_VERSION)
	mv $(TOOLS)/goimports-reviser $@

tidy: ## Run go mod tidy
	go mod tidy

# Legacy (Python)

py-build: ## Build the Python package
	rm -rf dist
	poetry build

py-install: py-build ## Dev install the Python package locally
	pip install --force-reinstall dist/*.whl

py-uninstall: ## Dev uninstall the Python package locally
	pip uninstall -y $(shell poetry version | cut -d ' ' -f 1)

py-publish: ## Publish the Python package to PyPI
	rm -rf dist
	poetry publish --build
