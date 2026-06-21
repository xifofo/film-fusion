# Film Fusion

一个功能强大的媒体文件管理和自动化处理服务，专为家庭媒体服务器设计。

## ✨ 主要功能

- 🎬 **STRM 文件管理** - 自动生成和管理 STRM 流媒体文件
- 📺 **Emby 集成** - 完整的 Emby 服务器代理和直链播放支持
- ☁️ **115网盘集成** - 支持 115网盘文件下载和直链播放
- 🔗 **CloudDrive2 集成** - 支持 CloudDrive2 Webhook 通知
- 🌐 **Web 管理界面** - 直观的 Web 界面进行配置和管理
- 🔐 **JWT 认证** - 安全的用户认证系统
- 🔄 **Webhook 支持** - 支持 CloudDrive2 和 MoviePilot2 的 Webhook 通知

## 🚀 快速部署


### Docker Compose 部署

1. **创建目录并下载文件**
```bash
mkdir -p film-fusion/data && cd film-fusion
curl -O https://raw.githubusercontent.com/xifofo/film-fusion/main/docker-compose.yml
curl -o data/config.yaml https://raw.githubusercontent.com/xifofo/film-fusion/main/data/config.example.yaml
```

2. **修改配置文件**
编辑 `data/config.yaml`，必须修改：
```yaml
server:
  password: "your-secure-password"  # 管理员密码
jwt:
  secret: "your-very-long-random-secret-key"  # JWT密钥
```

3. **修改挂载路径**
编辑 `docker-compose.yml`，修改媒体目录路径：
```yaml
volumes:
  - /path/to/your/media:/mnt/media  # 修改为实际媒体路径
```

4. **启动服务**
```bash
docker-compose up -d
```

## ⚙️ 配置说明

### 基础配置
```yaml
server:
  port: 9000                        # Web界面端口
  username: "admin"                 # 初始管理员用户名
  password: "your-secure-password"  # 初始管理员密码
  download_115_concurrency: 2       # 115网盘下载并发数

jwt:
  secret: "your-jwt-secret-key"     # JWT签名密钥（必须修改）
  expire_time: 240                  # Token过期时间（小时）
```

### Emby 集成配置
```yaml
emby:
  enabled: true                     # 启用Emby集成
  url: "http://localhost:8096"      # Emby服务器地址
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
添加 webhook 并找到 base_url 改成自己部署的地址，把 enabled 改成 true
```
base_url = "http://xxx.xxx.xxx.xxx:8095/webhook/clouddrive2"
# Whether the webhook is enabled
enabled = true
```

#### **MoviePilot2**:
添加 webhook 插件 选择 `POST` 填入以下链接
`http://xxx.xxx.xxx.xxx:8095/webhook/movie-pilot/v2`

#### EMBY 入库补充 媒体信息
EMBY 通知添加 webhook 勾选 新媒体已添加，填入链接

`http://xxx.xxx.xxx.xxx:8095/webhook/emby`


## 🛠️ 常用命令

```bash
# 查看服务状态
docker-compose ps

# 查看实时日志
docker-compose logs -f film-fusion

# 重启服务
docker-compose restart

# 更新应用
docker-compose pull && docker-compose up -d

# 停止服务
docker-compose down
```

## 🔍 故障排除

### 常见问题

**Q: 无法访问Web界面**
```bash
# 检查服务状态和端口占用
docker-compose ps
sudo netstat -tlnp | grep 9000
```

**Q: 115网盘下载失败**
- 检查 Access Token 是否过期
- 降低 download_115_concurrency 配置

## 🔐 安全建议

1. **修改默认密码** - 首次部署后立即修改
2. **使用强密钥** - 设置复杂的JWT密钥
3. **启用HTTPS** - 使用反向代理配置SSL
4. **定期备份** - 备份配置文件和数据库

## 📄 开源协议

本项目基于 [MIT 协议](LICENSE) 开源发布。

---

**Film Fusion** - *让媒体管理变得简单高效* 🎬✨
