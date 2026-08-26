SHELL := /bin/bash

GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.cache/go-mod
export GOCACHE GOMODCACHE

.PHONY: fmt test test-race vet verify

fmt:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.cache/*'))"

test:
	go test ./... -count=1

test-race:
	go test ./... -race -count=1

vet:
	go vet ./...

verify: fmt vet test

