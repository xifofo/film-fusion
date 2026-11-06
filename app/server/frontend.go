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

// frontendHandler 从 distDir 提供唯一的管理前端，不接管 API 或 Webhook。
// 历史 /v2/ 地址跳转到根路径下的对应地址，不再读取第二套前端产物。
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

		relative := strings.TrimPrefix(requestPath, "/")
		relative = strings.TrimSuffix(relative, "/")

		// 在重定向和拼接磁盘路径前校验，拒绝目录穿越及双斜线形成的站外跳转。
		if relative != "" && (!fs.ValidPath(relative) || relative == "." || strings.Contains(relative, "\\")) {
			c.Status(http.StatusBadRequest)
			return
		}

		if requestPath == "/v2" || strings.HasPrefix(requestPath, "/v2/") {
			targetURL := *c.Request.URL
			targetURL.Path = strings.TrimPrefix(requestPath, "/v2")
			targetURL.RawPath = ""
			if targetURL.Path == "" {
				targetURL.Path = "/"
			}

			// 重新转义路径中的保留字符，查询参数保持原样。
			target := targetURL.EscapedPath()
			if targetURL.RawQuery != "" {
				target += "?" + targetURL.RawQuery
			}
			c.Redirect(http.StatusPermanentRedirect, target)
			return
		}

		if relative != "" && serveFrontendFile(c, filepath.Join(distDir, filepath.FromSlash(relative))) {
			return
		}

		// 丢失的静态资源必须返回 404，不能用 HTML 冒充 JS/CSS；页面刷新才回退入口。
		if path.Ext(relative) != "" || relative == "assets" || strings.HasPrefix(relative, "assets/") {
			c.Status(http.StatusNotFound)
			return
		}
		if !serveFrontendFile(c, filepath.Join(distDir, "index.html")) {
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
