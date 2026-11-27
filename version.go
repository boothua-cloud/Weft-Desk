package main

// appVersion 是客户端版本号，同时是自动更新比较的基准。
//
// 之前这个数字散在十来个文件里（plist、info.json、nsi、前端三处、README、
// 打包脚本），每次发版都得挨个改，漏一个就会出现「安装包是 0.1.5、界面还写
// 0.1.4」这种事。现在只留这一处：
//
//   - 前端从 State.AppVersion 读，不再硬编码
//   - CI 可用 -ldflags "-X main.appVersion=x.y.z" 覆盖，把 tag 直接注进去
//
// 版本号不带 v 前缀，和 updater 的比较规则一致（tag 上的 v 会被剥掉）。
var appVersion = "0.1.5"

// updateRepo 是发布源，"owner/repo" 形式。
const updateRepo = "boothua-cloud/Weft-Desk"
