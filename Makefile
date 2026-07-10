BINARY := kibble

.PHONY: build test vet fmt tidy install clean

## build: compile the binary into the repo root
build:
	go build -o $(BINARY) .

## test: run the tests
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format the code
fmt:
	go fmt ./...

## tidy: tidy the module graph
tidy:
	go mod tidy

## install: install the binary into GOBIN
install:
	go install .

## clean: remove build output
clean:
	rm -f $(BINARY)
