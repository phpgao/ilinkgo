# AGENTS.md

给 Agent / 新同学的项目速查。微信 iLink Bot 发消息工具，CLI + 本地 HTTP API，基于
[openilink-sdk-go](https://github.com/openilink/openilink-sdk-go)。

## 常用命令

```bash
go build ./...              # 构建
go vet ./...                # 静态检查
golangci-lint run ./...     # lint（提交前必过）
go build -o ilinkgo .       # 出二进制
go run . send text --text "hi"   # 本地跑（需先 login）
```

依赖拉取可能被内网 GOPROXY 拦截，必要时用：

```bash
GOPROXY=direct GOSUMDB=off go get <module>
```

## 目录结构

```
main.go                        cobra Execute 入口
cmd/root.go                    root 命令、--data-dir(persistent)、newSender
cmd/login.go                   login / logout（终端二维码）
cmd/serve.go                   serve + monitorLoop（长轮询，可重入）
cmd/send.go                    send 及 text/image/video/file/media/typing 子命令
internal/sender/sender.go      发消息唯一入口 + 收件人/contextToken 解析
internal/store/store.go        凭证、contextToken、默认接收人、游标落盘(0600)
internal/apiserver/            HTTP API（含 /api/login 绑定）
.github/workflows/docker.yml   推 main / v* tag 时构建推送 ghcr.io
```

## 关键约定

- **发消息只能走 `internal/sender`**：CLI 和 HTTP API 共用同一份逻辑，不要在命令层或 handler 里直接调 SDK。
- **contextToken 三级回退**：显式传入 → SDK 内存缓存 → 磁盘缓存。iLink 协议要求 bot 只能给「给它发过消息的用户」推送，拿不到 token 就发不出去。
- **默认接收人**：`serve` 后台把最近发消息的用户写入 `default_user`，有新人发言即替换；`--to` / API 的 `to` 可省略，显式传入则覆盖。
- **voice（item type 3）只能收不能发**：SDK 没有发送接口，不要在 send 里补这个类型。
- **收件人解析顺序**：先定收件人再取 token，见 `Sender.resolve`。
- **不要引入配置框架 / 消息总线 / 多 channel 抽象**：这是 `wechat-gateway-go`（MQTT + YAML + bus）被判定过度设计后的精简重写版。

## 已知坑

- `qrcode_img_content` 返回的是**绑定 URL**，不是图片也不是 ASCII 图，必须自己编码成二维码才能扫（`cmd/login.go`、`internal/apiserver` 各有一处渲染）。
- cobra 的 `MarkFlagRequired` 在 `init()` 阶段看不到父命令的 persistent flag，会 panic；共享 flag 的必填校验统一走 `requireFlags`（在 `RunE` 里做）。
- 进程提示走 stderr（`cmd.PrintErrln`），结果（client_id / sent）走 stdout，便于脚本管道。
- 容器内必须监听 `0.0.0.0`（Dockerfile 的 CMD 已覆盖默认的 127.0.0.1）。
- `serve` 未登录时**不退出**，常驻等待 `/api/login` 绑定，成功后回调唤醒 monitor，无需重启。

## 提交与发布

- 提交信息用 Conventional Commits（`feat`/`fix`/`refactor`/`docs`/`ci`/`build`），body 用 `- ` 列表。
- **禁止 `git add -A` / `git add .`**，逐文件暂存；敏感信息扫描通过后再提交。
- push 前确认 `git remote -v` 与 `git config user.email`（本仓库走 includeIf → phpgao）。
- 发版：打 `v*` tag 并推送，workflow 自动构建 `linux/amd64` + `linux/arm64` 推送到 `ghcr.io/phpgao/ilinkgo`。
