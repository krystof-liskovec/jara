# Login shells use the same cwd-aware tool shims as noninteractive SSH and editors.
case ":$PATH:" in
  *:/usr/local/share/mise/shims:*) ;;
  *) PATH="/home/agent/.local/share/mise/shims:/usr/local/share/mise/shims:$PATH" ;;
esac
export PATH
GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
export GOTOOLCHAIN
GOCACHE="${GOCACHE:-/tmp/jara-cache/go-build}"
GOMODCACHE="${GOMODCACHE:-/tmp/jara-cache/go-mod}"
NPM_CONFIG_CACHE="${NPM_CONFIG_CACHE:-/tmp/jara-cache/npm}"
export GOCACHE GOMODCACHE NPM_CONFIG_CACHE
[ ! -r /run/jara/docker.env ] || . /run/jara/docker.env
JARA_DOCKER="${JARA_DOCKER:-0}"
export JARA_DOCKER
