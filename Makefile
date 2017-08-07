all: build test

build:
	@mkdir -p bin
	GOBIN="$(CURDIR)/bin" go install ./cmd/gochk

test:
	go test $(shell go list ./... | grep -vF /vendor/)

clean:
	rm -rfv bin

.PHONY: all build test clean
