.PHONY: build test vet clean

build:
	go build -o rook ./cmd/rook

test:
	go test -race ./...

vet:
	go vet ./...

clean:
	rm -f rook
