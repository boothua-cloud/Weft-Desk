# act_runner

给 `git.boothua.cn` 跑 `.gitea/workflows/build.yml` 的执行器。

## 起

```bash
cp .env.example .env      # 填 RUNNER_TOKEN
docker compose up -d
docker compose logs -f    # 看到 "declare successfully" 就好了
```

在 Gitea 的「站点管理 → Actions → Runners」里能看到它变成空闲状态。

## 排查

| 现象 | 原因 |
|---|---|
| `open config file ... no such file` | 挂了一个不存在的 config.yml，去掉这个挂载即可（首次启动会自己生成） |
| runner 一直离线 | 容器出不了网，或者 token 失效（重置过令牌） |
| job 卡在 `Waiting for a runner` | label 对不上，workflow 里 `runs-on` 要和 `GITEA_RUNNER_LABELS` 冒号前那截一致 |
| 拉 action 超时 | 容器没走代理，把 `HTTPS_PROXY` 打开，`NO_PROXY` 里保留 `git.boothua.cn` |
| 编译报 `pkg-config gtk+-3.0` | job 容器的镜像里没装依赖，workflow 里已经有 `apt-get install libgtk-3-dev libwebkit2gtk-4.1-dev` |

## 关于 macOS

这台 runner 只能跑 Linux 容器，`macos` job 需要 `macos-latest` 标签的 runner，
而那个必须是真正跑在 macOS 上的 act_runner（不能是 Docker 容器）。
没有 Mac 机器时，把 `build.yml` 里的 `macos` job 去掉，dmg 本地出。
