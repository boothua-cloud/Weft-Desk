# Gitea 内网部署参考

app.ini 已从仓库移除（里面是真实的数据库密码和各类密钥）。
自己部署时按 Gitea 官方文档生成一份，别把真实配置提交进仓库。

docker-compose.yml 里的 RUNNER_TOKEN 走环境变量，不写死。
