# CI 构建镜像：Go / Node / Wails / GTK 依赖一次性装好。
#
# 构建（amd64 机器上）：
#   docker build -t weft-ci:1 -t weft-ci:latest .
#
# runner 的 label：
#   GITEA_RUNNER_LABELS: "ubuntu-latest:docker://weft-ci:latest"

FROM node:20-bookworm

ENV DEBIAN_FRONTEND=noninteractive \
    TZ=Asia/Shanghai \
    LANG=C.UTF-8

# 系统依赖：Wails 的 Linux 后端要 gtk3 + webkit2gtk，nsis 给 installer job 用
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkg-config \
        libgtk-3-dev libwebkit2gtk-4.1-dev libayatana-appindicator3-dev \
        nsis zip unzip file \
        git curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Go 工具链：官方源
ARG GO_VERSION=1.27.4
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz \
 && tar -C /usr/local -xzf /tmp/go.tgz \
 && rm -f /tmp/go.tgz \
 && /usr/local/go/bin/go version

ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH

# GOTOOLCHAIN=local：go.mod 的版本号比本地高时，Go 会自动去下载别的工具链，
# 锁成 local 就只用镜像里这一份
ENV GOTOOLCHAIN=local

# wails3 预编译。CGO_ENABLED=0：纯 Go 工具，开了反而在交叉环境报 -m64
ARG WAILS_VERSION=v3.0.0
RUN CGO_ENABLED=0 go install -trimpath -ldflags="-s -w" \
        github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_VERSION} \
 && wails3 version

ENV GOCACHE=/root/.cache/go-build \
    GOMODCACHE=/root/go/pkg/mod \
    npm_config_cache=/root/.npm

WORKDIR /workspace
