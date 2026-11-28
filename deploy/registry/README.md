# 轻量镜像仓库

一个 `registry:2` 容器，给 CI 存构建镜像用。

## 为什么不用 Harbor

Harbor 是 8 个容器（core、jobservice、portal、registry、proxy、postgresql、redis、trivy），
跑起来 2~3G 内存，还要一份 harbor.yml 和 installer。对一个「CI 拉个基础镜像」的需求来说太重。

如果你要的是 UI、项目隔离、RBAC、漏洞扫描、镜像签名，那就该上 Harbor；只是存镜像，这个够。

## 起

```bash
mkdir -p data
docker compose up -d
curl -s http://127.0.0.1:5000/v2/_catalog     # 返回 {"repositories":[]} 就通了
```

## 推拉

客户端（192.168.10.5）要信任内网 HTTP：

```bash
sudo tee /etc/docker/daemon.json >/dev/null <<'JSON'
{ "insecure-registries": ["<极空间内网IP>:5000"] }
JSON
sudo systemctl restart docker
```

```bash
docker build -t weft-ci:1 deploy/ci-image/
docker tag weft-ci:1 <极空间IP>:5000/weft-ci:1
docker push <极空间IP>:5000/weft-ci:1
```

没开认证，所以不用 login。

## runner 那边

```yaml
GITEA_RUNNER_LABELS: "ubuntu-latest:docker://<极空间IP>:5000/weft-ci:1"
```

## 清理

registry 不会自动回收删除的空间。删 tag 后要跑一次 GC：

```bash
docker exec registry bin/registry garbage-collect /etc/docker/registry/config.yml
```

## 如果要上 Harbor

极空间上装 Harbor 前先确认两件事：

1. **CPU 架构**：官方镜像只提供 amd64。极空间 Z2/Z4 系列是 ARM，跑不了；
   x86 型号（如 Z4S）可以。
2. **内存**：至少留 4G 给 Harbor，低于这个 postgresql 和 core 会反复重启。

x86 型号上按官方文档走：

```bash
# installer 在 github releases 上，国内建议用代理或换源下载
wget https://github.com/goharbor/harbor/releases/download/v2.11.0/harbor-offline-installer-v2.11.0.tgz
tar xzf harbor-offline-installer-v2.11.0.tgz && cd harbor
cp harbor.yml.tmpl harbor.yml
# 改这三项：hostname（用内网 IP）、harbor_admin_password、data_volume（指向存储池）
./install.sh
```

装完 `http://<内网IP>` 打开，默认账号 `admin`。
之后项目要设为 public，否则 CI 拉镜像得先 docker login。
