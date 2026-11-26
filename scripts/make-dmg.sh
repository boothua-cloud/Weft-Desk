#!/bin/bash
# 打包 macOS 安装盘（.dmg）。
# Finder 的背景图只按 1pt=1px 渲染，Retina 上会被放大两倍导致文字发糊，
# 所以这里在打包完后再补一份 .background@2x.png，系统会自动优先用它。
set -e
cd "$(dirname "$0")/.."

APP="Weft"
VER="0.1.0"
OUT="release/${APP}-${VER}-macos-arm64.dmg"
RW="/tmp/weft-rw-$$.dmg"
SRC="/tmp/weft-src-$$.dmg"

# 清掉可能残留的旧挂载点
for v in "/Volumes/${APP}" "/Volumes/${APP} 1" "/Volumes/${APP} 2"; do
  hdiutil detach "$v" -quiet 2>/dev/null || true
done

# 先用当前的 bin/weft 重新生成 .app，否则会把上一次的旧二进制打进安装包
./scripts/make-app.sh
cp -R "bin/${APP}.app" release/
wails3 tool package --format dmg --name "$APP" --out release \
  --background build/darwin/dmg-background.png \
  --window-width 540 --window-height 380 >/dev/null
mv "release/${APP}.dmg" "$SRC"
rm -rf "release/${APP}.app"

hdiutil convert "$SRC" -format UDRW -o "${RW%.dmg}" >/dev/null
hdiutil attach "$RW" -nobrowse -quiet
cp "build/darwin/dmg-background@2x.png" "/Volumes/${APP}/.background@2x.png"
sync
hdiutil detach "/Volumes/${APP}" -quiet

rm -f "$OUT"
hdiutil convert "$RW" -format UDZO -o "$OUT" >/dev/null
rm -f "$SRC" "$RW"
echo "已生成 $OUT"
