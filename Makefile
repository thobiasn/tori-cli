.PHONY: build test vet clean

VERSION ?= dev

build:
	go build -ldflags="-X main.version=$(VERSION)" -o tori ./cmd/tori

test:
	go test -race ./...

vet:
	go vet ./...

clean:
	rm -f tori
