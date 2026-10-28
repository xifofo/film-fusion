package server

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// frontendHandler 将未命中的页面路由分配给各自的 SPA，不接管 API 或 Webhook。
// distDir 保存旧版产物，distDir/v2 保存新版产物；缺少新版时不能回退到旧版。
func frontendHandler(distDir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if strings.HasPrefix(requestPath, "/api") || strings.HasPrefix(requestPath, "/webhook") {
			c.JSON(http.StatusNotFound, gin.H{"error": "API路径不存在"})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Status(http.StatusNotFound)
			return
		}

		if requestPath == "/v2" {
			target := "/v2/"
			if c.Request.URL.RawQuery != "" {
				target += "?" + c.Request.URL.RawQuery
			}
			c.Redirect(http.StatusPermanentRedirect, target)
			return
		}

		root := distDir
		relative := strings.TrimPrefix(requestPath, "/")
		if strings.HasPrefix(requestPath, "/v2/") {
			root = filepath.Join(distDir, "v2")
			relative = strings.TrimPrefix(requestPath, "/v2/")
		}
		relative = strings.TrimSuffix(relative, "/")

		// 在拼接磁盘路径前拒绝目录穿越，避免新旧资源跨目录读取。
		if relative != "" && (!fs.ValidPath(relative) || relative == "." || strings.Contains(relative, "\\")) {
			c.Status(http.StatusBadRequest)
			return
		}
		if relative != "" && serveFrontendFile(c, filepath.Join(root, filepath.FromSlash(relative))) {
			return
		}

		// 丢失的静态资源必须返回 404，不能用 HTML 冒充 JS/CSS；页面刷新才回退入口。
		if path.Ext(relative) != "" || relative == "assets" || strings.HasPrefix(relative, "assets/") {
			c.Status(http.StatusNotFound)
			return
		}
		if !serveFrontendFile(c, filepath.Join(root, "index.html")) {
			c.Status(http.StatusNotFound)
		}
	}
}

// serveFrontendFile 只发送普通文件，不展示目录列表；入口要求重新验证以免升级后引用旧资源。
func serveFrontendFile(c *gin.Context, filename string) bool {
	file, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if filepath.Ext(filename) == ".html" {
		c.Header("Cache-Control", "no-cache")
	}

	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
	return true
}
