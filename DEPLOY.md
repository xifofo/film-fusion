# Film Fusion 部署文档

> 一个功能强大的媒体文件管理与自动化处理服务，专为家庭媒体服务器设计。
> Web 管理界面 + Emby 直链代理 + 115 网盘 + STRM + 观看统计。

- 镜像（Docker Hub）：`kumayi/film-fusion`
- 源码（GitHub）：`https://github.com/xifofo/film-fusion`

---

## ✨ 功能特性

- 🎬 **STRM 文件管理** — 自动生成与管理 STRM 流媒体文件
- 📺 **Emby 集成与直链代理** — 反代 Emby 播放，按账号走指定 115 存储直链
- ☁️ **115 网盘集成** — 扫码登录、下载、直链播放，支持多账号
- 🧩 **Match302 重定向** — 源/子账号池负载均衡 + 秒传缓存与自动清理
- 🖼️ **媒体库封面生成** — 自动拼接海报生成媒体库封面（支持定时）
- 🔎 **Emby 缺集扫描** — 扫描缺失剧集并支持重生成 STRM、外部链接查询
- 📊 **观看记录统计** — 按 Emby 用户隔离的观看数据：总览 / 画廊海报墙 / 日历 / 记录 / 年度报告（含分享图）
- 🔗 **CloudDrive2 / MoviePilot2 集成** — Webhook 通知联动
- 🌐 **Web 管理界面** — 直观的配置与管理界面
- ⚙️ **系统设置在线编辑** — 在网页上编辑 `config.yaml`，多数项保存即时生效（无需重启）
- 🔐 **JWT 认证** — 安全的用户认证

---

## 📦 端口与目录

| 端口 | 用途 | 对应配置 |
| ---- | ---- | -------- |
| `9000` | Web 管理界面 / API / **Webhook** | `server.port` |
| `8097` | Emby 直链播放代理 | `emby.run_proxy_port` |

| 容器路径 | 用途 |
| -------- | ---- |
| `/app/data` | 配置文件 `config.yaml`、SQLite 数据库、日志、字体等持久化数据 |

> 数据全部位于 `/app/data`，只需挂载这一个目录即可持久化。

---

## 🚀 快速部署（Docker Compose，推荐）

1. 创建目录并下载所需文件：

```bash
mkdir -p film-fusion/data && cd film-fusion
curl -O https://raw.githubusercontent.com/xifofo/film-fusion/main/docker-compose.yml
curl -o data/config.yaml https://raw.githubusercontent.com/xifofo/film-fusion/main/data/config.example.yaml
```

2. 编辑 `data/config.yaml`，**至少修改以下项**：

```yaml
server:
  password: "你的管理员密码"          # 初始登录密码
jwt:
  secret: "一段足够长的随机密钥"       # JWT 签名密钥，务必修改
emby:
  url: "http://你的Emby:8096"        # Emby 服务器地址
  api_key: "你的 Emby API Key"        # Emby → 设置 → 高级 → API 密钥
```

3. `docker-compose.yml` 示例（已内置，可按需调整挂载）：

```yaml
services:
  film-fusion:
    image: "kumayi/film-fusion:latest"
    container_name: "film-fusion"
    restart: unless-stopped
    ports:
      - "9000:9000"   # Web / API / Webhook
      - "8097:8097"   # Emby 直链代理
    volumes:
      - ./data:/app/data
      # 如使用本地媒体路径，可按需挂载媒体目录：
      # - /path/to/media:/mnt/media
    environment:
      - TZ=Asia/Shanghai
      - GIN_MODE=release
```

4. 启动：

```bash
docker compose up -d
```

5. 浏览器访问 `http://服务器IP:9000`，用 `config.yaml` 中的用户名/密码登录。

---

## 🐳 快速部署（docker run）

```bash
docker run -d \
  --name film-fusion \
  --restart unless-stopped \
  -p 9000:9000 \
  -p 8097:8097 \
  -v "$(pwd)/data:/app/data" \
  -e TZ=Asia/Shanghai \
  -e GIN_MODE=release \
  kumayi/film-fusion:latest
```

> 首次启动若 `data/config.yaml` 不存在，请先放入由 `config.example.yaml` 改写的配置文件。

---

## ⚙️ 配置说明（config.yaml）

```yaml
server:
  port: 9000                     # Web / API / Webhook 端口
  username: "admin"              # 初始管理员用户名
  password: ""                   # 初始管理员密码（务必修改）
  download_115_concurrency: 1    # 115 下载并发数
  process_new_media: false       # 是否处理新增媒体事件（Emby webhook）

emby:
  enabled: true                  # 启用 Emby 代理
  url: "http://127.0.0.1:8096"   # Emby 地址
  run_proxy_port: 8097           # Emby 直链代理端口
  api_key: ""                    # Emby API Key
  admin_user_id: ""              # Emby 管理员用户 ID（用于预取下一集等）
  cache_time: 30                 # 直链缓存时间（分钟）
  add_current_media_info: true   # 播放时预热当前媒体直链
  add_next_media_info: true      # 预取下一集（需 admin_user_id）
  cover:                         # 媒体库封面生成器
    enabled: false
    cron: ""                     # 如 "0 3 * * *"；为空则仅手动
    width: 1920
    height: 1080
    jpeg_quality: 88
    poster_count: 9
    font_cn: "data/assets/fonts/SourceHanSansCN-Bold.otf"
    font_en: "data/assets/fonts/Inter-Bold.ttf"

moviepilot:
  api: "http://127.0.0.1:3001"
  username: ""
  password: ""

log:
  level: info                    # debug / info / warn / error / fatal
  format: json                   # json / text
  output: file                   # stdout / file
  max_size: 100                  # MB
  max_backups: 3
  max_age: 28                    # 天
  compress: true

jwt:
  secret: "请修改为随机长字符串"  # JWT 密钥（务必修改）
  expire_time: 240               # Token 过期时间（小时）
  issuer: "film-fusion"
```

> 获取 Emby API Key：登录 Emby → 设置 → 高级 → API 密钥 → 新建。

---

## 🛠️ 在线系统设置（无需手改文件）

进入 Web 界面「**系统设置**」即可在线编辑 `config.yaml`，密钥类字段留空表示不修改。

- **保存即时生效**：Emby 连接（地址 / API Key / 管理员 ID）、新媒体与播放开关、封面参数与定时 cron、MoviePilot、JWT 密钥（会使已登录会话失效）、**日志级别**。
- **需重启生效**（界面会在对应字段标注「需重启」）：HTTP 端口、Emby 代理端口、Emby 代理启用开关、日志格式 / 输出 / 轮转、115 下载并发数。

---

## 🔔 Webhook 配置

Webhook 与 Web 界面同端口（默认 `9000`），地址形如 `http://服务器IP:9000/webhook/...`：

- **Emby**：通知中添加 Webhook，地址 `http://服务器IP:9000/webhook/emby`
  - 入库补充媒体信息：勾选「新媒体已添加」
  - 观看记录采集：勾选「播放停止 / 标记已播放」等事件
- **CloudDrive2**：`http://服务器IP:9000/webhook/clouddrive2/file_notify`，并将 `enabled` 设为 `true`
- **MoviePilot2**：添加 Webhook 插件（`POST`），地址 `http://服务器IP:9000/webhook/movie-pilot/v2`

---

## ⬆️ 升级

```bash
docker compose pull && docker compose up -d
# 或 docker run 方式：
# docker pull kumayi/film-fusion:latest && 重新创建容器
```

---

## 🧰 常用命令

```bash
docker compose ps                    # 查看状态
docker compose logs -f film-fusion   # 实时日志
docker compose restart               # 重启
docker compose down                  # 停止并移除容器
```

---

## 🔍 故障排查

- **无法访问 Web 界面**：`docker compose ps` 看容器是否运行；确认 `9000` 端口未被占用、未被防火墙拦截。
- **Emby 直链播放异常**：确认 `8097` 端口已映射、`emby.url` / `api_key` 正确；查看日志。
- **115 下载失败**：检查授权是否过期；适当降低 `download_115_concurrency`。
- **年报分享图生成失败**：需配置中文字体 `emby.cover.font_cn`（镜像内置默认字体路径）。

---

## 🔐 安全建议

1. 首次部署后立即修改默认密码与 JWT 密钥。
2. 对外暴露时建议使用反向代理 + HTTPS。
3. 定期备份 `data` 目录（含数据库与配置）。

---

## 📄 开源协议

本项目基于 MIT 协议开源。

**Film Fusion** — 让媒体管理变得简单高效 🎬✨
