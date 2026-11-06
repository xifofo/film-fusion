// 服务器目录选择只列出直属子目录，不读取文件内容、不创建目录，也不触发 STRM 写入。
package handler

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// LocalDirectoryHandler 供后台管理员选择服务进程可见的本地或容器内目录。
// 路由必须挂在 APIAuth 与 RequireAdmin 之后，且不授予 API Token 模块权限。
type LocalDirectoryHandler struct{}

// NewLocalDirectoryHandler 创建无状态的只读目录浏览处理器。
func NewLocalDirectoryHandler() *LocalDirectoryHandler { return &LocalDirectoryHandler{} }

type localDirectoryOption struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// List 在未提供 path 时返回文件系统根节点；展开时分页列出指定绝对路径的直属子目录。
// 符号链接只在指向可访问目录时展示，保留选择路径，不预读下一层目录。
func (h *LocalDirectoryHandler) List(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.GetUint("user_id") == 0 {
		c.JSON(http.StatusUnauthorized, NewErrorResponse("用户未认证", ""))
		return
	}
	if _, token := c.Get("api_token_id"); token {
		c.JSON(http.StatusForbidden, NewErrorResponse("请选择后台登录会话浏览服务器目录", ""))
		return
	}

	page, pageErr := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, sizeErr := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if pageErr != nil || sizeErr != nil || page < 1 || page > 1_000_000 || size < 1 || size > 200 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("目录分页参数无效", ""))
		return
	}

	path := c.Query("path")
	items := []localDirectoryOption{}
	if path == "" {
		items = localDirectoryRoots()
	} else {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') || len(path) > 4096 {
			c.JSON(http.StatusBadRequest, NewErrorResponse("请提供有效的服务器绝对目录路径", ""))
			return
		}

		path = filepath.Clean(path)
		info, err := os.Stat(path)
		if err != nil {
			h.readError(c, err)
			return
		}
		if !info.IsDir() {
			c.JSON(http.StatusBadRequest, NewErrorResponse("所选路径不是目录", ""))
			return
		}

		entries, err := os.ReadDir(path)
		if err != nil {
			h.readError(c, err)
			return
		}
		for _, entry := range entries {
			if c.Request.Context().Err() != nil {
				return
			}

			child := filepath.Join(path, entry.Name())
			isDirectory := entry.IsDir()
			if entry.Type()&os.ModeSymlink != 0 {
				linked, linkErr := os.Stat(child)
				isDirectory = linkErr == nil && linked.IsDir()
			}
			if isDirectory {
				items = append(items, localDirectoryOption{Name: entry.Name(), Path: child})
			}
		}
	}

	// 排序完整的直属目录后再分页，树分支不会因为翻页而遗漏或重复节点。
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	total := len(items)
	start := min(total, (page-1)*size)
	end := min(total, start+size)
	c.JSON(http.StatusOK, NewSuccessResponse("读取服务器目录成功", gin.H{
		"items": items[start:end], "total": total, "page": page, "has_more": end < total,
	}))
}

func (h *LocalDirectoryHandler) readError(c *gin.Context, err error) {
	status, message := http.StatusInternalServerError, "读取服务器目录失败"
	if errors.Is(err, os.ErrNotExist) {
		status, message = http.StatusNotFound, "服务器目录不存在"
	} else if errors.Is(err, os.ErrPermission) {
		status, message = http.StatusForbidden, "没有权限读取此服务器目录"
	}

	c.JSON(status, NewErrorResponse(message, ""))
}

func localDirectoryRoots() []localDirectoryOption {
	if runtime.GOOS != "windows" {
		return []localDirectoryOption{{Name: "/", Path: "/"}}
	}

	roots := []localDirectoryOption{}
	for drive := 'A'; drive <= 'Z'; drive++ {
		path := string(drive) + ":\\"
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			roots = append(roots, localDirectoryOption{Name: path, Path: path})
		}
	}
	return roots
}
