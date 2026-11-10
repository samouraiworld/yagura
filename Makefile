GOLANGCI_LINT ?= golangci-lint
PROMTOOL_IMAGE ?= prom/prometheus:v3.5.0

.PHONY: build test lint rules docker secrets

build:
	go build -trimpath -o yagura ./cmd/yagura

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)" && go vet ./... && $(GOLANGCI_LINT) run

rules:
	docker run --rm -v $(CURDIR)/alerts:/alerts --entrypoint sh $(PROMTOOL_IMAGE) -c 'promtool check rules /alerts/*.yml'
	docker run --rm -v $(CURDIR)/alerts:/alerts --entrypoint sh $(PROMTOOL_IMAGE) -c 'promtool test rules /alerts/tests/*.yml'

docker:
	docker build -t yagura:dev .

secrets:
	gitleaks git --no-banner . && gitleaks dir --no-banner .
