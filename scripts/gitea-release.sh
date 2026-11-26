#!/bin/bash
# 把 dist 目录里的构建产物传到 Gitea 的 Release。
# 用法：scripts/gitea-release.sh <产物目录> [tag]
# 需要环境变量：TOKEN（Gitea 访问令牌）、GITHUB_SERVER_URL、GITHUB_REPOSITORY
set -euo pipefail

DIST="${1:-release}"
TAG="${2:-build-$(date -u +%Y%m%d-%H%M)}"

SERVER="${GITHUB_SERVER_URL:-https://git.boothua.cn}"
REPO="${GITHUB_REPOSITORY:-boothua/WeftDesk}"
TOKEN="${TOKEN:-${GITEA_TOKEN:-}}"

if [ -z "$TOKEN" ]; then
  echo "缺少 TOKEN：在仓库设置里加 RELEASE_TOKEN 密钥，或导出 TOKEN 后再跑" >&2
  exit 1
fi
if [ ! -d "$DIST" ]; then
  echo "产物目录不存在: $DIST" >&2
  exit 1
fi

API="$SERVER/api/v1/repos/$REPO"
export API TOKEN TAG

# 1) 建 release；已存在就取回来
python3 - <<'PY'
import json, os, urllib.error, urllib.request

api, tok, tag = os.environ["API"], os.environ["TOKEN"], os.environ["TAG"]
hdr = {"Authorization": "token " + tok, "Content-Type": "application/json"}
payload = json.dumps({
    "tag_name": tag,
    "name": "云栈 " + tag,
    "body": "自动化构建产物：macOS dmg / Windows 安装包 / Linux 压缩包。",
    "draft": False,
    "prerelease": tag.startswith("v0"),
}).encode()

req = urllib.request.Request(api + "/releases", data=payload, headers=hdr, method="POST")
try:
    with urllib.request.urlopen(req) as r:
        rel = json.load(r)
except urllib.error.HTTPError:
    q = urllib.request.Request(api + "/releases/tags/" + tag,
                               headers={"Authorization": "token " + tok})
    with urllib.request.urlopen(q) as r:
        rel = json.load(r)

open("/tmp/weft-release-id", "w").write(str(rel["id"]))
print("release:", rel["id"], rel["tag_name"])
PY

RID=$(cat /tmp/weft-release-id)

# 2) 逐个上传附件；同名已存在时先删掉再传，保证内容是最新的
shopt -s nullglob
for f in "$DIST"/*; do
  name=$(basename "$f")
  case "$name" in
    *.dmg|*.exe|*.zip|*.tar.gz|*.deb) ;;
    *) continue ;;
  esac
  echo "上传 $name"
  # Gitea 不拦同名附件，直接传会留两份一样的文件。先查再删。
  AID=$(curl -sS "$API/releases/$RID/assets" -H "Authorization: token $TOKEN" |
    python3 -c "import json,sys;n=sys.argv[1];print(next((a['id'] for a in json.load(sys.stdin) if a['name']==n),''))" "$name" 2>/dev/null || true)
  if [ -n "${AID:-}" ]; then
    curl -sS -X DELETE "$API/releases/$RID/assets/$AID" -H "Authorization: token $TOKEN" >/dev/null
  fi
  curl -sS -f -X POST "$API/releases/$RID/assets?name=$name" \
    -H "Authorization: token $TOKEN" \
    -F "attachment=@$f" >/dev/null
done

echo "完成：$SERVER/$REPO/releases/tag/$TAG"
