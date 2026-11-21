ARG GO_BUILD_VERSION=1.27.3
ARG NODE_VERSION=22.22.0
ARG MISE_VERSION=2026.10.24
ARG DOCKER_VERSION=29.9.2
FROM docker:${DOCKER_VERSION}-cli AS dockercli
FROM ghcr.io/jdx/mise:${MISE_VERSION} AS mise
FROM golang:${GO_BUILD_VERSION}-bookworm AS build
ARG GLAB_VERSION=""
ARG TEA_VERSION=""
ARG GO_VERSION=""
RUN mkdir -p /out/bin \
 && if [ -n "$GLAB_VERSION" ]; then GOBIN=/out/bin CGO_ENABLED=0 go install -ldflags "-X main.version=${GLAB_VERSION}" "gitlab.com/gitlab-org/cli/cmd/glab@v${GLAB_VERSION}"; fi \
 && if [ -n "$TEA_VERSION" ]; then GOBIN=/out/bin CGO_ENABLED=0 go install -ldflags "-X gitea.dev/tea/modules/version.Version=${TEA_VERSION}" "gitea.dev/tea@v${TEA_VERSION}"; fi
RUN mkdir -p /out/go \
 && if [ -n "$GO_VERSION" ]; then \
      toolchain=$(GOTOOLCHAIN="go${GO_VERSION}" go env GOROOT) \
      && cp -a "$toolchain"/. /out/go/; \
    fi
WORKDIR /src
COPY .dockerignore ./
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY scripts ./scripts
COPY deploy ./deploy
RUN CGO_ENABLED=0 go build -tags jara_server -trimpath -o /out/bin/jara ./cmd/jara
RUN go generate ./internal/jara
RUN CGO_ENABLED=0 go build -trimpath -o /out/bin/jara-client ./cmd/jara
COPY --from=mise /usr/local/bin/mise /out/bin/mise
COPY --from=dockercli /usr/local/bin/docker /out/bin/docker
COPY --from=dockercli /usr/local/libexec/docker/cli-plugins/ /out/cli-plugins/

FROM node:${NODE_VERSION}-bookworm-slim
ARG CODEX_VERSION
ARG CLAUDE_CODE_VERSION=""
ARG EDITOR_PACKAGES=""
RUN test -n "$CODEX_VERSION" \
 && apt-get update \
 && apt-get install -y --no-install-recommends openssh-server openssh-client mosh git tmux bubblewrap bash ca-certificates curl tar xz-utils libatomic1 ${EDITOR_PACKAGES} \
 && rm -rf /var/lib/apt/lists/* \
 && npm install --global "@openai/codex@${CODEX_VERSION}" \
 && if [ -n "$CLAUDE_CODE_VERSION" ]; then \
      npm install --global "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
      && mkdir -p /opt/jara \
      && ln -s "$(readlink -f /usr/local/bin/claude)" /opt/jara/claude-cli \
      && rm /usr/local/bin/claude; \
    fi \
 && printf '%s\n' '#!/bin/sh' \
      '# Keep mosh on the UDP ports Compose publishes (60001-60004), and end a' \
      '# server whose client has been silent for a day: an abandoned one keeps' \
      '# its tmux session attached and holds one of the four ports.' \
      ': "${MOSH_SERVER_NETWORK_TMOUT:=86400}"; export MOSH_SERVER_NETWORK_TMOUT' \
      'if [ "$1" = new ] && ! printf "%s\n" "$@" | grep -qx -- "-p"; then shift; exec /usr/bin/mosh-server new -p 60001:60004 "$@"; fi' \
      'exec /usr/bin/mosh-server "$@"' > /usr/local/bin/mosh-server \
 && chmod 755 /usr/local/bin/mosh-server \
 && useradd --uid 1001 --create-home --shell /bin/bash agent \
 && passwd -d agent \
 && mkdir -p /run/sshd /var/lib/jara/ssh /home/agent/.ssh \
 && chown -R agent:agent /home/agent
COPY --from=build /out/ /opt/jara/
ENV LANG=C.UTF-8 \
    LC_CTYPE=C.UTF-8 \
    PATH="/home/agent/.local/share/mise/shims:/usr/local/share/mise/shims:/opt/jara/bin:/opt/jara/go/bin:${PATH}" \
    GOTOOLCHAIN=local \
    GOCACHE=/tmp/jara-cache/go-build \
    GOMODCACHE=/tmp/jara-cache/go-mod \
    NPM_CONFIG_CACHE=/tmp/jara-cache/npm
RUN for binary in /opt/jara/bin/*; do ln -s "$binary" "/usr/local/bin/$(basename "$binary")"; done \
 && if [ -x /opt/jara/go/bin/go ]; then \
      ln -s /opt/jara/go/bin/go /usr/local/bin/go \
      && ln -s /opt/jara/go/bin/gofmt /usr/local/bin/gofmt; \
    fi
COPY deploy/codex-launcher.sh /opt/jara/codex-launcher
COPY deploy/claude-launcher.sh /opt/jara/claude-launcher
COPY deploy/mise.toml /etc/mise/config.toml
COPY deploy/profile.sh /etc/profile.d/jara-tools.sh
RUN rm /usr/local/bin/codex \
 && ln -s /opt/jara/codex-launcher /usr/local/bin/codex \
 && chmod 755 /opt/jara/codex-launcher /opt/jara/claude-launcher \
 && if [ -e /opt/jara/claude-cli ]; then ln -s /opt/jara/claude-launcher /usr/local/bin/claude; fi \
 && mkdir -p /usr/local/share/mise/shims \
 && for tool in node npm npx corepack go gofmt; do ln -s /usr/local/bin/mise "/usr/local/share/mise/shims/$tool"; done
RUN mkdir -p /usr/local/libexec/docker && ln -s /opt/jara/cli-plugins /usr/local/libexec/docker/cli-plugins
COPY deploy/sshd_config /etc/ssh/sshd_config
COPY deploy/entrypoint.sh /usr/local/bin/jara-entrypoint
RUN chmod 755 /usr/local/bin/jara-entrypoint
EXPOSE 22
ENTRYPOINT ["/usr/local/bin/jara-entrypoint"]
