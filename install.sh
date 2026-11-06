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
    sh -c 'mkdir -p "$HOME" && JARA_INSTALL_BIN_DIR=/out ./install.sh'
}

build_and_install() {
  install_tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/jara-install.XXXXXX")
  trap 'rm -rf "$install_tmp_dir"' EXIT
  JARA_DEPLOY_BUNDLE_OUTPUT="$install_tmp_dir/deploy_bundle.tar.gz" \
    JARA_DEPLOY_OVERLAY_OUTPUT="$install_tmp_dir/overlay.json" \
    go generate ./internal/jara
  GOBIN="$install_bin_dir" go install -overlay="$install_tmp_dir/overlay.json" ./cmd/jara
}

install_bin_dir=${JARA_INSTALL_BIN_DIR:-"$HOME/.local/bin"}
mkdir -p "$install_bin_dir"
if command -v go >/dev/null 2>&1; then
  build_and_install
elif command -v docker >/dev/null 2>&1; then
  echo 'Go is not installed; building Jara in Docker with Go 1.27.3.' >&2
  install_with_docker
else
  echo 'Go 1.27.3+ is required. Install Go or Docker, then rerun ./install.sh.' >&2
  exit 1
fi
