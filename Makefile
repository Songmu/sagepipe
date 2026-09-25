CURRENT_REVISION = $(shell git rev-parse --short HEAD)
BUILD_LDFLAGS = "-s -w -X github.com/Songmu/sagepipe.revision=$(CURRENT_REVISION)"
u := $(if $(update),-u)

.PHONY: deps
deps:
	go get ${u}
	go mod tidy

.PHONY: devel-deps
devel-deps:
	go install github.com/Songmu/gocredits/cmd/gocredits@v0.5.0

.PHONY: test
test:
	go test ./...

.PHONY: build
build:
	go build -ldflags=$(BUILD_LDFLAGS) ./cmd/sagepipe

.PHONY: install
install:
	go install -ldflags=$(BUILD_LDFLAGS) ./cmd/sagepipe

.PHONY: prepare-release
prepare-release: devel-deps
	go mod tidy
	gocredits -w
	git update-index --add --remove -- go.mod go.sum CREDITS

CREDITS: go.sum deps devel-deps
	gocredits -w
