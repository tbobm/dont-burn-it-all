BINARY := burn

.PHONY: build vet test clean cross

build:
	go build -o $(BINARY) .

vet:
	go vet ./...

test:
	go test ./...

cross:
	GOOS=darwin  GOARCH=arm64 go build -o dist/$(BINARY)-darwin-arm64 .
	GOOS=darwin  GOARCH=amd64 go build -o dist/$(BINARY)-darwin-amd64 .
	GOOS=linux   GOARCH=amd64 go build -o dist/$(BINARY)-linux-amd64 .
	GOOS=linux   GOARCH=arm64 go build -o dist/$(BINARY)-linux-arm64 .

clean:
	rm -f $(BINARY)
	rm -rf dist
