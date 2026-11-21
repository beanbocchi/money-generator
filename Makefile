# Go tooling. `make check` is what must pass before a commit.
.PHONY: build test check
build:
	CGO_ENABLED=0 go build -trimpath -o bin/mg ./cmd/mg
test:
	go test ./...
check:
	gofmt -l . | (! grep .) || (echo "gofmt needed"; exit 1)
	go vet ./...
	go test -race ./...
