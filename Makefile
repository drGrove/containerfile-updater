PLATFORM ?= linux/amd64,linux/arm64,linux/arm
PROGRESS ?= auto
SOURCE_DATE_EPOCH := $(shell git log -1 --format=%ct)
COMMIT_ISO := $(shell git log -1 --format=%cI)
REGISTRY ?= drgrove
NAME = containerfile-updater
BINARY_NAME = containerfile-updater
VERSION := latest
SHELL := /bin/bash
OUT_DIR=out
BIN_DIR=$(OUT_DIR)/bins
IMAGE_DIR=$(OUT_DIR)/image
MAIN_PATH=./main.go
GO_SRCS=$(shell find . -type f -name "*.go" -not -path "*/\.*")
MOD_SRCS=$(shell find . -type f -name "go.mod" -o -name "go.sum" -not -path "*/\.*")
SRCS=$(GO_SRCS) $(MOD_SRCS)
LICENSE_CODE=AGPL-3.0
SOURCE_URL=https://github.com/drGrove/containerfile-updater

export SOURCE_DATE_EPOCH
export TZ=UTC
export LANG=C.UTF-8
export LC_ALL=C
export BUILDKIT_MULTI_PLATFORM=1
export DOCKER_BUILDKIT=1

all: \
	$(BIN_DIR) \
	image \
	build-linux-amd64 \
	build-linux-arm64 \
	build-linux-arm \
	build-mac-amd64 \
	build-mac-arm64

$(OUT_DIR):
	mkdir -p $(OUT_DIR)

$(IMAGE_DIR): out
	mkdir -p $(IMAGE_DIR)

$(BIN_DIR): $(OUT_DIR)
	mkdir -p $(BIN_DIR)

$(BIN_DIR)/%: $(SRCS) $(OUT_DIR)
	go build \
		$(LDFLAGS) \
		-o $(BIN_DIR)/$* \
		$(MAIN_PATH)

.PHONY: test
test: $(SRCS)
	go test ./...

# Build for specific platforms
build-linux-amd64: $(SRCS) | $(BIN_DIR)
	CGO_ENABLED=0 \
	GOOS=linux \
	GOARCH=amd64 \
	$(MAKE) $(BIN_DIR)/$(BINARY_NAME)_linux_amd64

build-linux-arm64: $(SRCS) | $(BIN_DIR)
	CGO_ENABLED=0 \
	GOOS=linux \
	GOARCH=arm64 \
	$(MAKE) $(BIN_DIR)/$(BINARY_NAME)_linux_arm64

build-linux-arm: $(SRCS) | $(BIN_DIR)
	CGO_ENABLED=0 \
	GOOS=linux \
	GOARCH=arm \
	GOARM=7 \
	$(MAKE) $(BIN_DIR)/$(BINARY_NAME)_linux_armv7

build-mac-amd64: $(SRCS) | $(BIN_DIR)
	CGO_ENABLED=0 \
	GOOS=darwin \
	GOARCH=amd64 \
	$(MAKE) $(BIN_DIR)/$(BINARY_NAME)_darwin_amd64 $(MAIN_PATH)

build-mac-arm64: $(SRCS) | $(BIN_DIR)
	CGO_ENABLED=0 \
	GOOS=darwin \
	GOARCH=arm64 \
	$(MAKE) $(BIN_DIR)/$(BINARY_NAME)_darwin_arm64

.PHONY: image
image: $(OUT_DIR)/image/index.json
$(OUT_DIR)/image/index.json: $(OUT_DIR)/image Containerfile $(SRCS)
	docker \
		buildx \
		build \
		--ulimit nofile=2048:16384 \
		--tag $(REGISTRY)/$(NAME):$(VERSION) \
		--output \
			name=$(NAME),type=oci,rewrite-timestamp=true,force-compression=true,annotation.org.opencontainers.licenses=$(LICENSE_CODE),annotation.org.opencontainers.image.revision=$(shell git rev-list HEAD -1 .),annotation.org.opencontainers.source=$(SOURCE_URL),annotation.org.opencontainers.image.created=$(COMMIT_ISO),tar=true,dest=- \
		$(EXTRA_ARGS) \
		$(NOCACHE_FLAG) \
		$(CHECK_FLAG) \
		--platform=$(PLATFORM) \
		--progress=$(PROGRESS) \
		--sbom=true \
		--provenance=true \
		-f Containerfile \
		. \
		| tar -C $(IMAGE_DIR) -mx

.PHONY: load-image
load-image: image
	cd out/image
	tar -cf - . | docker load

.PHONY: image-digests
.ONESHELL:
image-digests: $(IMAGE_DIR)/index.json
	@cd $(IMAGE_DIR) && \
	INDEX_DIGEST=$$(jq -r '.manifests[0].digest' index.json) && \
	MANIFEST_FILE=$$(echo "$$INDEX_DIGEST" | sed 's/sha256://' | xargs -I {} find blobs/sha256 -name "{}" -type f) && \
	if [ -n "$$MANIFEST_FILE" ]; then \
		jq -r '.manifests[] | select(.annotations."vnd.docker.reference.type" != "attestation-manifest") | "\(.digest | sub("sha256:"; "")) \(.platform.os)/\(.platform.architecture)"' "$$MANIFEST_FILE" | sort; \
	else \
		echo "Error: Could not find manifest file for $$INDEX_DIGEST"; \
	fi

.PHONY: verify
verify: all
	@make all OUT_DIR=out2
	@cmp $(IMAGE_DIR)/index.json out2/image/index.json
	@cmp $(BIN_DIR)/$(BINARY_NAME)_darwin_arm64 out2/bins/$(BINARY_NAME)_darwin_arm64
	@cmp $(BIN_DIR)/$(BINARY_NAME)_darwin_amd64 out2/bins/$(BINARY_NAME)_darwin_amd64
	@cmp $(BIN_DIR)/$(BINARY_NAME)_linux_armv7 out2/bins/$(BINARY_NAME)_linux_armv7
	@cmp $(BIN_DIR)/$(BINARY_NAME)_linux_arm64 out2/bins/$(BINARY_NAME)_linux_arm64
	@cmp $(BIN_DIR)/$(BINARY_NAME)_linux_amd64 out2/bins/$(BINARY_NAME)_linux_amd64
	@echo "Digests match!"
