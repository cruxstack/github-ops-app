# set mac-only linker flags only for go test (not global)
UNAME_S := $(shell uname -s)
TEST_ENV :=
ifeq ($(UNAME_S),Darwin)
  TEST_ENV = CGO_LDFLAGS=-w
endif

TEST_FLAGS := -race -count=1

.PHONY: build-lambda
build-lambda:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/bootstrap ./cmd/lambda

.PHONY: build-server
build-server:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/server ./cmd/server

.PHONY: build-debug
build-debug:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "-s -w" -o dist/sample ./cmd/sample

.PHONY: debug
debug:
	go run ./cmd/sample

.PHONY: server
server:
	go run ./cmd/server

.PHONY: test
test:
	$(TEST_ENV) go test $(TEST_FLAGS) ./...

.PHONY: test-unit
test-unit:
	$(TEST_ENV) go test $(TEST_FLAGS) ./internal/...

.PHONY: test-verify
test-verify:
	go run ./cmd/verify

.PHONY: test-verify-verbose
test-verify-verbose:
	go run ./cmd/verify -verbose

