# FilmFusion 自建服务器部署

本文档面向希望把 FilmFusion 部署到自己 Linux 服务器、NAS 或家用主机的用户。推荐使用 Docker Compose 和 FilmFusion 发布镜像。

## 部署结构

| 服务 | 端口 | 是否需要对外开放 | 说明 |
| --- | --- | --- | --- |
| `film-fusion` | `9000` | 是，或仅交给反向代理 | Web 管理界面、API 和 Webhook |
| `film-fusion` | `8097` | 按需 | Emby 直链代理 |

FilmFusion 的配置、SQLite 数据库、日志、登录会话密钥和上传资源都保存在宿主机的 `./data` 目录。

## 一、准备服务器

服务器需要安装：

- Docker Engine
- Docker Compose v2（命令为 `docker compose`）
- Git

先确认命令可用：

```bash
docker --version
docker compose version
git --version
```

普通部署不需要在服务器上安装 Go、Node.js 或 pnpm。

## 二、下载部署文件

克隆仓库并准备配置文件：

```bash
git clone --depth 1 https://github.com/xifofo/film-fusion.git
cd film-fusion
cp data/config.example.yaml data/config.yaml
```

`data/config.yaml` 已被 Git 忽略，后续执行 `git pull` 不会覆盖它。

## 三、填写必需配置

### 1. 管理员账户

编辑 `data/config.yaml`，至少设置管理员用户名和强密码：

```yaml
server:
  port: 9000
  username: "admin"
  password: "请替换为强密码"
```

管理员密码不能为空，否则服务会拒绝启动。JWT 签名密钥会自动生成到 `data/.jwt-secret`，不需要手工填写。

### 2. Emby 地址

如果需要 Emby 集成，继续修改 `data/config.yaml`：

```yaml
emby:
  enabled: true
  url: "http://192.168.1.10:8096"
  run_proxy_port: 8097
  api_key: "你的 Emby API Key"
  admin_user_id: "你的 Emby 管理员用户 ID"
```

容器内的 `127.0.0.1` 指向 FilmFusion 容器自身，并不指向宿主机。请按实际环境填写：

- Emby 在局域网另一台主机：使用该主机的局域网 IP。
- Emby 与 FilmFusion 位于同一个 Docker 网络：使用 Emby 的 Compose 服务名和容器端口。
- 暂时不使用 Emby：将 `emby.enabled` 设为 `false`。

Emby API Key 可在 Emby 管理后台的“设置 → 高级 → API 密钥”中创建。

### 3. 挂载媒体目录（按需）

如果 FilmFusion 需要访问宿主机上的媒体、下载或 CloudDrive2 目录，可新建 `docker-compose.override.yml`，避免直接修改仓库中的 Compose 文件：

```yaml
services:
  film-fusion:
    volumes:
      - /srv/media:/mnt/media
      - /mnt/clouddrive:/mnt/clouddrive:ro
```

左侧是宿主机真实路径，右侧是容器内路径。后续在 FilmFusion 中填写的路径必须使用右侧路径；参与同一工作流的 Emby、CloudDrive2 等服务也要采用一致的路径映射。

## 四、校验并启动

先检查 Compose 配置，再拉取并启动主服务镜像：

```bash
docker compose config --quiet
docker compose pull film-fusion
docker compose up -d --no-build
```

这里对最后一步使用 `--no-build`，是为了确保主服务使用包含 Web 前端的发布镜像。只有准备从源码构建 FilmFusion 时，才需要本地构建主服务镜像。

查看启动结果：

```bash
docker compose ps
docker compose logs --tail=100 film-fusion
curl -fsS http://127.0.0.1:9000/api/public-config
```

正常情况下：

- `film-fusion` 状态为 `Up`。
- 最后一条命令返回登录页公开配置 JSON。

然后访问 `http://服务器IP:9000`，使用 `data/config.yaml` 中的管理员账户登录。

## 五、反向代理与 HTTPS

准备公网访问时，建议只让反向代理连接 FilmFusion，并由反向代理负责 HTTPS。以 Caddy 为例：

```caddyfile
film.example.com {
    reverse_proxy 127.0.0.1:9000
}
```

如果反向代理与 Docker 在同一台服务器，可把管理端口改为仅监听本机：

```yaml
ports:
  - "127.0.0.1:9000:9000"
```

手工修改 `data/config.yaml` 后重建主服务容器即可生效：

```bash
docker compose up -d --no-build --force-recreate film-fusion
```

如果还要通过域名访问 Emby 直链代理，需要另外为 `8097` 配置反向代理；否则请按实际客户端网络开放该端口。

反向代理后，`server.security.trusted_proxy_cidrs` 用于管理后台登录保护的真实来源 IP 解析。只应把实际反向代理的 IP 或最小网段加入其中。`emby.security.trusted_proxy_cidrs` 仍单独控制 Emby 登录保护，不要直接信任所有来源。

## 六、Webhook 地址

Webhook 与管理界面共用 `9000` 端口。使用域名时，常用地址为：

- Emby：`https://film.example.com/webhook/emby`
- CloudDrive2：`https://film.example.com/webhook/clouddrive2/file_notify`
- MoviePilot2：`https://film.example.com/webhook/movie-pilot/v2`

建议先在“系统设置 → Webhook”中为 CloudDrive2 生成独立 Token 并启用 Bearer Token 鉴权。Webhook Token 与管理员密码应使用不同的随机值。

## 七、升级

升级前先备份数据，然后执行：

```bash
git pull --ff-only
docker compose pull film-fusion
docker compose up -d --no-build --remove-orphans
docker compose ps
```

`docker compose pull film-fusion` 会拉取 `docker-compose.yml` 中指定的镜像版本；仓库更新了版本号后，先执行 `git pull` 才能获取该版本。

升级完成后检查日志：

```bash
docker compose logs --tail=100 film-fusion
```

## 八、备份与恢复

需要备份：

- `data/`：配置、SQLite 数据库、JWT 密钥、日志和上传资源。
- 自己创建的 `docker-compose.override.yml`。

为了得到一致的 SQLite 备份，先短暂停止服务：

```bash
docker compose stop
tar -czf ../film-fusion-backup-$(date +%F-%H%M%S).tar.gz data docker-compose.override.yml
docker compose start
```

如果没有 `docker-compose.override.yml`，请从 `tar` 命令中删去该文件名。备份包含密码和访问密钥，必须放在受保护的位置。

恢复时应先停止服务，把备份中的文件恢复到同一目录，再执行 `docker compose up -d --no-build`。建议使用与备份时相同的 FilmFusion 镜像版本完成首次恢复启动。

## 九、常用命令

```bash
docker compose ps
docker compose logs -f film-fusion
docker compose restart film-fusion
docker compose stop
docker compose start
docker compose down
```

`docker compose down` 只删除容器和 Compose 网络，不会删除当前使用的 `./data` 绑定目录。不要额外添加 `-v`，也不要手工删除 `data/`。

## 十、常见问题

### FilmFusion 容器反复重启

先查看日志：

```bash
docker compose logs --tail=200 film-fusion
```

最常见原因是 `data/config.yaml` 中管理员用户名或密码为空、YAML 缩进错误，或者 `data/` 无法写入。

### 页面能打开，但连接不到 Emby

不要在容器部署中使用 `http://127.0.0.1:8096` 指向宿主机 Emby。改用 Emby 的局域网 IP、同一 Docker 网络中的服务名，或自行配置 `host.docker.internal`。

### 本地构建主服务时提示找不到 `dist`

发布镜像已经包含 Web 前端；普通部署请使用本文的 `pull` + `up --no-build` 流程。源码构建只需准备新版 `film-fusion-admin`，在后端运行 `./build-frontends.sh` 生成完整 `dist/` 后再构建镜像，详见下方说明。

### 修改配置后没有生效

部分系统设置可热更新，端口、代理启用状态和部分日志配置需要重启：

```bash
docker compose restart film-fusion
```

## 安全清单

- 管理员密码与 Webhook Token 使用不同的随机值。
- 公网部署使用 HTTPS，不直接暴露管理端口。
- 只向可信代理开放 `trusted_proxy_cidrs`。
- 定期备份 `data/` 和 Compose 覆盖文件。
- 不要把 `data/config.yaml`、数据库或备份提交到 Git。

## 源码构建主服务（可选）

只有需要部署尚未发布的代码时才需要这一步。构建机器还需安装 Node.js 22.12+ 与 pnpm 10+，并准备两个兄弟目录：

```text
FilmFusion/
├── film-fusion/           # 本仓库
└── film-fusion-admin/     # AdminBase 新版前端源码
```

在后端仓库执行：

```bash
./build-frontends.sh
docker compose build --pull film-fusion
docker compose up -d
```

`build-frontends.sh` 会在依赖缺失时安装依赖，只构建 `film-fusion-admin`，将其 `apps/web/dist/` 完整替换到后端 `dist/`，不保留旧版文件或 `dist/v2/`。构建和拷贝均成功后才替换旧产物；它不构建镜像、不推送、不启动后端。源码在其他位置时，用 `ADMIN_FRONTEND_DIR` 指定绝对路径，不再使用 `FRONTEND_DIR`。

原有镜像发布命令 `./build.sh <版本号>` 已接入唯一新版前端构建，默认构建并推送版本标签和 `latest`；加 `--no-push` 仅构建镜像。Dockerfile 会把整个 `dist/` 放入镜像，Go 编译仍在 builder 阶段完成，宿主机不需要安装 Go。

升级到此版本后，根路径 `/` 直接打开新版，旧前端不再发布；API 仍为 `/api`，账号、数据库及配置继续沿用，不需要第二个端口。历史 `/v2` 和 `/v2/…` 地址跳转到对应根路径，并保留查询参数。必须一起发布后端路由改动和根路径构建的新前端产物。

使用本指南的 Caddy 整站反向代理即可访问新版。已有 Nginx 等代理也应把整站请求原样转发到 `9000`，由后端处理静态资源、页面回退及历史地址跳转。升级后检查 `/`、`/login`、任一业务页面刷新，以及 `/v2/`、`/v2/login` 的跳转均正确；缺失的 JS/CSS 应返回 404。
