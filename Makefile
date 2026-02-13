.PHONY: build test vet clean

build:
	go build -o tori ./cmd/tori

test:
	go test -race ./...

vet:
	go vet ./...

clean:
	rm -f tori
