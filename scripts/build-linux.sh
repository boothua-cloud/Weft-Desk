#!/bin/bash
# 在 Ubuntu / Debian 上构建云栈。Linux 版的 Wails 后端依赖 webkit2gtk 的 C 库，
# 必须在 Linux 环境里编译，macOS 上的交叉编译跑不通。
#
# 用法：把整个项目目录拷到 Ubuntu 机器上，执行 bash scripts/build-linux.sh
set -e
cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  echo "需要先装 Go 1.24+：https://go.dev/dl/"
  exit 1
fi

echo "==> 安装系统依赖（需要 sudo）"
sudo apt-get update
sudo apt-get install -y \
  build-essential pkg-config \
  libgtk-3-dev libwebkit2gtk-4.1-dev \
  nodejs npm

echo "==> 安装 wails3"
go install github.com/wailsapp/wails/v3/cmd/wails3@latest
export PATH="$PATH:$(go env GOPATH)/bin"

echo "==> 构建"
wails3 build -tags devtools

echo "==> 打包"
mkdir -p release
tar -C bin -czf "release/云栈-linux-amd64.tar.gz" 云栈
echo "完成：release/云栈-linux-amd64.tar.gz"
echo
echo "运行前确认运行时库："
echo "  sudo apt install -y libgtk-3-0 libwebkit2gtk-4.1-0"
