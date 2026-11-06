// 115 应用字典候选查询只返回公开名称和 ID，与授权使用相同的管理员及 API Token 权限。
package handler

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"film-fusion/app/database"

	"github.com/gin-gonic/gin"
)

// ListApps 按名称或 ID 搜索并分页；ids 用于精确回显，不将目录作为授权白名单。
func (h *Auth115Handler) ListApps(c *gin.Context) {
	page, pageErr := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, sizeErr := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	search := strings.ToLower(strings.TrimSpace(c.Query("q")))
	ids := c.QueryArray("ids")
	if pageErr != nil || sizeErr != nil || page < 1 || page > 1_000_000 || size < 1 || size > 100 ||
		utf8.RuneCountInString(search) > 100 || len(ids) > 100 {
		h.error(c, http.StatusBadRequest, 400, "应用目录查询参数无效")
		return
	}

	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !database.Valid115ClientID(id) {
			h.error(c, http.StatusBadRequest, 400, "应用 ID 格式无效")
			return
		}
		wanted[id] = true
	}

	items, err := database.Load115AppCatalog(database.GetDB().WithContext(c.Request.Context()))
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "读取 115 应用目录失败")
		return
	}

	filtered := make([]database.App115Option, 0)
	for _, item := range items {
		if !item.Enabled || (len(ids) > 0 && !wanted[item.Value]) {
			continue
		}
		if len(ids) == 0 && !strings.Contains(strings.ToLower(item.Label), search) && !strings.Contains(item.Value, search) {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].SortOrder != filtered[j].SortOrder {
			return filtered[i].SortOrder < filtered[j].SortOrder
		}
		return filtered[i].Value < filtered[j].Value
	})

	total := len(filtered)
	start := min((page-1)*size, total)
	end := min(start+size, total)
	if len(ids) > 0 {
		page, start, end = 1, 0, total
	}
	h.success(c, gin.H{
		"list": filtered[start:end], "total": total, "page": page, "page_size": size,
	}, "获取 115 应用目录成功")
}
