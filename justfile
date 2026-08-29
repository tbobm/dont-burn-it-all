binary := "burn"
# darwin/amd64 darwin/arm64 linux/amd64 linux/arm64
platforms := "darwin/amd64 darwin/arm64 linux/amd64 linux/arm64"

# list recipes
default:
    @just --list

# build the local binary
build:
    go build -o {{binary}} .

vet:
    go vet ./...

test:
    go test ./...

# build per-platform tar.gz archives + checksums into dist/ (consumed by ubi)
dist:
    #!/usr/bin/env bash
    set -euo pipefail
    rm -rf dist && mkdir -p dist
    for p in {{platforms}}; do
      os="${p%/*}"; arch="${p#*/}"
      out="dist/{{binary}}_${os}_${arch}"
      GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -o "$out/{{binary}}" .
      tar -C "$out" -czf "dist/{{binary}}_${os}_${arch}.tar.gz" {{binary}}
      rm -rf "$out"
    done
    ( cd dist && shasum -a 256 *.tar.gz > checksums.txt )
    ls -1 dist

clean:
    rm -f {{binary}}
    rm -rf dist

# build the optional --sandbox image (requires docker; not needed for `just build`)
build-sandbox-image:
    docker build -f Dockerfile.sandbox -t burn-sandbox:latest .
