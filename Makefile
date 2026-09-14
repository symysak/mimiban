VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
DIST     = dist

.PHONY: build test vet cross clean run

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o mimiban .

run: build
	./mimiban

test:
	go test ./...

vet:
	go vet ./...

cross: clean
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/mimiban-windows-amd64.exe .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/mimiban-darwin-arm64 .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/mimiban-darwin-amd64 .
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/mimiban-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/mimiban-linux-arm64 .
	@ls -la $(DIST)

clean:
	rm -rf $(DIST) mimiban
