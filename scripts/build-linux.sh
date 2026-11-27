#!/bin/bash
# 在 Ubuntu / Debian 上构建云栈。Linux 版的 Wails 后端要链 webkit2gtk 的 C 库，
# 必须原生编译，交叉编译跑不通。
#
# 用法：把整个项目目录拷到 Ubuntu 机器上，执行 bash scripts/build-linux.sh
#
# 注意构建标签必须是 `gtk3`：wails v3.0 默认走 GTK4 + WebKitGTK 6.0
# （pkg-config: gtk4 webkitgtk-6.0），而 Debian 12 上没有 webkitgtk-6.0。
# 加 gtk3 后链的是 gtk+-3.0 + webkit2gtk-4.1，Ubuntu 22.04+ / Debian 12 都能跑，
# deb 的 Depends 也要跟着是 libgtk-3-0 / libwebkit2gtk-4.1-0。
set -e
cd "$(dirname "$0")/.."

VER="${APP_VERSION:-0.1.3}"

if ! command -v go >/dev/null 2>&1; then
  echo "需要先装 Go 1.27+：https://go.dev/dl/"
  exit 1
fi

echo "==> 安装系统依赖（需要 sudo）"
sudo apt-get update
sudo apt-get install -y \
  build-essential pkg-config \
  libgtk-3-dev libwebkit2gtk-4.1-dev libayatana-appindicator3-dev \
  nodejs npm

echo "==> 安装 wails3"
# 必须 CGO_ENABLED=0：否则 linux+cgo 的 CLI 会 import internal/operatingsystem/
# webkit_linux.go，硬要 pkg-config gtk4 + webkitgtk-6.0，而本项目按 gtk3 构建
CGO_ENABLED=0 go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
export PATH="$PATH:$(go env GOPATH)/bin"

echo "==> 构建（CGO + gtk3）"
(cd frontend && npm ci --include=dev && npm run build)
CGO_ENABLED=1 go build -tags "production gtk3" -trimpath -buildvcs=false \
  -ldflags="-w -s" -o bin/weft .

echo "==> 生成 .desktop"
mkdir -p build/linux
wails3 generate .desktop -name "云栈" -exec weft -icon weft \
  -outputfile build/linux/weft.desktop -categories "Network;Utility;"

echo "==> 打 deb"
mkdir -p release
wails3 tool package -name weft -format deb \
  -config ./build/linux/nfpm/nfpm.yaml -out release
# wails3 只按 <name>.deb 命名，版本与架构自己补，否则下一个版本会覆盖同名文件
mv release/weft.deb "release/weft_${VER}-1_amd64.deb"

echo "==> 附一份 tar.gz（deb 装不上时可直接跑）"
tar -C bin -czf "release/weft-${VER}-linux-amd64.tar.gz" weft

ls -la release
echo
echo "安装：sudo dpkg -i release/weft_${VER}-1_amd64.deb && sudo apt-get -f install"
echo "运行前确认运行时库："
echo "  sudo apt install -y libgtk-3-0 libwebkit2gtk-4.1-0"
