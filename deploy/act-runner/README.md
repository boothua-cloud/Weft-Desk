# act_runner

给 `git.example.com` 跑 `.gitea/workflows/build.yml` 的执行器，部署在 192.168.10.5（与 Gitea 同机）。

## 起

```bash
mkdir -p /opt/act-runner && cd /opt/act-runner
# 放入 docker-compose.yml 和 .env（.env 里填 RUNNER_TOKEN）
docker compose up -d
docker compose logs -f     # 看到 "declare successfully" 就好了
```

之后在「站点管理 → Actions → Runners」能看到它，状态空闲。

## 内网直连

`GITEA_INSTANCE_URL` 同时决定两件事：act_runner 汇报/拉任务的地址，以及 job 里的
`GITHUB_SERVER_URL`——`actions/checkout` 拿它拼 clone 地址。所以填内网地址后，
CI 拉代码也走内网，不经过公网域名，也就绕开了仓库前面的 WAF。

前提是 Gitea 的端口映射到 `0.0.0.0` 或 `192.168.10.5`：

```bash
docker ps --format '{{.Names}}\t{{.Ports}}' | grep gitea
# 要看到 0.0.0.0:3000->3000/tcp 或 192.168.10.5:3000->3000/tcp
# 只有 127.0.0.1:3000->3000/tcp 的话，job 容器连不进去，用 composer 文末的方案 B
```

## 排查

| 现象 | 原因 |
|---|---|
| `open config file ... no such file` | 挂了一个不存在的 config.yml，去掉这个挂载（首次启动会自己生成） |
| runner 一直离线 | 容器出不了网，或令牌被重置过 |
| job 卡在 `Waiting for a runner` | label 对不上，`runs-on` 要和 `GITEA_RUNNER_LABELS` 冒号前那截一致 |
| 拉 action 超时 | 容器没走代理，打开 `HTTPS_PROXY`；`NO_PROXY` 里保留内网段 |
| `pkg-config gtk+-3.0` 找不到 | job 镜像里缺依赖，workflow 已有 `apt-get install libgtk-3-dev libwebkit2gtk-4.1-dev` |
| job 超时 | Go 首次编译 + 依赖下载慢，act_runner 默认 3 小时，一般够 |

## 关于 macOS

这台 runner 只跑 Linux 容器。`build.yml` 里的 `macos` job 需要 `macos-latest` 标签，
且必须是真正跑在 macOS 上的 act_runner（Docker 容器假装不了）。
没有 Mac 机器就把它从 workflow 里删掉，dmg 本地出。
