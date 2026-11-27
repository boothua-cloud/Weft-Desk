FROM node:20-bookworm

ENV DEBIAN_FRONTEND=noninteractive \
    TZ=Asia/Shanghai \
    LANG=C.UTF-8

# apt 走阿里云：这堆包一共 150MB+，走代理容易 fake-ip 解析到 198.18.x.x 然后 502
RUN sed -i 's|deb.debian.org|mirrors.aliyun.com|g' \
        /etc/apt/sources.list /etc/apt/sources.list.d/*.sources 2>/dev/null || true \
 && apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkg-config \
        libgtk-3-dev libwebkit2gtk-4.1-dev libayatana-appindicator3-dev \
        nsis zip unzip file \
        git curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*


ARG GO_VERSION=1.27.4
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz \
 && tar -C /usr/local -xzf /tmp/go.tgz \
 && rm -f /tmp/go.tgz \
 && /usr/local/go/bin/go version

ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH
ENV GOTOOLCHAIN=local

ARG WAILS_VERSION=v3.0.0
RUN CGO_ENABLED=0 go install -trimpath -ldflags="-s -w" \
        github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_VERSION} \
 && wails3 version

ENV GOCACHE=/root/.cache/go-build \
    GOMODCACHE=/root/go/pkg/mod \
    npm_config_cache=/root/.npm

WORKDIR /workspace
