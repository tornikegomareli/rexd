VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test e2e e2e-race lint dist clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/rexd ./cmd/rexd

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/rexd

test:
	go test -race ./...

# Runs against an isolated Rex server and needs Rex to be installed.
e2e:
	go test -race -tags e2e -count=1 ./e2e/...

# Slower, because a race-enabled rexd takes about a second to start.
e2e-race:
	REXD_E2E_RACE=1 go test -race -tags e2e -count=1 -timeout 15m ./e2e/...

lint:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...

# Archives for a release, one for each system, and their checksums.
dist:
	rm -rf dist && mkdir dist
	for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; dir=dist/rexd_$${os}_$${arch}; \
		mkdir $$dir && cp LICENSE README.md $$dir/ && \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$dir/rexd ./cmd/rexd && \
		tar -czf $$dir.tar.gz -C $$dir rexd LICENSE README.md && rm -r $$dir || exit 1; \
	done
	cd dist && shasum -a 256 *.tar.gz > checksums.txt

clean:
	rm -rf bin dist
