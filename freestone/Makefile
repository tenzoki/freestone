.PHONY: build test lint run install auth-google clean

BINARY := freestone-mcp
CMD    := ./cmd/freestone-mcp

build:
	go build -o $(BINARY) $(CMD)

test:
	go test ./... -count=1

lint:
	golangci-lint run ./...

run: build
	./$(BINARY) serve

install: build
	mkdir -p $(HOME)/.local/bin
	cp $(BINARY) $(HOME)/.local/bin/$(BINARY)

auth-google: build
	./$(BINARY) auth google

clean:
	rm -f $(BINARY)
	rm -f coverage.out
