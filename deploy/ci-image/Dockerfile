# CI 构建镜像：把 Go / Node / Wails / GTK 依赖一次性装好，
# 之后每个 job 不用再下 Go tarball、不用 apt 装 gtk、不用 go install wails3。
#
# 构建（在 10.0.1.5 上，原生 amd64）：
#   cd deploy/ci-image
#   docker build -t weft-ci:1 .
#   docker tag weft-ci:1 weft-ci:latest
#
# 非要在 Mac（arm64）上构建的话必须显式指定平台，否则产出的是 arm64 镜像，
# 到 Ubuntu 上跑不了：
#   docker build --platform linux/amd64 -t weft-ci:1 .
#
# 然后 runner 的 label 改成：
#   GITEA_RUNNER_LABELS: "ubuntu-latest:docker://weft-ci:latest"

FROM node:20-bookworm

ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=Asia/Shanghai

# apt 换中科大源，deb.debian.org 国内拉包很慢
RUN sed -i 's|deb.debian.org|mirrors.ustc.edu.cn|g' \
        /etc/apt/sources.list /etc/apt/sources.list.d/*.sources 2>/dev/null || true

# Wails 的 Linux 后端要 gtk3 + webkit2gtk；nsis 给 installer job 用
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkg-config \
        libgtk-3-dev libwebkit2gtk-4.1-dev \
        nsis zip unzip file \
        git curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Go：用阿里云镜像，国内直连
ARG GO_VERSION=1.27.4
RUN curl -fsSL "https://mirrors.aliyun.com/golang/go${GO_VERSION}.linux-amd64.tar.gz" \
        | tar -C /usr/local -xz \
    && /usr/local/go/bin/go version
ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn

# wails3：预先编译好，省掉每个 job 一次 go install
# CGO_ENABLED=0 必须开：CLI 是纯 Go 工具，关掉 CGO 才不会在交叉/模拟环境下
# 让 gcc 收到 -m64 这种只对某一架构有效的参数
ARG WAILS_VERSION=v3.0.0
RUN CGO_ENABLED=0 go install github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_VERSION} \
    && wails3 version

# npm 走国内镜像
RUN npm config set registry https://mirrors.cloud.tencent.com/npm/

# 缓存目录留着，runner 会把宿主机的目录挂进来
ENV GOCACHE=/root/.cache/go-build
ENV GOMODCACHE=/root/go/pkg/mod
ENV npm_config_cache=/root/.npm

WORKDIR /workspace
