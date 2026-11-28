# 开发计划

排进版本的东西。全量功能池在 `docs/roadmap.md`，这里只放要先做的。

做完一项把 `[ ]` 改成 `[x]`，底下补一行怎么做的、怎么验的。一批全做完再动 `version.go`、`CHANGELOG.md` 和 tag。

0.x 阶段 minor 是一个能力批次，patch 只修 bug 不动界面。

## 0.1.5 — 已发布

https://github.com/boothua-cloud/Weft-Desk/releases/tag/v0.1.5

- [x] ACL 拆成「用户组与标签 / 规则 / 源码」三个页签，矩阵钉在规则下面
- [x] 权限矩阵去掉白卡和阴影
- [x] 文案整体收短
- [x] 修设备管理页打不开（TDZ）
- [x] 转发流量列按量级显示
- [x] 帮助手册 14 节，README 补全所有页面
- [x] 截图重拍，示例数据全虚构

## 0.2.0

主题：连不上、为什么慢，都要在界面上看得见。

- [x] 服务端自签证书
  - `sharedHTTP` 换成按服务端配置构造的 client。CA PEM 或「跳过校验」按服务端存，跟 Key 一样进配置。
  - 结果：`newHTTPClient` 里挂 `tls.Config`，`newControlPlane` 时重建。CA 解析不了的当场报错。
  - 验：`httptest.NewTLSServer` 起自签服务，不配 CA 连不上、配了 CA 能连、勾跳过也能连。

- [x] 读系统代理
  - macOS 走 `scutil --proxy`（连例外列表一起解析），Windows 读注册表，环境变量优先。
  - 结果：新增 `proxycfg_darwin.go`、`proxycfg_windows.go`、`proxycfg_other.go`。命中例外就不走代理，内网域名不会被绕进去。
  - 验：单测覆盖例外匹配与 scutil 输出解析；Windows 交叉编译通过。

- [x] 应用书签在多台设备间同步
  - 本机在 tailnet 上开一个只给同账号设备访问的端口，另一台凭同步码拉取或推送；按 URL 合并，删除不传播。
  - 新增 `apps_sync.go`，`apps` 表加 `updated_at`，Apps.vue 加同步抽屉。
  - 结果：监听用 `net.Listen` 绑节点的 100.x 地址。tsnet 自带的 `Listen` 走进程内用户态栈，注入真实网卡后拿不到入站包，实测端口起不来。
  - 验：`curl /v1/info` 返回主机与条数，带码取 `/v1/apps` 正常、错码 401；同一批推两次第二次全是 skipped；单测覆盖合并、拉取、推送、错码。

- [x] 诊断面板
  - 每台设备一行，点检查实测：三次 disco 探测看链路，再拨一次目标端口（默认 22）。
  - 结论按链路层往上排：离线 → 探测全丢 → 端口不通。拨测失败时读包过滤规则，
    能分清「ACL 没放行」和「对端没开服务」。
  - 结果：`systun.go` 加 `DiagPeer`，`diag.go` 加 `aclAllows` / `diagVerdict`（纯函数好测），
    新增 `views/Diagnose.vue`。引擎没起来、对端不在 netmap 里都各有说法。
  - 验：单测覆盖 ACL 规则匹配（网段、区间、协议、通配）、结论分支；截图看过实际界面。

- [x] ACL 表单补 `hosts` 和 `autoApprovers`
  - 用户组与标签页加了「地址别名」和「自动批准」两张卡，别名参与目标白名单校验。
  - 结果：`AclForm` 多四个字段，值不是简单字符串映射时整段留在 `extra` 原样保存，
    表单不碰。清空别名不写空 `hosts`。
  - 验：Node 脚本跑往返，`hosts` / `autoApprovers` 原样进出，`extra` 不重复。

- [x] 策略历史和回滚
  - 保存前先读线上策略，存进 `policy_history`，留最近 20 版；回滚走同一条保存路径，
    所以回滚前那版也会进历史。
  - 结果：`store.go` 加列表、取单版、裁剪；`weft.go` 加 `ListPolicyHistory` /
    `GetPolicyVersion` / `RestorePolicy`；ACL 页顶栏「历史」抽屉带行级 diff。
  - 验：假 headscale 跑保存、同内容不记历史、回滚后可再回滚、裁剪保留最新 20 版。

- [x] 设备巡检
  - 总览页「需要处理」：路由未批、7 天内到期、离线超 7 天、生成 7 天没用的 Key。
  - 结果：都从前端已有数据算，不额外打接口；每条给动作或跳到对应页。
  - 验：截图上四类都能出现，已过期的 Key 不重复提醒。

## 0.3.0

主题：改策略别改出事。

- [ ] ACL 风险体检：`*:*`、目标写 `*`、敏感端口对全员开放
- [ ] 保存前 diff 预览
- [ ] 校验提示一键修
- [ ] 策略模板，支持当前策略另存
- [ ] 批量操作：多选设备打标签、批路由、批过期
- [ ] 审计页：补全写操作、搜索、导出
- [ ] 一键诊断包，配置自动脱敏
- [ ] 离线态：headscale 挂了显示缓存和时间戳

## 0.4.0

主题：日常用着顺。

- [ ] ⌘K 命令面板
- [ ] 内嵌终端，走 100.x + `x/crypto/ssh`
- [ ] 本地名字解析：127.0.0.1:53 把设备名解析成 100.x
- [ ] MTU 探测
- [ ] 延迟和丢包曲线
- [ ] 应用图标抓 favicon、拖动排序、最近使用
- [ ] 应用小窗钉在最前
- [ ] 首次启动向导

## 待定

动网络层的大件，想清楚再做。

- [ ] 出口节点
- [ ] 分流（只接管指定网段）
- [ ] 文件互传
- [ ] 服务发现扫描
- [ ] macOS 公证 + Windows 代码签名
- [ ] CLI 伴侣
- [ ] 中英双语

## 发版检查

- [ ] `CHANGELOG.md` 的日期是当天
- [ ] `version.go` 和 `build.yml` 的版本号一致
- [ ] 本机 `wails3 build` + `make-app.sh`，装到 `/Applications` 跑一遍
- [ ] 截图对得上界面，且都是虚构数据
- [ ] 打 tag 推上去，等 CI 出三平台包，建 Release，说明粘 CHANGELOG 对应小节
