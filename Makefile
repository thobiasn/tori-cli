.PHONY: build test vet clean demo-up demo-down demo-record

VERSION ?= dev

build:
	go build -ldflags="-X main.version=$(VERSION)" -o tori ./cmd/tori

test:
	go test -race ./...

vet:
	go vet ./...

clean:
	rm -f tori

demo-up:
	cd demo && ./demo.sh up

demo-down:
	cd demo && ./demo.sh down

demo-record:
	cd demo && ./demo.sh record $(THEME)
