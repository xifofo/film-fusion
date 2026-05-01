package config

import (
	"fmt"
	"log"

	"github.com/spf13/viper"
)

type Config struct {
	Server      ServerConfig       `mapstructure:"server"`
	Log         LogConfig          `mapstructure:"log"`
	JWT         JWTConfig          `mapstructure:"jwt"`
	FileWatcher FileWatcherConfigs `mapstructure:"file_watcher"`
	Emby        EmbyConfig         `mapstructure:"emby"`
	MoviePilot  MoviePilotConfig   `mapstructure:"moviepilot"`
}

type ServerConfig struct {
	Port                   string `mapstructure:"port"`
	Username               string `mapstructure:"username"`
	Password               string `mapstructure:"password"`
	Download115Concurrency int    `mapstructure:"download_115_concurrency"`
	ProcessNewMedia        bool   `mapstructure:"process_new_media"` // 是否处理新增媒体事件
}

type LogConfig struct {
	Level      string `mapstructure:"level"`
	Format     string `mapstructure:"format"`      // json 或 text
	Output     string `mapstructure:"output"`      // stdout 或 file
	MaxSize    int    `mapstructure:"max_size"`    // 兆字节
	MaxBackups int    `mapstructure:"max_backups"` // 备份数量
	MaxAge     int    `mapstructure:"max_age"`     // 天数
	Compress   bool   `mapstructure:"compress"`    // 是否压缩旧文件
}

type JWTConfig struct {
	Secret     string `mapstructure:"secret"`      // JWT 密钥
	ExpireTime int    `mapstructure:"expire_time"` // 过期时间（小时）
	Issuer     string `mapstructure:"issuer"`      // 签发者
}

// FileWatcherConfigs 保存文件监控配置
type FileWatcherConfigs struct {
	Enabled bool                `mapstructure:"enabled"` // 是否启用文件监控功能
	Configs []FileWatcherConfig `mapstructure:"configs"` // 多个监控配置
}

// FileWatcherConfig 保存单个文件监控配置
type FileWatcherConfig struct {
	Name                 string   `mapstructure:"name"`                   // 监控配置名称
	SourceDir            string   `mapstructure:"source_dir"`             // 监控的源目录
	TargetDir            string   `mapstructure:"target_dir"`             // 目标复制目录
	Extensions           []string `mapstructure:"extensions"`             // 监控的文件扩展名，空表示所有文件
	Recursive            bool     `mapstructure:"recursive"`              // 是否递归监控子目录
	CopyMode             string   `mapstructure:"copy_mode"`              // 复制模式: copy(复制), move(移动), link(硬链接)
	CreateDirs           bool     `mapstructure:"create_dirs"`            // 是否自动创建目标目录
	ProcessExistingFiles bool     `mapstructure:"process_existing_files"` // 是否在启动时处理已存在的文件
}

type EmbyConfig struct {
	Enabled             bool            `mapstructure:"enabled"`                // 是否启用 EMBY 服务
	URL                 string          `mapstructure:"url"`                    // EMBY 服务器地址
	APIKey              string          `mapstructure:"api_key"`                // EMBY API 密钥
	AdminUserID         string          `mapstructure:"admin_user_id"`          // EMBY 管理员用户 ID
	CacheTime           int             `mapstructure:"cache_time"`             // API 请求超时时间（秒）
	AddCurrentMediaInfo bool            `mapstructure:"add_current_media_info"` // 是否在开始播放时补充当前媒体信息
	AddNextMediaInfo    bool            `mapstructure:"add_next_media_info"`    // 是否添加下一部媒体信息
	RunProxyPort        int             `mapstructure:"run_proxy_port"`         // 运行 Emby 代理端口
	Cover               EmbyCoverConfig `mapstructure:"cover"`                  // 媒体库封面生成器配置
}

// EmbyCoverConfig 媒体库封面生成器配置
type EmbyCoverConfig struct {
	Enabled     bool   `mapstructure:"enabled"`      // 是否启用封面生成功能
	Cron        string `mapstructure:"cron"`         // cron 表达式，例如 "0 3 * * *" 每天 3 点；为空则禁用定时任务
	Width       int    `mapstructure:"width"`        // 输出图宽（默认 1920）
	Height      int    `mapstructure:"height"`       // 输出图高（默认 1080）
	JpegQuality int    `mapstructure:"jpeg_quality"` // JPEG 输出质量 1-100（默认 88）
	FontCN      string `mapstructure:"font_cn"`      // 中文字体绝对路径或相对项目根目录路径
	FontEN      string `mapstructure:"font_en"`      // 英文字体路径
	PosterCount int    `mapstructure:"poster_count"` // 拼接海报数量（默认 9，对应九宫格）
}

type MoviePilotConfig struct {
	API      string `mapstructure:"api"`      // MoviePilot API 地址
	Username string `mapstructure:"username"` // MoviePilot 用户名
	Password string `mapstructure:"password"` // MoviePilot 密码
}

func Load() *Config {
	setDefaults()

	// 读取配置
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			log.Println("未找到配置文件，使用默认配置")
		} else {
			log.Fatalf("读取配置文件出错: %v", err)
		}
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		log.Fatalf("无法解码配置: %v", err)
	}

	// 验证配置
	if err := validateConfig(&config); err != nil {
		log.Fatalf("配置验证失败: %v", err)
	}

	return &config
}

// setDefaults 设置默认配置
func setDefaults() {
	viper.SetDefault("server.port", "5000")
	viper.SetDefault("server.process_new_media", true) // 默认启用新媒体处理

	// MoviePilot 默认配置
	viper.SetDefault("moviepilot.api", "http://127.0.0.1:3001")
	viper.SetDefault("moviepilot.username", "")
	viper.SetDefault("moviepilot.password", "")

	// 日志默认配置
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.format", "text")
	viper.SetDefault("log.output", "stdout")
	viper.SetDefault("log.max_size", 100)
	viper.SetDefault("log.max_backups", 3)
	viper.SetDefault("log.max_age", 28)
	viper.SetDefault("log.compress", true)

	// JWT默认配置
	viper.SetDefault("jwt.secret", "your-secret-key-change-in-production")
	viper.SetDefault("jwt.expire_time", 24) // 24小时
	viper.SetDefault("jwt.issuer", "film-fusion")

	// Emby 默认配置
	viper.SetDefault("emby.add_current_media_info", true)

	// Emby Cover 默认配置
	viper.SetDefault("emby.cover.enabled", false)
	viper.SetDefault("emby.cover.cron", "")
	viper.SetDefault("emby.cover.width", 1920)
	viper.SetDefault("emby.cover.height", 1080)
	viper.SetDefault("emby.cover.jpeg_quality", 88)
	viper.SetDefault("emby.cover.font_cn", "data/assets/fonts/SourceHanSansCN-Bold.otf")
	viper.SetDefault("emby.cover.font_en", "data/assets/fonts/Inter-Bold.ttf")
	viper.SetDefault("emby.cover.poster_count", 9)
}

// validateConfig 验证配置的有效性
func validateConfig(config *Config) error {
	if config.Server.Port == "" {
		return fmt.Errorf("服务器端口未设置")
	}
	if config.JWT.Secret == "" {
		return fmt.Errorf("JWT密钥未设置")
	}
	return nil
}
