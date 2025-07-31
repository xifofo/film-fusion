package cmd

import (
	"context"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/server"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "启动服务器",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()

		// 创建日志器
		log := logger.New(cfg.Log)
		defer log.Sync()

		// 初始化数据库
		if err := database.Init(cfg, log); err != nil {
			log.Fatalf("数据库初始化失败: %v", err)
		}

		srv := server.New(cfg, log)

		// 在协程中启动服务器
		go func() {
			if err := srv.Start(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("启动服务器失败: %v", err)
			}
		}()

		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		log.Info("收到关闭信号，正在关闭服务器...")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Errorf("服务器关闭失败: %v", err)
		}
		log.Info("服务器已退出")
	},
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
