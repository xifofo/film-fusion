# FilmFusion 自建服务器部署

本文档面向希望把 FilmFusion 部署到自己 Linux 服务器、NAS 或家用主机的用户。推荐使用 Docker Compose：FilmFusion 主服务使用发布镜像，RSS 生成器 Worker 从仓库源码构建，两者通过 Compose 内网通信。

## 部署结构

| 服务 | 端口 | 是否需要对外开放 | 说明 |
| --- | --- | --- | --- |
| `film-fusion` | `9000` | 是，或仅交给反向代理 | Web 管理界面、API、公开 RSS 和 Webhook |
| `film-fusion` | `8097` | 按需 | Emby 直链代理 |
| `rss-generator-worker` | `8787` | 否 | RSS 抓取与浏览器渲染，仅供 FilmFusion 内网调用 |

FilmFusion 的配置、SQLite 数据库、日志、登录会话密钥、RSS 加密密钥和上传资源都保存在宿主机的 `./data` 目录。RSS Worker 内部 Token 由管理员手工配置：Worker 从 Compose 环境变量读取，FilmFusion 从系统设置读取。

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

当前 Compose 包含 RSS Worker 的本地构建上下文，因此需要克隆仓库，不能只下载 `docker-compose.yml`：

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

### 2. RSS Worker Token 与公开地址

编辑 `docker-compose.yml`，为 Worker 填写至少 32 个字符的随机 Token：

```yaml
services:
  rss-generator-worker:
    environment:
      - WORKER_AUTH_TOKEN=请替换为至少32个字符的随机Token
```

Token 不会自动生成。启动完成后，登录 FilmFusion，进入“系统设置 → RSS 生成器”，填写与 `WORKER_AUTH_TOKEN` **完全相同**的值并保存。两边不一致时 Worker 会返回鉴权失败。

没有域名时保持公开地址为空：

```yaml
rss_generator:
  public_base_url: ""
```

使用 HTTPS 域名后可填写 `https://film.example.com`，不要添加结尾斜杠。

每个 Feed 有一组固定路径：

- 局域网：`/rss/{feedPublicID}.xml` 或 `/rss/{feedPublicID}.atom`，无需 Token。
- 公网：使用同一路径，并追加管理页生成的 `?token=...`。

这里的订阅 Token 与 RSS Worker 内部 Token 是两套独立凭证。前者只保护公网 Feed 访问，后者只用于 FilmFusion 调用 Worker。
FilmFusion 自身不会记录 `/rss/` 请求，但公网反向代理也应避免在访问日志中记录完整查询字符串，防止 `token` 被写入日志。

### 3. Emby 地址

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

### 4. 挂载媒体目录（按需）

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

先检查 Compose 配置，再分别拉取主服务镜像、构建 Worker：

```bash
docker compose config --quiet
docker compose pull film-fusion
docker compose build --pull rss-generator-worker
docker compose up -d --no-build
```

这里对最后一步使用 `--no-build`，是为了确保主服务使用包含 Web 前端的发布镜像。只有准备从源码构建 FilmFusion 时，才需要本地构建主服务镜像。

查看启动结果：

```bash
docker compose ps
docker compose logs --tail=100 film-fusion rss-generator-worker
curl -fsS http://127.0.0.1:9000/api/public-config
```

正常情况下：

- `film-fusion` 状态为 `Up`。
- `rss-generator-worker` 状态为 `Up (healthy)`。
- 最后一条命令返回登录页公开配置 JSON。

然后访问 `http://服务器IP:9000`，使用 `data/config.yaml` 中的管理员账户登录。
首次启动后还需要进入“系统设置 → RSS 生成器”，保存与 Compose 中 `WORKER_AUTH_TOKEN` 相同的 Token；保存后主服务立即使用，无需重启。

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

使用域名后，将 `data/config.yaml` 中的公开 RSS 地址设为同一个 HTTPS 来源：

```yaml
rss_generator:
  public_base_url: "https://film.example.com"
```

手工修改 `data/config.yaml` 后重建主服务容器即可生效：

```bash
docker compose up -d --no-build --force-recreate film-fusion
```

如果还要通过域名访问 Emby 直链代理，需要另外为 `8097` 配置反向代理；否则请按实际客户端网络开放该端口。不要把 RSS Worker 的 `8787` 端口映射到宿主机或公网。

反向代理后，`server.security.trusted_proxy_cidrs` 同时用于管理后台登录保护和 RSS 局域网免 Token 判定。只应把实际反向代理的 IP 或最小网段加入其中；FilmFusion 会从可信代理提供的 `X-Forwarded-For` 解析真实客户端 IP。未配置代理信任时，带转发头的请求不会获得局域网免 Token 权限。`emby.security.trusted_proxy_cidrs` 仍单独控制 Emby 登录保护，不要直接信任所有来源。

## 六、Webhook 地址

Webhook 与管理界面共用 `9000` 端口。使用域名时，常用地址为：

- Emby：`https://film.example.com/webhook/emby`
- CloudDrive2：`https://film.example.com/webhook/clouddrive2/file_notify`
- MoviePilot2：`https://film.example.com/webhook/movie-pilot/v2`

建议先在“系统设置 → Webhook”中为 CloudDrive2 生成独立 Token 并启用 Bearer Token 鉴权。Webhook Token、RSS Worker Token 和管理员密码应使用三份不同的随机值。

## 七、升级

升级前先备份数据，然后执行：

```bash
git pull --ff-only
docker compose pull film-fusion
docker compose build --pull rss-generator-worker
docker compose up -d --no-build --remove-orphans
docker compose ps
```

`docker compose pull film-fusion` 会拉取 `docker-compose.yml` 中指定的镜像版本；仓库更新了版本号后，先执行 `git pull` 才能获取该版本。

升级完成后检查日志：

```bash
docker compose logs --tail=100 film-fusion rss-generator-worker
```

## 八、备份与恢复

需要备份：

- `data/`：配置、SQLite 数据库、JWT/RSS 密钥、日志和上传资源。
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
docker compose logs -f rss-generator-worker
docker compose restart film-fusion
docker compose stop
docker compose start
docker compose down
```

`docker compose down` 只删除容器和 Compose 网络，不会删除当前使用的 `./data` 绑定目录。不要额外添加 `-v`，也不要手工删除 `data/`。

## 十、常见问题

### 系统信息页面提示 Worker Token 尚未设置或 Worker 鉴权失败

先检查 Worker：

```bash
docker compose ps rss-generator-worker
docker compose logs rss-generator-worker
docker compose up -d --no-build --force-recreate rss-generator-worker
```

确认 Compose 的 `WORKER_AUTH_TOKEN` 非空且至少 32 个字符，再到“系统设置 → RSS 生成器”重新填写完全相同的值。修改 Compose 环境变量后必须重建 Worker 容器；只修改 FilmFusion 设置无需重启。

### FilmFusion 容器反复重启

先查看日志：

```bash
docker compose logs --tail=200 film-fusion
```

最常见原因是 `data/config.yaml` 中管理员用户名或密码为空、YAML 缩进错误，或者 `data/` 无法写入。

### 页面能打开，但连接不到 Emby

不要在容器部署中使用 `http://127.0.0.1:8096` 指向宿主机 Emby。改用 Emby 的局域网 IP、同一 Docker 网络中的服务名，或自行配置 `host.docker.internal`。

### RSS Worker 不健康

```bash
docker compose logs --tail=200 rss-generator-worker
docker compose build --pull --no-cache rss-generator-worker
docker compose up -d --no-build rss-generator-worker film-fusion
```

Worker 镜像包含 Chromium 及其依赖，首次构建下载量较大。不要为它发布 `8787` 端口。

### 本地构建主服务时提示找不到 `dist`

发布镜像已经包含 Web 前端；普通部署请使用本文的 `pull` + `up --no-build` 流程。源码构建则需要先在兄弟目录 `film-fusion-frontend` 中执行 `pnpm build`，再把前端 `dist/` 内容复制到后端仓库的 `dist/` 后构建镜像。

### 修改配置后没有生效

部分系统设置可热更新，端口、代理启用状态和部分日志配置需要重启：

```bash
docker compose restart film-fusion
```

## 安全清单

- 管理员密码、Webhook Token 与 RSS Worker Token 分别使用独立随机值；Worker Token 至少 32 个字符，并在 Compose 与 FilmFusion 设置页保持一致。
- 公网部署使用 HTTPS，不直接暴露管理端口。
- 不对外映射 RSS Worker 的 `8787` 端口。
- 只向可信代理开放 `trusted_proxy_cidrs`。
- 定期备份 `data/` 和 Compose 覆盖文件。
- 不要把 `data/config.yaml`、数据库或备份提交到 Git。

## 源码构建主服务（可选）

只有需要部署尚未发布的代码时才需要这一步。服务器还需安装 Node.js 20.19+ 或 22.12+ 与 pnpm：

```bash
cd ..
git clone --depth 1 https://github.com/xifofo/film-fusion-frontend.git
cd film-fusion-frontend
pnpm install --frozen-lockfile
pnpm build
mkdir -p ../film-fusion/dist
cp -R dist/. ../film-fusion/dist/
cd ../film-fusion
docker compose build --pull film-fusion rss-generator-worker
docker compose up -d
```

后端 Go 编译在 Dockerfile 的 builder 阶段完成，宿主机不需要安装 Go。
