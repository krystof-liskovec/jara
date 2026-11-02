#!/bin/sh
set -eu

install_with_docker() {
  docker run --rm \
    --user "$(id -u):$(id -g)" \
    --volume "$PWD:/src" \
    --volume "$HOME/.local/bin:/out" \
    --workdir /src \
    --env HOME=/tmp/jara-home \
    --env GOPATH=/tmp/jara-gopath \
    --env GOCACHE=/tmp/jara-go-cache \
    --env GOMODCACHE=/tmp/jara-go-mod \
    golang:1.27.3-bookworm \
    sh -c 'mkdir -p "$HOME" && go generate ./internal/jara && GOBIN=/out go install ./cmd/jara'
}

mkdir -p "$HOME/.local/bin"
if command -v go >/dev/null 2>&1; then
  go generate ./internal/jara
  GOBIN="$HOME/.local/bin" go install ./cmd/jara
elif command -v docker >/dev/null 2>&1; then
  echo 'Go is not installed; building Jara in Docker with Go 1.27.3.' >&2
  install_with_docker
else
  echo 'Go 1.27.3+ is required. Install Go or Docker, then rerun ./install.sh.' >&2
  exit 1
fi
