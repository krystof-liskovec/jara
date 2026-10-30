#!/bin/sh
set -eu

GOBIN="$HOME/.local/bin" go install ./cmd/jara
