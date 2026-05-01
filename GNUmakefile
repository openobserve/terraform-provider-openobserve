default: build

.PHONY: build
build:
	go build -o bin/terraform-provider-openobserve .

.PHONY: install
install: build
	mkdir -p ~/.terraform.d/plugins/registry.terraform.io/openobserve/openobserve/0.0.1/$$(go env GOOS)_$$(go env GOARCH)
	cp bin/terraform-provider-openobserve ~/.terraform.d/plugins/registry.terraform.io/openobserve/openobserve/0.0.1/$$(go env GOOS)_$$(go env GOARCH)/

.PHONY: test
test:
	go test ./... -v -timeout 120s

.PHONY: testacc
testacc:
	TF_ACC=1 go test ./... -v -timeout 120s

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: fmt
fmt:
	gofmt -s -w .
	goimports -w .

.PHONY: docs
docs:
	tfplugindocs generate

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: clean
clean:
	rm -rf bin/ dist/
