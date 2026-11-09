# Film Fusion

一个功能强大的媒体文件管理和自动化处理服务，专为家庭媒体服务器设计。

## ✨ 主要功能

- 🎬 **STRM 文件管理** - 自动生成和管理 STRM 流媒体文件
- 📺 **Emby 集成** - 完整的 Emby 服务器代理和直链播放支持
- ☁️ **115网盘集成** - 支持 115网盘文件下载和直链播放
- 🔗 **CloudDrive2 集成** - 支持 CloudDrive2 Webhook 通知
- 🌐 **Web 管理界面** - 直观的 Web 界面进行配置和管理
- 🖼️ **登录页定制** - 支持上传并持久化登录页背景、配置品牌及备案信息
- 🔐 **JWT 认证** - 安全的用户认证系统
- 🔄 **Webhook 支持** - 支持 CloudDrive2 和 MoviePilot2 的 Webhook 通知
- 🐟 **[PlutoFish 通知](docs/plutofish-notifications.md)** - 主动补收与 SSE 订阅转存结果，持久化恢复及本地 STRM 处理，无需公网回调

- 🗂️ **[媒体目录](docs/media-directories.md)** - 统一路径映射与媒体整理，自动迁移旧配置及关联

## 🚀 开箱即用教程（v4）

下面从全新部署开始，依次完成后台登录、添加 115、生成第一个 STRM 和接入 Emby。本文使用 `kumayi/film-fusion:v4.0.4`（2026-11-09 核对的已发布版本），镜像版本可在 [Docker Hub](https://hub.docker.com/r/kumayi/film-fusion/tags) 查看。发布镜像已包含管理界面，无需安装 Go、Node.js 或单独部署前端。

### 1. 准备环境和配置文件

准备一台 Linux 服务器、NAS 或安装了 Docker Desktop 的电脑，安装 Git 和 Docker Compose v2。Docker 的安装方式见 [官方说明](https://docs.docker.com/compose/install/)。在终端确认：

```bash
docker --version
docker compose version
git --version
```

在准备存放应用的目录执行：

```bash
git clone --depth 1 https://github.com/xifofo/film-fusion.git
cd film-fusion
cp data/config.example.yaml data/config.yaml
mkdir -p strm
```

`data/` 保存配置、数据库、登录密钥和上传资源；`strm/` 保存稍后生成的 STRM 文件。已有部署请保留原来的 `data/`，不要重新复制示例覆盖配置；升级方法见 [自建服务器部署指南](DEPLOY.md#七升级)。

### 2. 设置管理员账号

用文本编辑器打开 `data/config.yaml`，修改已有的对应字段，保留其余配置；不要在文件末尾重复添加同名配置段：

```yaml
server:
  port: 9000
  username: "admin"
  password: "请替换为你自己的强密码"  # 必须修改，不能为空
emby:
  enabled: false                    # 完成后台登录后再配置 Emby
log:
  output: stdout                    # 方便通过 docker compose logs 查看日志
```

管理员账号来自此配置，程序启动时会同步到数据库。忘记密码时也可修改这里的密码后重启服务。

### 3. 挂载 STRM 保存目录

在 `docker-compose.yml` 旁新建 `docker-compose.override.yml`，填入：

```yaml
services:
  film-fusion:
    volumes:
      - ./strm:/strm
```

Compose 会自动合并这两个文件，保留主文件中的 `./data:/app/data` 挂载。左侧 `./strm` 是宿主机目录，右侧 `/strm` 是 FilmFusion 容器内目录，后台的「STRM 保存路径」应填写 `/strm`。

如果已有媒体目录，可以将左侧换成实际的绝对路径，例如 `/srv/media-strm:/strm`。FilmFusion 需要对该目录有写入权限。

### 4. 启动并登录后台

以下命令均在 `film-fusion` 目录执行：

```bash
docker compose config --quiet
docker compose pull film-fusion
docker compose up -d --no-build
docker compose ps
docker compose logs --tail=100 film-fusion
```

容器状态应为 `Up`。在运行 Docker 的主机上检查后台接口：

```bash
curl -fsS http://127.0.0.1:9000/api/public-config
```

返回登录页配置 JSON 后，在浏览器访问 `http://服务器IP:9000`，使用刚才设置的用户名和密码登录；在本机部署时可使用 `http://localhost:9000`。

| 端口 | 用途 | 访问方式 |
| --- | --- | --- |
| `9000` | FilmFusion 后台、API、Webhook | 浏览器管理入口 |
| `8097` | FilmFusion 的 Emby 代理 | 完成第 8 步后供播放器连接 |
| `8096` | 现有 Emby 的原始端口（常见默认值） | 作为 FilmFusion 的上游地址 |

端口被占用时，可将主 Compose 文件中的 `9000:9000` 改成 `19000:9000`，然后用 `http://服务器IP:19000` 访问；只改左侧宿主机端口即可，容器内 `server.port` 仍为 `9000`。修改 Compose 后重新执行 `docker compose up -d --no-build`。

### 5. 添加 115 网盘

1. 打开「云存储管理」，点击「添加 115 网盘」。
2. 从应用列表选择可用应用，或输入已有的应用 ID，点击「扫码授权」。
3. 使用 115 手机客户端扫码并确认，等待授权结果成功保存、账号出现在列表中。
4. 仅使用这次 OpenAPI 授权时，后面的目录选择和网盘浏览均选择 **OpenAPI**；Cookie 是另一套凭据，不会因为完成 OpenAPI 授权就自动具备。

账号处于启用状态后继续。授权过期可选中账号执行「重新授权」。需要 Cookie 功能时，在该账号的 Cookie 配置中补充凭据。

### 6. 创建媒体目录

以下用网盘文件 `/电影/示例影片.mkv` 举例；请换成自己的真实目录和文件。示例的播放路径前缀为 `/CloudNAS/115`，对应 Emby 中可读取的 115 挂载根目录；已有 CloudDrive2 等挂载时，按实际路径替换。

打开「媒体目录」→「创建媒体目录」，填写：

| 位置 / 字段 | 示例值 | 说明 |
| --- | --- | --- |
| 基本信息 → 云存储 | 刚添加的 115 账号 | 文件实际所在账号 |
| 基本信息 → 目录名称 | `电影` | 本例中也是输出目录名 |
| 基本信息 → 目录读取方式 | `OpenAPI` | 与已有凭据对应 |
| 基本信息 → 云盘目录 | 选择网盘中的 `/电影` | 使用选择器回填真实目录 ID，不填本地路径 |
| STRM 同步 → 云盘源路径 | 留空 | 本次先手动生成，暂不接收目录同步事件 |
| STRM 同步 → STRM 保存路径 | `/strm` | FilmFusion 容器内的可写目录 |
| STRM 同步 → STRM 内容前缀 | `/CloudNAS/115` | 指向原始媒体的播放路径根目录 |
| STRM 同步 → URI 编码 | 关闭 | 本例使用本地路径 |
| 整理文件过滤 → 包含后缀 | `mkv,mp4` | 按实际文件后缀调整 |
| 整理文件过滤 → 排除小于（MB） | `0` | 首次验证不按大小排除 |

其他选项保持默认并保存。填写云盘目录后，该配置可用于媒体整理和网盘浏览；**保存配置本身不会移动网盘文件，也不会自动生成 STRM**。本例无需配置洗版、自动化或 TMDB。

### 7. 生成第一个 STRM

选中刚创建的媒体目录，打开「浏览网盘」，选择 `OpenAPI` 并点击「加载目录」。选中一个视频文件，点击「生成 STRM」并确认。

以上例为准，成功后应得到：

| 位置 | 路径或内容 |
| --- | --- |
| 115 原始文件 | `/电影/示例影片.mkv` |
| FilmFusion 容器内生成文件 | `/strm/电影/示例影片.strm` |
| 宿主机生成文件 | `film-fusion/strm/电影/示例影片.strm` |
| `.strm` 文件中的一行内容 | `/CloudNAS/115/电影/示例影片.mkv` |

可在宿主机打开 `.strm` 文本文件核对。STRM 只保存播放地址，不包含视频本体；保存目录和文件内容指向的媒体目录是两回事。

单文件验证通过后，再使用「生成当前目录 STRM」批量生成。该操作递归读取当前目录及子目录，可在任务面板查看进度、失败原因或停止；只完成手动生成时，后续新增文件不会自动同步。自动同步需另外配置 [媒体目录](docs/media-directories.md) 的源路径与下方 Webhook，或配置 [自动化](docs/automation-115-openapi.md)。

### 8. 接入 Emby 并验证播放（可选）

需要先有一台可用的 Emby。本例采用本地挂载路径作为 STRM 内容，Emby 也应能读取 `/CloudNAS/115/电影/示例影片.mkv`；如果实际挂载路径不同，先修正上一节的内容前缀并重新生成 STRM。

**连接上游 Emby：** 在「系统设置 → Emby」启用代理，填写 Emby 的实际地址（例如 `http://192.168.1.10:8096`）、API Key 和代理端口 `8097`。API Key 在 Emby 管理后台的「设置 → 高级 → API 密钥」创建。需要提前获取下一集信息时填写 Emby 管理员用户 ID，否则关闭该功能。

上游地址必须能从 FilmFusion 容器访问，不能填写 FilmFusion 自身的 `8097`，也不要用容器内的 `localhost` 指代宿主机。保存后，启用开关和代理端口需要重启才能生效：

```bash
docker compose restart film-fusion
```

**让 Emby 读取 STRM：** 将宿主机上的 `film-fusion/strm` 目录提供给 Emby。若 Emby 也在 Docker 中，可在它自己的 Compose 服务配置中添加或合并下面的 `volumes`，再重建 Emby 容器；将左侧替换为第 1 步目录的真实绝对路径：

```yaml
volumes:
  - /path/to/film-fusion/strm:/strm:ro
```

然后在 Emby 中创建或编辑媒体库，将 `/strm/电影` 加入媒体库并扫描。如果 Emby 在另一台主机，需先通过共享目录让它读取同一批 STRM 文件。

**配置直链路径映射：** 在 FilmFusion 的「Match302 路径映射」中点击「新建路径映射」，按本例填写：

| 字段 | 值 |
| --- | --- |
| 云存储 | 文件所在的 115 账号 |
| Emby 文件路径前缀 | `/CloudNAS/115` |
| 网盘对应目录 | `/`（根目录，也可留空） |

这条规则将 STRM 内容 `/CloudNAS/115/电影/示例影片.mkv` 对应到网盘的 `/电影/示例影片.mkv`。这里匹配的是原始媒体路径，不是保存 STRM 的 `/strm` 路径。单账号起步时无需启用负载均衡或添加子账号池。

最后，将播放器中的 Emby 服务器地址设为 `http://FilmFusion服务器IP:8097`，使用 **Emby 账号**登录，打开刚扫描的影片，确认能播放并拖动进度。`9000` 是 FilmFusion 后台，直接连接上游 `8096` 则不会经过 FilmFusion 代理。

到这里，后台登录、网盘授权、STRM 生成、媒体库扫描和代理播放应分别验证成功。需要 HTTPS、备份恢复或源码构建时，继续阅读 [自建服务器部署指南](DEPLOY.md)。

### Web 管理界面

新版 AdminBase 前端是唯一发布界面，使用现有 `9000` 端口：

| 地址 | 用途 |
| --- | --- |
| `http://localhost:9000/` | 新版管理界面 |
| `http://localhost:9000/login` | 新版登录页 |

继续使用现有 API、管理员账号、数据库及业务配置。旧 `/v2` 和 `/v2/…` 地址会跳转到对应根路径地址并保留查询参数；深层页面支持直接打开与刷新。反向代理应将整站请求原样转发到同一服务。

源码发布时，在本仓库运行 `./build-frontends.sh`，仅构建兄弟目录 `film-fusion-admin` 并完整替换 `dist/`；不再需要或打包 `film-fusion-frontend`，产物中不保留旧版文件或 `dist/v2/`。`./build.sh <版本号>` 会先完成这一步，再构建并推送镜像，详见 [源码构建说明](DEPLOY.md#源码构建主服务可选)。

## ⚙️ 配置说明

### 基础配置

```yaml
server:
  port: 9000                        # Web界面端口
  username: "admin"                 # 初始管理员用户名
  password: "your-secure-password"  # 初始管理员密码
  download_115_concurrency: 2       # 115网盘下载并发数
  cookie_115_default_app: "alipaymini" # 仅首次初始化数据库时导入
  web_115_user_agent: ""            # 仅首次初始化数据库时导入

jwt:
  expire_time: 240                  # Token过期时间（小时）
```

JWT 签名密钥由程序自动生成并保存在数据目录中，无需手工配置。
系统设置的「Emby → 媒体库封面」可选择电脑上的 TTF/OTF 字体文件（单个不超过 32 MiB）。字体上传至持久化目录 `data/uploads/fonts/`，经渲染器解析校验后回填服务器路径；保存 Emby 设置后才会启用。上传接口 `POST /api/site-assets/fonts` 仅供管理员登录会话使用，不开放公开字体下载。
115 默认 App 与浏览器 UA 首次从上述 YAML 导入，之后只保存在数据库并通过系统设置维护；UA 暂未接入请求。

通知统一在「系统设置 → 通知」中管理。Telegram 与通用 JSON Webhook 可独立启用，Emby/FilmFusion 登录爆破、自动化事件触发和 115 Cookie 失效均可分别选择一个或多个投递渠道；旧版顶层 `telegram` 配置会自动导入。

自动化中心支持监控 115 OpenAPI 目录。首次扫描只建立存量基线，之后按目录增量检查；新增媒体稳定后可执行 medianame 媒体识别、媒体整理、STRM 生成和通知。

### Emby 集成配置
```yaml
emby:
  enabled: true                     # 启用Emby集成
  url: "http://192.168.1.10:8096"   # Emby服务器地址；容器中不要用 localhost 指向宿主机
  run_proxy_port: 8097             # 代理服务端口
  api_key: "your-emby-api-key"     # Emby API密钥
  admin_user_id: "user-id"         # Emby管理员用户ID
  cache_time: 30                   # 缓存时间（分钟）
  bound_account_wait_seconds: 10   # 指定账号秒传等待上限，1–20 秒；超时后按正常路由回退
```

可在「系统设置 → Emby」修改「指定账号等待超时（秒）」，默认 10 秒，保存后对新的播放请求立即生效。超时回退日志会保留指定账号等待超时及实际等待上限。

**获取 Emby API 密钥：**
1. 登录 Emby → 设置 → 高级 → API 密钥
2. 创建新密钥，输入应用名称
3. 复制生成的密钥到配置文件

## 🔗 Webhook 集成（按需）
配置第三方服务的 Webhook 地址：
#### **CloudDrive2**
FilmFusion 会始终接收 CloudDrive2 Webhook。建议先在「系统设置 → Webhook」生成并保存独立 Token、开启 Bearer Token 鉴权，再在 CloudDrive2 添加 webhook，修改服务地址并启用：
```toml
base_url = "http://xxx.xxx.xxx.xxx:9000/webhook/clouddrive2/file_notify"
# Whether the webhook is enabled
enabled = true

[global_params.default_headers]
content-type = "application/json"
authorization = "Bearer <FilmFusion 中生成的 Token>" # FilmFusion 开启鉴权时必填
```

不要复用 FilmFusion 管理后台密码。公网或不可信网络部署时，请在 HTTPS 反向代理或 VPN 后使用 Webhook。

#### **MoviePilot2**:
添加 webhook 插件 选择 `POST` 填入以下链接
`http://xxx.xxx.xxx.xxx:9000/webhook/movie-pilot/v2`

#### EMBY 入库补充 媒体信息
EMBY 通知添加 webhook 勾选 新媒体已添加，填入链接

`http://xxx.xxx.xxx.xxx:9000/webhook/emby`


## 🛠️ 常用命令

```bash
# 查看服务状态
docker compose ps

# 查看实时日志
docker compose logs -f film-fusion

# 重启服务
docker compose restart

# 更新应用（先备份 data/ 和 docker-compose.override.yml）
git pull --ff-only
docker compose pull film-fusion
docker compose up -d --no-build --remove-orphans

# 停止服务
docker compose down
```

## 🔍 故障排除

### Emby 绑定播放诊断

在“系统设置 → 日志”将级别设为 `debug`，复现一次播放后，在“运行日志”搜索 `[EMBY PLAYBACK]`。同一次播放的 `trace` 相同，可继续按完整 `trace` 查找；本次增加日志不会自动调整服务器日志级别。

- `user_identity`：`login_token` 表示命中真实登录响应记录的 Token → UserId；`device` / `device_cache` 表示按用户 Token 和 DeviceId 查询或短时复用设备会话。服务器地址或 API Key 变化时缓存隔离，退出登录或收到 401 时清除该 Token 的映射。
- `user_session_http` / `user_session_match`：`/Sessions?DeviceId=…` 的 HTTP 状态、会话数量及设备匹配结果。不会再调用 `/Users/Me`；同设备有不同用户时标记 `ambiguous`，缺少设备 ID 或无法识别时不缓存空结果，也不按影片、共享 IP 或客户端自报 UserId 猜测身份。
- `binding` / `match302`：是否命中启用的绑定、绑定账号 ID、路径规则 ID 和负载均衡开关。媒体信息直接返回外部 URL 时会标记 `external_media_url`，这条路径不会进入绑定决策。
- `bound_storage`：源账号/绑定账号的凭据可用性（仅布尔值）、Cookie 是否缺失、并发是否已满；`binding_fallback` 说明转入普通流程。
- `bound_assignment`：绑定转存任务 ID、等待后的状态和目标文件凭据是否存在；后台任务可继续用 `assignment` ID 对照已有转存日志及队列错误详情。
- `account_decision` / `download_url` / `result`：计划及最终使用的账号 ID、直链获取结果、是否回退默认反代。`redirect_ready=true` 只说明已取得重定向地址，不代表播放器完成播放。

新增的 `[EMBY PLAYBACK]` 日志不输出 Token、Cookie、密码、完整 URL、签名参数或上游响应体，仅记录状态码、内部 ID 和固定原因。绑定仍先于负载均衡开关处理；秒传等待及播放兜底逻辑保持原样。排查结束后可恢复原日志级别。

成功登录需经过 FilmFusion 的 Emby 代理才能记录 Token 与用户的对应关系；已有登录可通过设备会话识别。设备会话只短时缓存到 Token + DeviceId，不作为长期 Token 归属证明。识别到用户后仍按其 UserId 查询已启用的存储绑定；关闭 Match302 负载均衡也可以使用绑定账号。

### 常见问题

**Q: 无法访问Web界面**
```bash
# 检查服务状态和端口占用
docker compose ps
sudo netstat -tlnp | grep 9000
```

**Q: 115网盘下载失败**

- 检查 Access Token 是否过期
- 降低 download_115_concurrency 配置

**Q: 启动后容器反复重启**

- 查看 `docker compose logs --tail=100 film-fusion`，确认管理员用户名、密码非空，YAML 缩进正确，`data/` 可写。
- 若沿用文件日志配置，可在后台「运行日志」或宿主机 `data/` 内的日志文件查看详细错误。

**Q: 浏览网盘提示缺少 Cookie**

- OpenAPI 扫码授权与 Cookie 凭据独立；仅做过扫码授权时，将目录读取方式和网盘浏览方式切换为 `OpenAPI`。

**Q: 保存媒体目录后没有 STRM 文件**

- 保存配置不会触发生成。先在「浏览网盘」中选择文件并执行「生成 STRM」。
- 核对保存路径使用容器内路径、挂载目录可写，并查看文件后缀及大小过滤、生成结果中的跳过原因。

**Q: Emby 已看到影片，但不能播放或没有走直链**

- 检查 STRM 内容是否对应真实媒体路径、Emby 是否能读取该路径，以及 Match302 映射是否对应正确的 115 账号和网盘目录。
- 确认播放器连接的是 FilmFusion 的 `8097`，Emby 代理已启用并重启生效；再检查该账号的直链方式和凭据。

## 🔐 安全建议

1. **修改默认密码** - 首次部署后立即修改
2. **保护数据目录** - JWT密钥由程序生成并保存在 `data` 中
3. **启用HTTPS** - 使用反向代理配置SSL
4. **定期备份** - 备份配置文件和数据库

## 📄 开源协议

本项目基于 [MIT 协议](LICENSE) 开源发布。

---

**Film Fusion** - *让媒体管理变得简单高效* 🎬✨
