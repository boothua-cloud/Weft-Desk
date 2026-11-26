# syntax=docker/dockerfile:1
#
# CI 构建镜像：把 Go / Node / Wails / GTK 依赖一次性装好。
#
# 构建（amd64 机器上，比如 10.0.1.5）：
#   docker build -t weft-ci:1 -t weft-ci:latest .
# Mac 上构建必须加 --platform linux/amd64，否则产出 arm64 镜像，Ubuntu 上跑不了。
#
# runner 的 label：
#   GITEA_RUNNER_LABELS: "ubuntu-latest:docker://weft-ci:latest"

FROM node:20-bookworm

# 所有源都走国内，避免构建时卡在 deb.debian.org / proxy.golang.org / github
ENV DEBIAN_FRONTEND=noninteractive \
    TZ=Asia/Shanghai \
    LANG=C.UTF-8

# ── 1. 系统依赖 ────────────────────────────────────────────────
# Wails 的 Linux 后端要 gtk3 + webkit2gtk；nsis 给 installer job 用
RUN sed -i 's|deb.debian.org|mirrors.ustc.edu.cn|g' \
        /etc/apt/sources.list /etc/apt/sources.list.d/*.sources 2>/dev/null || true \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
        build-essential pkg-config \
        libgtk-3-dev libwebkit2gtk-4.1-dev libayatana-appindicator3-dev \
        nsis zip unzip file \
        git curl ca-certificates \
 && rm -rf /var/lib/apt/lists/* /tmp/*

# ── 2. Go 工具链 ──────────────────────────────────────────────
# 中科大镜像，和 apt 用同一家；阿里云也验证过能下
ARG GO_VERSION=1.27.4
RUN curl -fsSL "https://mirrors.ustc.edu.cn/golang/go${GO_VERSION}.linux-amd64.tar.gz" \
        -o /tmp/go.tgz \
 && tar -C /usr/local -xzf /tmp/go.tgz \
 && rm -f /tmp/go.tgz \
 && /usr/local/go/bin/go version

ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH

# GOTOOLCHAIN=local 是关键：go.mod 里的 go 版本如果高于本地工具链，
# Go 1.21+ 会自作主张去下载对应版本的工具链（走 proxy.golang.org，国内必超时）。
# 锁成 local 就只用镜像里这一份。
# GOPROXY 写多个，前面的不通自动跳下一个。
ENV GOPROXY=https://goproxy.cn,https://mirrors.aliyun.com/goproxy/,direct \
    GOSUMDB=sum.golang.google.cn \
    GOTOOLCHAIN=local \
    GOFLAGS=-buildvcs=false

# ── 3. wails3（预编译，省掉每个 job 一次 go install）────────────
# CGO_ENABLED=0：纯 Go 工具，开了 CGO 反而会在交叉环境下让 gcc 收到 -m64 报错
ARG WAILS_VERSION=v3.0.0
RUN CGO_ENABLED=0 go install -trimpath -ldflags="-s -w" \
        github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_VERSION} \
 && wails3 version \
 && rm -rf /root/go/pkg/mod/cache/download /root/.cache/go-build

# ── 4. npm ───────────────────────────────────────────────────
# 与 frontend/package-lock.json 里的 resolved 保持一致（都是腾讯云）
RUN npm config set registry https://mirrors.cloud.tencent.com/npm/ \
 && npm config set fund false \
 && npm config set audit false \
 && npm config set progress false

# 缓存目录留在固定位置，runner 会把宿主的目录挂进来复用
ENV GOCACHE=/root/.cache/go-build \
    GOMODCACHE=/root/go/pkg/mod \
    npm_config_cache=/root/.npm

WORKDIR /workspace
