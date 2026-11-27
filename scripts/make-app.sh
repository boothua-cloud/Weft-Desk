#!/bin/bash
# 把 bin/weft 包成 macOS .app（包名云栈.app，内部可执行文件叫 weft）：直接跑裸二进制的 Dock 图标和菜单栏名称由系统决定，
# 打包后 Info.plist 里的 CFBundleName / CFBundleIconFile 才会真正生效。
set -e
cd "$(dirname "$0")/.."

APP="bin/云栈.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "bin/weft" "$APP/Contents/MacOS/weft"
cp build/darwin/icon.icns "$APP/Contents/Resources/icon.icns"
cp build/darwin/icon.icns build/darwin/icons.icns  # wails3 package 读的是这个名字
cp build/darwin/Info.plist "$APP/Contents/Info.plist"
codesign --force --deep --sign - "$APP" >/dev/null 2>&1 || true
echo "已生成 $APP"
