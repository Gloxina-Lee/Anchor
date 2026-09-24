# Anchor

私有的单用户短链接服务。Go 提供公开跳转、登录和管理 API；Vue 管理界面位于 `/admin/`，根路径 `/` 当前为空白页。创建、删除、设置等管理操作需要登录，访问有效短码时无需登录。

## 本地运行

需要 Go 1.27、Node.js 24 和 npm。

```powershell
cd web
npm ci
npm run build
cd ..
$env:ANCHOR_INSECURE_COOKIES = 'true'
go run .
```

然后打开 `http://127.0.0.1:8080/admin/`。首次访问可设置唯一的管理员账号。所有需要持久化的数据保存在 `./data/anchor.db`。

开发前端时可以在 `web` 目录运行 `npm run dev`，访问 Vite 提供的 `/admin/`。要检查根路径与短码跳转，应使用 Go 服务地址。

## Docker 部署

镜像采用 Node.js Alpine 构建 Vue、Go Alpine 构建静态链接的 Go 程序，并在 Alpine 容器内运行。构建产物不依赖 Node.js 运行时。

```sh
docker build -t anchor:local .
sudo install -d -m 0700 -o 10001 -g 10001 /srv/anchor-data
docker run -d --name anchor --restart unless-stopped \
  --mount type=bind,src=/srv/anchor-data,dst=/data \
  -p 127.0.0.1:8080:8080 anchor:local
```

建议由宿主机反向代理提供 HTTPS，并保留请求的 Host。生产环境默认使用 `Secure` 会话 Cookie，因此应通过 HTTPS 访问管理界面。不要在公网开放尚未完成初始化的服务；未初始化时首位访问者可以创建管理员账号。

宿主机的 `/srv/anchor-data` 是唯一的持久化目录，包含账户、设置、短链和会话数据。镜像以 UID/GID `10001` 运行，所以绑定目录必须允许该用户写入。备份时请先停止容器，再复制该目录。更换镜像时保留同一目录即可保留数据。

可用环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `ANCHOR_LISTEN` | `:8080` | 监听地址 |
| `ANCHOR_DATA_DIR` | `./data`，镜像内为 `/data` | 持久化目录 |
| `ANCHOR_WEB_DIR` | `./web/dist`，镜像内为 `/app/web` | Vue 构建产物目录 |
| `ANCHOR_INSECURE_COOKIES` | 未设置 | 仅在本地 HTTP 开发时设为 `true` |

短链主域名取自实际访问的站点地址，没有写死在程序中。

## 行为约定

- 自动短码从设置的最小长度开始，碰撞时重试并在必要时加长。手动短码为 1 至设置的最大长度个 ASCII 字母或数字。
- 点击管理页面顶部按钮，在弹窗中创建短链接。列表显示短码，复制按钮会复制包含当前站点地址的完整短链接。
- 每条短链接可在创建时填写备注，并在列表中查看、搜索、编辑或清空。备注最长 500 个字符。
- `/api`、`/auth`、`/admin` 等系统路径不可作为短码。保留名称检查不区分大小写。
- 未生效、已到期、已删除和不存在的短码均返回 404。有效短链使用 302 跳转，响应不缓存，以支持将来到期和复用。
- 快捷到期时间从生效时间起算；“1 月”按日历月计算。时间经浏览器本地时区输入，后端以 UTC 保存。
- 到期或删除后的短码默认允许复用，可在设置中关闭。已复用的旧记录不能再编辑。

## 验证

```powershell
$env:GOCACHE = Join-Path (Get-Location) '.cache/go-build'
$env:GOPATH = Join-Path (Get-Location) '.cache/gopath'
go test ./...
go vet ./...
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -o .cache/anchor-linux .
```

在 `web` 目录执行 `npm run build` 验证前端。Linux 二进制需要与目标 Debian 服务器的架构一致，例如 `amd64` 或 `arm64`。
