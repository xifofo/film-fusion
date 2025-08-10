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
}

type ServerConfig struct {
	Port                   string `mapstructure:"port"`
	Username               string `mapstructure:"username"`
	Password               string `mapstructure:"password"`
	Download115Concurrency int    `mapstructure:"download_115_concurrency"`
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
