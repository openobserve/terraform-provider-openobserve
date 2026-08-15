default: build

.PHONY: build
build:
	go build -o bin/terraform-provider-openobserve .

# Version the locally installed build is published under. Override to test a
# specific constraint: make install VERSION=1.1.0
VERSION ?= 1.0.0

.PHONY: install
install: build
	mkdir -p ~/.terraform.d/plugins/registry.terraform.io/openobserve/openobserve/$(VERSION)/$$(go env GOOS)_$$(go env GOARCH)
	cp bin/terraform-provider-openobserve ~/.terraform.d/plugins/registry.terraform.io/openobserve/openobserve/$(VERSION)/$$(go env GOOS)_$$(go env GOARCH)/

.PHONY: test
test:
	go test ./... -v -timeout 120s

.PHONY: testacc
testacc:
	TF_ACC=1 go test ./... -v -timeout 120s

# Integration tests run against a live OpenObserve instance and skip without one.
# See the README for how to start a throwaway server in Docker.
.PHONY: testintegration
testintegration:
	go test ./internal/provider/ -run TestIntegration -v -timeout 300s

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: fmt
fmt:
	gofmt -s -w .
	goimports -w .

.PHONY: docs
docs:
	tfplugindocs generate --provider-name openobserve

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: clean
clean:
	rm -rf bin/ dist/
