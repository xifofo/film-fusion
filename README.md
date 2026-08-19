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

## 🚀 快速部署

推荐使用 Docker Compose。完整的服务器准备、目录挂载、HTTPS、升级、备份与故障排查说明见 [自建服务器部署指南](DEPLOY.md)。

### Docker Compose 部署

1. **克隆仓库并创建本地配置**

```bash
git clone --depth 1 https://github.com/xifofo/film-fusion.git
cd film-fusion
cp data/config.example.yaml data/config.yaml
```

2. **修改配置文件**

编辑 `data/config.yaml`，必须修改：

```yaml
server:
  password: "your-secure-password"  # 管理员密码
emby:
  enabled: false                     # 尚未配置 Emby 时先关闭
```

3. **修改挂载路径**

如需访问宿主机媒体目录，新建 `docker-compose.override.yml`：

```yaml
services:
  film-fusion:
    volumes:
      - /path/to/your/media:/mnt/media  # 修改为实际媒体路径
```

4. **启动服务**

```bash
docker compose config
docker compose pull film-fusion
docker compose up -d --no-build
```

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
115 默认 App 与浏览器 UA 首次从上述 YAML 导入，之后只保存在数据库并通过系统设置维护；UA 暂未接入请求。

通知统一在「系统设置 → 通知」中管理。Telegram 与通用 JSON Webhook 可独立启用，Emby/FilmFusion 登录爆破、RSS 命中和 115 Cookie 失效均可分别选择一个或多个投递渠道；旧版顶层 `telegram` 配置会自动导入。

RSS 自动化可直接订阅外部 RSS/Atom 地址；需要把不提供 Feed 的网站转换为 RSS 时，可使用独立部署的 RSSHub。

### Emby 集成配置
```yaml
emby:
  enabled: true                     # 启用Emby集成
  url: "http://192.168.1.10:8096"   # Emby服务器地址；容器中不要用 localhost 指向宿主机
  run_proxy_port: 8097             # 代理服务端口
  api_key: "your-emby-api-key"     # Emby API密钥
  admin_user_id: "user-id"         # Emby管理员用户ID
  cache_time: 30                   # 缓存时间（分钟）
```

**获取 Emby API 密钥：**
1. 登录 Emby → 设置 → 高级 → API 密钥
2. 创建新密钥，输入应用名称
3. 复制生成的密钥到配置文件

## 🎯 使用指南

### 首次使用
1. 访问 `http://localhost:9000` 进入管理界面
2. 使用配置文件中的用户名和密码登录

### 云存储配置
**115网盘：**
1. 进入"云存储管理" → "添加云存储"
2. 选择类型"115网盘"，扫码登录

### Webhook 集成
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

# 更新应用
git pull --ff-only
docker compose pull film-fusion
docker compose up -d --no-build --remove-orphans

# 停止服务
docker compose down
```

## 🔍 故障排除

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

## 🔐 安全建议

1. **修改默认密码** - 首次部署后立即修改
2. **保护数据目录** - JWT密钥由程序生成并保存在 `data` 中
3. **启用HTTPS** - 使用反向代理配置SSL
4. **定期备份** - 备份配置文件和数据库

## 📄 开源协议

本项目基于 [MIT 协议](LICENSE) 开源发布。

---

**Film Fusion** - *让媒体管理变得简单高效* 🎬✨
