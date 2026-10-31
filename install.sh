#!/bin/sh
set -eu

go generate ./internal/jara
GOBIN="$HOME/.local/bin" go install ./cmd/jara
