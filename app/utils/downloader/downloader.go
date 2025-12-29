package downloader

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DownloadConfig 下载配置
type DownloadConfig struct {
	UserAgent     string        // User-Agent
	Timeout       time.Duration // 超时时间
	UseTemp       bool          // 是否使用临时文件
	OverwriteFile bool          // 是否覆盖已存在的文件
	BufferSize    int           // 缓冲区大小 (字节)
}

// DefaultDownloadConfig 默认下载配置
func DefaultDownloadConfig() *DownloadConfig {
	return &DownloadConfig{
		UserAgent:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		Timeout:       time.Minute * 30,
		UseTemp:       true,
		OverwriteFile: false,
		BufferSize:    1024 * 1024 * 2, // 2MB 缓冲区
	}
}

// DownloadResult 下载结果
type DownloadResult struct {
	Size     int64         // 下载的文件大小
	Duration time.Duration // 下载耗时
	Speed    float64       // 下载速度 (MB/s)
	Path     string        // 保存的文件路径
}

// DownloadFromURL 通用的从 URL 下载文件的方法
func DownloadFromURL(url, savePath string, config *DownloadConfig) (*DownloadResult, error) {
	if config == nil {
		config = DefaultDownloadConfig()
	}

	// 检查文件是否已存在
	if !config.OverwriteFile {
		if _, err := os.Stat(savePath); err == nil {
			return nil, fmt.Errorf("文件已存在: %s", savePath)
		}
	}

	// 创建HTTP请求
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	// 设置请求头
	req.Header.Set("User-Agent", config.UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity") // 禁用压缩，避免 Content-Length 不匹配
	req.Header.Set("Connection", "keep-alive")

	// 创建HTTP客户端，确保跟随重定向
	client := &http.Client{
		Timeout: config.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// 允许最多 10 次重定向
			if len(via) >= 10 {
				return fmt.Errorf("重定向次数过多")
			}
			// 保持原始请求头
			req.Header.Set("User-Agent", config.UserAgent)
			return nil
		},
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		// 读取错误响应体用于调试
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("HTTP请求失败，状态码: %d, 响应: %s", resp.StatusCode, string(bodyBytes))
	}

	// 获取文件大小（如果有Content-Length头）
	contentLength := resp.ContentLength

	// 确保保存目录存在
	if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
		return nil, fmt.Errorf("创建保存目录失败: %w", err)
	}

	// 决定使用的文件路径
	targetPath := savePath
	if config.UseTemp {
		targetPath = savePath + ".tmp"
	}

	// 创建文件
	file, err := os.Create(targetPath)
	if err != nil {
		return nil, fmt.Errorf("创建文件失败: %w", err)
	}
	defer func() {
		file.Close()
		// 如果下载失败，删除未完成的文件
		if err != nil {
			os.Remove(targetPath)
		}
	}()

	// 记录下载开始时间
	startTime := time.Now()

	// 使用 io.Copy 进行可靠的数据传输
	written, err := io.Copy(file, resp.Body)
	if err != nil {
		return nil, fmt.Errorf("写入文件内容失败: %w", err)
	}

	// 强制刷新数据到磁盘
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("刷新文件到磁盘失败: %w", err)
	}

	// 关闭文件句柄
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("关闭文件失败: %w", err)
	}

	// 验证文件大小（如果服务器提供了Content-Length）
	if contentLength > 0 && written != contentLength {
		os.Remove(targetPath)
		return nil, fmt.Errorf("下载不完整: 期望 %d bytes, 实际 %d bytes", contentLength, written)
	}

	// 如果使用临时文件，重命名为最终文件名
	if config.UseTemp {
		if err := os.Rename(targetPath, savePath); err != nil {
			// 删除临时文件
			os.Remove(targetPath)
			return nil, fmt.Errorf("重命名文件失败: %w", err)
		}
	}

	// 计算下载结果
	duration := time.Since(startTime)
	speed := float64(written) / duration.Seconds() / 1024 / 1024 // MB/s

	result := &DownloadResult{
		Size:     written,
		Duration: duration,
		Speed:    speed,
		Path:     savePath,
	}

	return result, nil
}

// DownloadFromURLSimple 简化的下载方法，使用默认配置
func DownloadFromURLSimple(url, userAgent, savePath string) error {
	config := DefaultDownloadConfig()
	if userAgent != "" {
		config.UserAgent = userAgent
	}

	_, err := DownloadFromURL(url, savePath, config)
	return err
}
