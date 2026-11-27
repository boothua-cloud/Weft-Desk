#!/bin/bash
# 打包 macOS 安装盘（.dmg）。
# Finder 的背景图只按 1pt=1px 渲染，Retina 上会被放大两倍导致文字发糊，
# 所以这里在打包完后再补一份 .background@2x.png，系统会自动优先用它。
set -e
cd "$(dirname "$0")/.."

APP="云栈"      # .app 的显示名，Finder / Dock 里看到的就是它
PKG="weft"     # 产物文件名前缀，下载链接里别出现中文
# CI 打 tag 时由 VERSION 环境变量传进来，本地不传就用默认值
VER="${VERSION:-0.1.5}"
OUT="release/${PKG}-${VER}-macos-arm64.dmg"
RW="/tmp/weft-rw-$$.dmg"
SRC="/tmp/weft-src-$$.dmg"

# 清掉可能残留的旧挂载点
for v in "/Volumes/${APP}" "/Volumes/${APP} 1" "/Volumes/${APP} 2"; do
  hdiutil detach "$v" -quiet 2>/dev/null || true
done

# 先用当前的 bin/weft 重新生成 .app，否则会把上一次的旧二进制打进安装包
./scripts/make-app.sh
# release/ 在干净检出里不存在，cp 会把目录复制成同名文件
mkdir -p release
cp -R "bin/${APP}.app" release/

# 自动更新只认 zip（且压缩包里必须恰好一个顶层条目，也就是整个 .app）。
# dmg 是给用户手动拖的安装盘，框架不会去挂载它。
ZIP="release/${PKG}-${VER}-macos-arm64.zip"
rm -f "$ZIP"
# ZIP 是相对仓库根的路径；进了 release/ 之后只留文件名，
# 否则 ../${ZIP} 会落到仓库根，产物凭空多出一份在错误的位置。
# --norsrc 不能省：--sequesterRsrc 会额外塞一个 __MACOSX 顶层目录，
# 而自动更新要求压缩包里恰好一个顶层条目，多一个就整包被拒。
( cd release && ditto -c -k --norsrc --keepParent "${APP}.app" "$(basename "$ZIP")" )
echo "已生成 $ZIP"

wails3 tool package --format dmg --name "$APP" --out release \
  --background build/darwin/dmg-background.png \
  --window-width 540 --window-height 380
mv "release/${APP}.dmg" "$SRC"
rm -rf "release/${APP}.app"

if ! hdiutil convert "$SRC" -format UDRW -o "${RW%.dmg}"; then
  # 没有图形会话时造不了 dmg。zip 上面已经打好了，自动更新照样能用。
  echo "hdiutil 不可用（大概率没有图形会话），跳过 dmg；已生成的 zip 可直接用于自动更新" >&2
  rm -f "$SRC"
  exit 0
fi
hdiutil attach "$RW" -nobrowse
cp "build/darwin/dmg-background@2x.png" "/Volumes/${APP}/.background@2x.png"
sync
hdiutil detach "/Volumes/${APP}"

rm -f "$OUT"
hdiutil convert "$RW" -format UDZO -o "$OUT"
rm -f "$SRC" "$RW"
echo "已生成 $OUT"
