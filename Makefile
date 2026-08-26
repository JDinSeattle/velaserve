SHELL := /bin/bash

GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.cache/go-mod
export GOCACHE GOMODCACHE

.PHONY: fmt test test-race vet tools verify-render verify kind-up kind-down

fmt:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.cache/*'))"

test:
	go test ./... -count=1

test-race:
	go test ./... -race -count=1

vet:
	go vet ./...

tools:
	./hack/bootstrap-tools.sh

verify-render: tools
	./hack/verify-render.sh

verify: tools fmt vet test verify-render

kind-up:
	./hack/kind-up.sh

kind-down:
	./hack/kind-down.sh
