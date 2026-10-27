.PHONY: build test fmt vet check clean

build:
	go build -o pear .

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

check: fmt vet test

clean:
	rm -f pear
