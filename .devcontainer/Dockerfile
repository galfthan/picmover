# Dev container for picmover: Go, Node, the Go editor tools, tmux and
# emacs, plus small tmux, emacs, readline and bash configs. The build
# context is this directory only.
#
#   make dev-image     (or: docker build -t picmover-dev .devcontainer)
#
# The in-container user is "dev"; build it with your own uid/gid
# (make dev-image does) so files written to the mounted repo are yours.
FROM ubuntu:24.04

ARG GO_VERSION=1.27.2
ARG NODE_VERSION=22.23.3
ARG USERNAME=dev
ARG USER_UID=1000
ARG USER_GID=1000
# 1 installs Claude Code for the dev user; build with 0 to leave it out.
ARG INSTALL_CLAUDE=1
# Set by BuildKit: amd64 or arm64.
ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        emacs-nox \
        git \
        htop \
        less \
        make \
        sudo \
        tmux \
    && rm -rf /var/lib/apt/lists/*

# Go and Node from their release tarballs (.tar.gz, so no xz needed).
RUN set -eux; \
    arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "$arch" in \
        amd64) nodearch=x64 ;; \
        arm64) nodearch=arm64 ;; \
        *) echo "unsupported architecture: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" \
        | tar -xz -C /usr/local; \
    mkdir -p /usr/local/node; \
    curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${nodearch}.tar.gz" \
        | tar -xz -C /usr/local/node --strip-components=1

# ubuntu:24.04 ships a user "ubuntu" with uid/gid 1000; remove it so the
# dev user can take that (or any other) uid. An existing group with the
# wanted gid is reused.
RUN set -eux; \
    userdel -r ubuntu 2>/dev/null || true; \
    groupdel ubuntu 2>/dev/null || true; \
    if ! getent group "${USER_GID}" >/dev/null; then groupadd -g "${USER_GID}" "${USERNAME}"; fi; \
    useradd -u "${USER_UID}" -g "${USER_GID}" -m -s /bin/bash "${USERNAME}"; \
    echo "${USERNAME} ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/${USERNAME}"; \
    chmod 0440 "/etc/sudoers.d/${USERNAME}"; \
    git config --system --add safe.directory '*'

ENV HOME=/home/${USERNAME} \
    GOPATH=/home/${USERNAME}/go \
    GOCACHE=/home/${USERNAME}/.cache/go-build \
    GOMODCACHE=/home/${USERNAME}/go/pkg/mod
ENV PATH=/home/${USERNAME}/.local/bin:/home/${USERNAME}/go/bin:/usr/local/go/bin:/usr/local/node/bin:${PATH}

USER ${USERNAME}
WORKDIR /home/${USERNAME}

# Editor tools. The module and build caches are emptied afterwards; the
# empty directories stay, owned by the dev user, so named volumes mounted
# on them start out with the right owner.
RUN set -eux; \
    go install golang.org/x/tools/gopls@latest; \
    go install golang.org/x/tools/cmd/goimports@latest; \
    go install github.com/go-delve/delve/cmd/dlv@latest; \
    go clean -cache -modcache; \
    mkdir -p "$GOCACHE" "$GOMODCACHE" "$HOME/.npm"

COPY --chown=${USER_UID}:${USER_GID} tmux.conf /home/${USERNAME}/.tmux.conf
COPY --chown=${USER_UID}:${USER_GID} init.el /home/${USERNAME}/.emacs.d/init.el
COPY --chown=${USER_UID}:${USER_GID} inputrc /home/${USERNAME}/.inputrc
# Sourced after Ubuntu's default ~/.bashrc, so it wins over that file's PS1
# and runs after its dircolors. ~/.profile sources ~/.bashrc for login shells.
COPY --chown=${USER_UID}:${USER_GID} bashrc /home/${USERNAME}/.bashrc.local
RUN printf '\n[ -f ~/.bashrc.local ] && . ~/.bashrc.local\n' >> "$HOME/.bashrc"
RUN emacs --batch -l "$HOME/.emacs.d/init.el" \
        --eval "(unless (package-installed-p 'go-mode) (kill-emacs 1))"

RUN if [ "${INSTALL_CLAUDE}" = "1" ]; then curl -fsSL https://claude.ai/install.sh | bash; fi

CMD ["/bin/bash", "-l"]
