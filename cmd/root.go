package cmd

import (
	"log"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var rootCmd = &cobra.Command{
	Use:     "film-fusion",
	Short:   "家庭影院融合工具",
	Long:    "一个用于管理 STRM 文件和 Emby 服务器的工具",
	Version: "2.0.0",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
}

// initConfig 读取配置文件和环境变量（如果设置）
func initConfig() {
	// 添加配置文件搜索路径
	viper.AddConfigPath("./data") // 相对于当前工作目录的 data 文件夹
	viper.AddConfigPath(".")      // 当前目录
	viper.SetConfigType("yaml")
	viper.SetConfigName("config")

	viper.AutomaticEnv() // 读取匹配的环境变量

	// 如果找到配置文件，读取它
	if err := viper.ReadInConfig(); err != nil {
		log.Println("配置文件读取失败:", err)
		os.Exit(1)
	}
}
