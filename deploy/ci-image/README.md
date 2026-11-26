# CI 构建镜像

一次性把工具链装好，避免每个 job 重复下载。

## 为什么

每次 job 都是全新的 `node:20-bookworm` 容器，原来要做这些事：

| 步骤 | 耗时 |
|---|---|
| 下载 Go tarball（70MB） | 20~40s |
| `go install wails3` | 40~90s（首次编译） |
| `apt install libgtk-3-dev libwebkit2gtk-4.1-dev` | 30~60s |
| `npm ci` | 30~60s |
| `go mod download` | 60~120s |

加起来两三分钟纯等待，每次都重来。

## 构建

**在 10.0.1.5（amd64）上构建**，别在这台 Mac 上做：

```bash
cd deploy/ci-image
docker build -t weft-ci:1 .
docker tag weft-ci:1 weft-ci:latest
```

Mac 是 arm64，直接 `docker build` 出来的镜像也是 arm64，推到 amd64 的 runner 上用不了（报 `exec format error`）。非要在这儿构建就得写平台，走 QEMU 模拟，编译慢好几倍：

```bash
docker build --platform linux/amd64 -t weft-ci:1 .
```

镜像约 1.8G（Node 20 的 1.6G + GTK 依赖 + Go + wails3）。

## 让 runner 用上它

改 `docker-compose.yml`：

```yaml
environment:
  GITEA_RUNNER_LABELS: "ubuntu-latest:docker://weft-ci:latest"
```

然后 `docker compose up -d`（recreate 才会读到新环境变量）。

## 再快一步：挂缓存

Go 的编译缓存和模块缓存是跨 job 复用的大头。在 `/data/config.yml` 里加：

```yaml
container:
  options: >-
    -v /opt/ci-cache/go-build:/root/.cache/go-build
    -v /opt/ci-cache/go-mod:/root/go/pkg/mod
    -v /opt/ci-cache/npm:/root/.npm
  valid_volumes:
    - /opt/ci-cache/*
```

`valid_volumes` 必须写，否则 act_runner 会拒绝挂载（安全限制）。宿主机上先建目录：

```bash
sudo mkdir -p /opt/ci-cache/{go-build,go-mod,npm}
sudo chmod -R 777 /opt/ci-cache
```

效果：第一次仍然全量编译，之后拿缓存的构建通常在 40 秒到 1 分钟。
