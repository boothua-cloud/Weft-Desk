#!/bin/bash
# 取 WinTun 运行库（wintun.dll）。
#
# 云栈在 Windows 上靠 WinTun 建虚拟网卡，wintun 包按
# LoadLibraryEx(APPLICATION_DIR|SYSTEM32) 查找，所以 dll 必须和可执行文件同目录；
# 缺了的话点「连接」只会得到一句 Unable to load library: wintun.dll。
#
# 用法：bash scripts/fetch-wintun.sh [amd64|arm64]
set -e
cd "$(dirname "$0")/.."

VER="${WINTUN_VERSION:-0.14.1}"
ARCH="${1:-amd64}"
URL="https://www.wintun.net/builds/wintun-${VER}.zip"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> 下载 wintun ${VER}（${ARCH}）"
curl -fsSL --max-time 120 "$URL" -o "$TMP/wintun.zip"
unzip -qo "$TMP/wintun.zip" -d "$TMP/x"

mkdir -p bin
cp "$TMP/x/wintun/bin/${ARCH}/wintun.dll" bin/wintun.dll
echo "已放置 bin/wintun.dll（wintun ${VER} · ${ARCH}）"
echo "打包时把 bin/wintun.dll 放在 exe 同目录即可。"
