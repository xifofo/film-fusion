package config

import (
	"fmt"
	"log"

	"github.com/spf13/viper"
)

type Config struct {
	Server      ServerConfig       `mapstructure:"server" json:"server"`
	Log         LogConfig          `mapstructure:"log" json:"log"`
	JWT         JWTConfig          `mapstructure:"jwt" json:"jwt"`
	FileWatcher FileWatcherConfigs `mapstructure:"file_watcher" json:"file_watcher"`
	Emby        EmbyConfig         `mapstructure:"emby" json:"emby"`
	MoviePilot  MoviePilotConfig   `mapstructure:"moviepilot" json:"moviepilot"`
}

type ServerConfig struct {
	Port                   string `mapstructure:"port" json:"port"`
	Username               string `mapstructure:"username" json:"username"`
	Password               string `mapstructure:"password" json:"password"`
	Download115Concurrency int    `mapstructure:"download_115_concurrency" json:"download_115_concurrency"`
	ProcessNewMedia        bool   `mapstructure:"process_new_media" json:"process_new_media"` // 是否处理新增媒体事件
}

type LogConfig struct {
	Level      string `mapstructure:"level" json:"level"`
	Format     string `mapstructure:"format" json:"format"`           // json 或 text
	Output     string `mapstructure:"output" json:"output"`           // stdout 或 file
	MaxSize    int    `mapstructure:"max_size" json:"max_size"`       // 兆字节
	MaxBackups int    `mapstructure:"max_backups" json:"max_backups"` // 备份数量
	MaxAge     int    `mapstructure:"max_age" json:"max_age"`         // 天数
	Compress   bool   `mapstructure:"compress" json:"compress"`       // 是否压缩旧文件
}

type JWTConfig struct {
	Secret     string `mapstructure:"secret" json:"secret"`           // JWT 密钥
	ExpireTime int    `mapstructure:"expire_time" json:"expire_time"` // 过期时间（小时）
	Issuer     string `mapstructure:"issuer" json:"issuer"`           // 签发者
}

// FileWatcherConfigs 保存文件监控配置
type FileWatcherConfigs struct {
	Enabled bool                `mapstructure:"enabled" json:"enabled"` // 是否启用文件监控功能
	Configs []FileWatcherConfig `mapstructure:"configs" json:"configs"` // 多个监控配置
}

// FileWatcherConfig 保存单个文件监控配置
type FileWatcherConfig struct {
	Name                 string   `mapstructure:"name" json:"name"`                                     // 监控配置名称
	SourceDir            string   `mapstructure:"source_dir" json:"source_dir"`                         // 监控的源目录
	TargetDir            string   `mapstructure:"target_dir" json:"target_dir"`                         // 目标复制目录
	Extensions           []string `mapstructure:"extensions" json:"extensions"`                         // 监控的文件扩展名，空表示所有文件
	Recursive            bool     `mapstructure:"recursive" json:"recursive"`                           // 是否递归监控子目录
	CopyMode             string   `mapstructure:"copy_mode" json:"copy_mode"`                           // 复制模式: copy(复制), move(移动), link(硬链接)
	CreateDirs           bool     `mapstructure:"create_dirs" json:"create_dirs"`                       // 是否自动创建目标目录
	ProcessExistingFiles bool     `mapstructure:"process_existing_files" json:"process_existing_files"` // 是否在启动时处理已存在的文件
}

type EmbyConfig struct {
	Enabled             bool            `mapstructure:"enabled" json:"enabled"`                                 // 是否启用 EMBY 服务
	URL                 string          `mapstructure:"url" json:"url"`                                         // EMBY 服务器地址
	APIKey              string          `mapstructure:"api_key" json:"api_key"`                                 // EMBY API 密钥
	AdminUserID         string          `mapstructure:"admin_user_id" json:"admin_user_id"`                     // EMBY 管理员用户 ID
	CacheTime           int             `mapstructure:"cache_time" json:"cache_time"`                           // API 请求超时时间（秒）
	AddCurrentMediaInfo bool            `mapstructure:"add_current_media_info" json:"add_current_media_info"`   // 是否在开始播放时补充当前媒体信息
	AddNextMediaInfo    bool            `mapstructure:"add_next_media_info" json:"add_next_media_info"`         // 是否添加下一部媒体信息
	RunProxyPort        int             `mapstructure:"run_proxy_port" json:"run_proxy_port"`                   // 运行 Emby 代理端口
	Cover               EmbyCoverConfig `mapstructure:"cover" json:"cover"`                                     // 媒体库封面生成器配置
}

// EmbyCoverConfig 媒体库封面生成器配置
type EmbyCoverConfig struct {
	Enabled     bool   `mapstructure:"enabled" json:"enabled"`           // 是否启用封面生成功能
	Cron        string `mapstructure:"cron" json:"cron"`                 // cron 表达式，例如 "0 3 * * *" 每天 3 点；为空则禁用定时任务
	Width       int    `mapstructure:"width" json:"width"`               // 输出图宽（默认 1920）
	Height      int    `mapstructure:"height" json:"height"`             // 输出图高（默认 1080）
	JpegQuality int    `mapstructure:"jpeg_quality" json:"jpeg_quality"` // JPEG 输出质量 1-100（默认 88）
	FontCN      string `mapstructure:"font_cn" json:"font_cn"`           // 中文字体绝对路径或相对项目根目录路径
	FontEN      string `mapstructure:"font_en" json:"font_en"`           // 英文字体路径
	PosterCount int    `mapstructure:"poster_count" json:"poster_count"` // 拼接海报数量（默认 9，对应九宫格）
}

type MoviePilotConfig struct {
	API      string `mapstructure:"api" json:"api"`           // MoviePilot API 地址
	Username string `mapstructure:"username" json:"username"` // MoviePilot 用户名
	Password string `mapstructure:"password" json:"password"` // MoviePilot 密码
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

// Save 把内存配置写回 config.yaml（仅覆盖已暴露的键，未暴露键如 file_watcher 会被保留）。
// 通过全局 viper 设置各键后 WriteConfig，保持原有 yaml 键名与未管理项。
func Save(c *Config) error {
	viper.Set("server.port", c.Server.Port)
	viper.Set("server.username", c.Server.Username)
	viper.Set("server.password", c.Server.Password)
	viper.Set("server.download_115_concurrency", c.Server.Download115Concurrency)
	viper.Set("server.process_new_media", c.Server.ProcessNewMedia)

	viper.Set("log.level", c.Log.Level)
	viper.Set("log.format", c.Log.Format)
	viper.Set("log.output", c.Log.Output)
	viper.Set("log.max_size", c.Log.MaxSize)
	viper.Set("log.max_backups", c.Log.MaxBackups)
	viper.Set("log.max_age", c.Log.MaxAge)
	viper.Set("log.compress", c.Log.Compress)

	viper.Set("jwt.secret", c.JWT.Secret)
	viper.Set("jwt.expire_time", c.JWT.ExpireTime)
	viper.Set("jwt.issuer", c.JWT.Issuer)

	viper.Set("emby.enabled", c.Emby.Enabled)
	viper.Set("emby.url", c.Emby.URL)
	viper.Set("emby.api_key", c.Emby.APIKey)
	viper.Set("emby.admin_user_id", c.Emby.AdminUserID)
	viper.Set("emby.cache_time", c.Emby.CacheTime)
	viper.Set("emby.add_current_media_info", c.Emby.AddCurrentMediaInfo)
	viper.Set("emby.add_next_media_info", c.Emby.AddNextMediaInfo)
	viper.Set("emby.run_proxy_port", c.Emby.RunProxyPort)

	viper.Set("emby.cover.enabled", c.Emby.Cover.Enabled)
	viper.Set("emby.cover.cron", c.Emby.Cover.Cron)
	viper.Set("emby.cover.width", c.Emby.Cover.Width)
	viper.Set("emby.cover.height", c.Emby.Cover.Height)
	viper.Set("emby.cover.jpeg_quality", c.Emby.Cover.JpegQuality)
	viper.Set("emby.cover.font_cn", c.Emby.Cover.FontCN)
	viper.Set("emby.cover.font_en", c.Emby.Cover.FontEN)
	viper.Set("emby.cover.poster_count", c.Emby.Cover.PosterCount)

	viper.Set("moviepilot.api", c.MoviePilot.API)
	viper.Set("moviepilot.username", c.MoviePilot.Username)
	viper.Set("moviepilot.password", c.MoviePilot.Password)

	if err := viper.WriteConfig(); err != nil {
		// 配置文件不存在时回退到显式路径写入
		path := viper.ConfigFileUsed()
		if path == "" {
			path = "data/config.yaml"
		}
		return viper.WriteConfigAs(path)
	}
	return nil
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
