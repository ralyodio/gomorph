.PHONY: build build-native test test-native vet vet-native check check-native

build:
	go build -o bin/gomorph ./cmd/gomorph

build-native:
	go build -tags libav -o bin/gomorph-native ./cmd/gomorph

test:
	go test ./...

test-native:
	go test -tags libav ./...

vet:
	go vet ./...

vet-native:
	go vet -tags libav ./...

check: test vet

check-native: test-native vet-native
