.PHONY: build test vet
build:
	CGO_ENABLED=1 go build -tags sqlite_omit_load_extension -buildvcs=false -trimpath -ldflags="-s -w" -o bin/tiimo-mcp ./cmd/tiimo-mcp
test:
	go test -race ./...
vet:
	go vet ./...
