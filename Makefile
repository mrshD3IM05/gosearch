BINARY_NAME := gosearch
BIN_DIR     := bin

.PHONY: build run web test test-race vet fmt fmt-check check clean

build:
	go build -o $(BIN_DIR)/$(BINARY_NAME) ./cmd/gosearch

run:
	go run ./cmd/gosearch

web:
	go run ./cmd/webtest -addr :5000

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./pkg

fmt-check:
	@out=$$(gofmt -l ./cmd ./pkg); \
	if [ -n "$$out" ]; then \
		echo "The following files are not gofmt-formatted:"; \
		echo "$$out"; \
		exit 1; \
	fi

check: fmt-check vet test-race

clean:
	rm -rf $(BIN_DIR)