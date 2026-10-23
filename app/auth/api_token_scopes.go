package auth

import "strings"

// APITokenScope grants every operation in the listed modules, including writes.
// Account, token and server configuration routes deliberately have no scope.
type APITokenScope struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Prefixes    []string `json:"prefixes"`
}

func APITokenScopes() []APITokenScope {
	return []APITokenScope{
		{"system-info", "系统信息", "读取服务器运行状态", []string{"/api/system-info"}},
		{"cloud-storage", "云存储管理", "管理存储账号及 115 授权，包含账号凭据和 Cookie", []string{"/api/cloud-storage", "/api/auth/115", "/api/115-cookie", "/api/115-open"}},
		{"paths", "云路径映射", "查询、修改路径映射并执行目录操作", []string{"/api/paths"}},
		{"directories", "目录配置", "查询和修改目录配置", []string{"/api/directories"}},
		{"strm", "STRM 生成", "生成或重新生成 STRM 文件", []string{"/api/strm"}},
		{"media-recognition", "媒体识别", "识别测试及识别词、分类和重命名配置", []string{"/api/media-recognition"}},
		{"organize", "文件整理", "整理文件、搜索媒体及管理预览任务", []string{"/api/organize"}},
		{"download-queue", "下载队列", "查看、重试和删除下载任务", []string{"/api/download-queue"}},
		{"downloaders", "下载器设置", "管理下载器连接配置，包含连接凭据", []string{"/api/downloaders"}},
		{"automations", "自动化中心", "管理并执行自动化流程，包含下载器连接配置", []string{"/api/automations"}},
		{"match-302", "Match302 重定向", "管理重定向规则、负载均衡及缓存分配", []string{"/api/match-302"}},
		{"pickcode-cache", "Pickcode 缓存", "查询、修改和清空文件缓存映射", []string{"/api/pickcode-cache"}},
		{"emby-library", "Emby 媒体库工具", "批量入库检查、封面生成及排序名称回填", []string{"/api/emby-cover", "/api/emby-sortname", "/api/emby-library-check"}},
		{"emby-stats", "Emby 媒体统计", "读取媒体统计和图片", []string{"/api/emby-stats"}},
		{"emby-watch", "Emby 观看记录", "查看、回填、删除观看记录及修改统计配置", []string{"/api/emby-watch"}},
		{"emby-accounts", "Emby 账户管理", "查询账户并设置到期时间", []string{"/api/emby-accounts"}},
		{"emby-bindings", "Emby 账号绑定", "管理 Emby 与 115 账号绑定", []string{"/api/emby-bindings"}},
		{"emby-missing", "Emby 缺集扫描", "扫描缺集及管理扫描配置和黑名单", []string{"/api/emby-missing"}},
		{"emby-version-check", "Emby 多版本检查", "扫描本地媒体多版本并管理配置", []string{"/api/emby-version-check"}},
		{"emby-image-optimization", "Emby 图片优化", "管理图片优化配置及执行测试", []string{"/api/emby-image-optimization"}},
		{"emby-proxy", "Emby 代理管理", "查看、清空代理日志及解除登录保护限制", []string{"/api/emby-proxy"}},
		{"logs", "日志中心", "读取运行日志并查询、清空整理日志", []string{"/api/logs", "/api/organize-logs"}},
		{"notifications", "通知测试", "发送已配置渠道的测试通知", []string{"/api/notifications", "/api/telegram"}},
	}
}

// APITokenScopeForRoute uses Gin's matched route, never a client-supplied scope.
// Segment boundaries prevent e.g. /api/paths-extra from inheriting /api/paths.
func APITokenScopeForRoute(route string) string {
	for _, scope := range APITokenScopes() {
		for _, prefix := range scope.Prefixes {
			if route == prefix || strings.HasPrefix(route, prefix+"/") {
				return scope.ID
			}
		}
	}
	return ""
}
