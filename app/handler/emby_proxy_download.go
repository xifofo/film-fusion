package handler

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/pathhelper"
	"film-fusion/app/utils/sdk115helper"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/patrickmn/go-cache"
	"gorm.io/gorm"
)

// md5CacheKey 生成MD5缓存键
func (h *EmbyProxyHandler) md5CacheKey(input string) string {
	// 创建一个 MD5 哈希实例
	hash := md5.New()

	// 写入数据
	hash.Write([]byte(input))

	// 获取哈希结果
	hashBytes := hash.Sum(nil)

	// 将结果转换为十六进制字符串
	hashString := hex.EncodeToString(hashBytes)

	return hashString
}

// checkMatch302 检查路径是否匹配 match302 规则
// 参数:
//   - filePath: 要检查的文件路径
//   - userAgent: 用户代理字符串
//
// 返回:
//   - string: 匹配的重定向URL（如果匹配）
//   - bool: 是否匹配到规则
func (h *EmbyProxyHandler) checkMatch302(filePath, userAgent string) (string, bool) {
	// 预加载关联的 CloudStorage 数据，避免 N+1 查询
	var matches []model.Match302
	if err := database.DB.Preload("CloudStorage").Find(&matches).Error; err != nil {
		h.logger.Errorf("[EMBY PROXY] 查询 match302 配置失败: %v", err)
		return "", false
	}

	if len(matches) == 0 {
		h.logger.Debugf("[EMBY PROXY] 未找到任何 match302 规则")
		return "", false
	}

	h.logger.Debugf("[EMBY PROXY] 检查路径 %s 是否匹配 %d 个 match302 规则", filePath, len(matches))

	// 遍历所有规则，找到匹配的
	for _, match := range matches {
		// 检查关联的云存储是否存在
		if match.CloudStorage == nil {
			h.logger.Warnf("[EMBY PROXY] Match302 规则 ID:%d 缺少关联的云存储配置", match.ID)
			continue
		}

		// 暂时只处理 StorageType115Open 的规则
		if match.CloudStorage.StorageType != model.StorageType115Open {
			continue
		}

		// 使用 Match302 模型的 GetMatchedPath 方法检查是否匹配
		matchedPath := match.GetMatchedPath(filePath)

		// 如果路径没发生变化，说明匹配失败
		if matchedPath == filePath {
			continue
		}

		h.logger.Infof("[EMBY PROXY] Match302 规则匹配: %s -> %s (规则: %s -> %s)",
			filePath, matchedPath, match.SourcePath, match.TargetPath)

		// 尝试获取下载URL
		// filePath 是 Emby 看到的播放地址(STRM 内容空间)，用作 pickcode 缓存 key
		// matchedPath 是 115 盘内路径，用于调 115 API 反查 pickcode
		downloadURL, _, err := h.getDownloadURL(filePath, matchedPath, *match.CloudStorage, userAgent)
		if err != nil {
			h.logger.Errorf("[EMBY PROXY] 获取下载URL失败: %v", err)
			continue
		}

		return downloadURL, true
	}

	h.logger.Debugf("[EMBY PROXY] 路径 %s 未匹配任何 match302 规则", filePath)
	return "", false
}

// getDownloadURL 获取文件的下载URL
// filePath: Emby 播放地址(STRM 内容空间的完整绝对路径)，用作 pickcode 缓存 key
// matchedPath: 115 盘内路径，用于调 115 API 反查 pickcode
func (h *EmbyProxyHandler) getDownloadURL(filePath, matchedPath string, storage model.CloudStorage, userAgent string) (string, bool, error) {
	// 缓存 key 使用解码后的 filePath（与 walk 时写入缓存的 key 语义一致）
	cacheKey, decodeErr := url.PathUnescape(filePath)
	if decodeErr != nil {
		cacheKey = filePath
	}
	cacheKey = pathhelper.EnsureLeadingSlash(cacheKey)

	// 检查是否有 pickcode 缓存
	var pickcodeCache model.PickcodeCache
	err := database.DB.Where("file_path = ?", cacheKey).First(&pickcodeCache).Error

	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, fmt.Errorf("查询 pickcode 缓存失败: %w", err)
	}

	// 如果没有缓存或 pickcode 为空，则获取新的 pickcode
	if errors.Is(err, gorm.ErrRecordNotFound) || pickcodeCache.Pickcode == "" {
		h.logger.Infof("[EMBY PROXY] 路径 %s 未找到 pickcode 缓存，正在获取", cacheKey)

		pickcode, err := h.fetchPickcodeForStorage(context.Background(), matchedPath, storage)
		if err != nil {
			return "", false, fmt.Errorf("获取 pickcode 失败: %w", err)
		}

		// 创建或更新缓存（key 用 STRM 内容空间的 cacheKey）
		cache, _, err := model.UpsertPickcodeCache(database.DB, cacheKey, pickcode)
		if err != nil {
			h.logger.Errorf("[EMBY PROXY] 保存 pickcode 缓存失败: %v", err)
			return "", false, fmt.Errorf("保存 pickcode 缓存失败: %w", err)
		}
		pickcodeCache = *cache
	}

	if pickcodeCache.Pickcode == "" {
		return "", false, fmt.Errorf("pickcode 为空")
	}

	return h.getDownloadURLForStorage(storage, pickcodeCache.Pickcode, userAgent)
}

func (h *EmbyProxyHandler) getDownloadURLForStorage(storage model.CloudStorage, pickcode, userAgent string) (string, bool, error) {
	pickcode = strings.TrimSpace(pickcode)
	if pickcode == "" {
		return "", false, fmt.Errorf("pickcode 为空")
	}
	mode := storage.Match302AccessModeValue()
	cacheKey := h.md5CacheKey(fmt.Sprintf("download-url:%d:%s:%s:%s", storage.ID, mode, pickcode, userAgent))
	if cacheLink, found := h.goCache.Get(cacheKey); found {
		if link, ok := cacheLink.(string); ok && link != "" {
			return link, true, nil
		}
	}

	redirectURL, err := resolveMatch302DownloadURL(
		mode,
		func() (string, error) {
			if strings.TrimSpace(storage.AccessToken) == "" {
				return "", fmt.Errorf("OpenAPI AccessToken 缺失")
			}
			client := sdk115helper.WithAccessToken(h.sdk115Open, storage.AccessToken)
			downURLResp, err := client.DownURL(context.Background(), pickcode, userAgent)
			if err != nil {
				return "", fmt.Errorf("调用 DownURL API 失败: %w", err)
			}
			for _, urlInfo := range downURLResp {
				if rawURL := strings.TrimSpace(urlInfo.URL.URL); rawURL != "" {
					return rawURL, nil
				}
			}
			return "", fmt.Errorf("OpenAPI DownURL 未返回可用直链")
		},
		func() (string, error) {
			if strings.TrimSpace(storage.Cookie) == "" {
				return "", fmt.Errorf("Cookie 缺失")
			}
			client, err := h.web115Svc.NewClient(storage.Cookie)
			if err != nil {
				return "", fmt.Errorf("115 Cookie 无效: %w", err)
			}
			info, err := client.DownloadWithUA(pickcode, userAgent)
			if err != nil {
				return "", fmt.Errorf("调用 DownloadWithUA 失败: %w", err)
			}
			return strings.TrimSpace(info.Url.Url), nil
		},
	)
	if err != nil {
		return "", false, fmt.Errorf("未找到可用的下载URL，pickcode=%s: %w", pickcode, err)
	}
	h.goCache.Set(cacheKey, redirectURL, h.downloadURLCacheTTL(redirectURL))
	return redirectURL, false, nil
}

func resolveMatch302DownloadURL(
	mode string,
	openAPI func() (string, error),
	cookie func() (string, error),
) (string, error) {
	var attemptErrors []error
	for _, method := range model.Match302AccessOrder(mode) {
		var (
			rawURL string
			err    error
		)
		switch method {
		case model.Match302AccessMethodOpenAPI:
			rawURL, err = openAPI()
		case model.Match302AccessMethodCookie:
			rawURL, err = cookie()
		}
		if err == nil && strings.TrimSpace(rawURL) != "" {
			return strings.TrimSpace(rawURL), nil
		}
		if err == nil {
			err = fmt.Errorf("未返回可用直链")
		}
		attemptErrors = append(attemptErrors, fmt.Errorf("%s: %w", method, err))
	}
	return "", fmt.Errorf("Match302 下载方式 %s 全部失败: %w", model.NormalizeMatch302AccessMode(mode), errors.Join(attemptErrors...))
}

func (h *EmbyProxyHandler) downloadURLCacheTTL(rawURL string) time.Duration {
	configured := time.Duration(h.config.Emby.CacheTime) * time.Minute
	if configured <= 0 {
		configured = cache.DefaultExpiration
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return configured
	}
	rawT := parsed.Query().Get("t")
	if rawT == "" {
		return configured
	}
	expiresAt, err := strconv.ParseInt(rawT, 10, 64)
	if err != nil {
		return configured
	}
	until := time.Until(time.Unix(expiresAt, 0))
	if until <= 0 {
		return time.Second
	}
	if configured > 0 && until < configured {
		return until
	}
	return configured
}

func (h *EmbyProxyHandler) fetchPickcodeForStorage(ctx context.Context, matchedPath string, storage model.CloudStorage) (string, error) {
	mode := storage.Match302AccessModeValue()
	var attemptErrors []error
	for _, method := range model.Match302AccessOrder(mode) {
		var (
			file  service.Web115File
			found bool
			err   error
		)
		switch method {
		case model.Match302AccessMethodOpenAPI:
			if strings.TrimSpace(storage.AccessToken) == "" {
				attemptErrors = append(attemptErrors, fmt.Errorf("OpenAPI AccessToken 缺失"))
				continue
			}
			file, found, err = h.web115Svc.ResolveFilePathWithOpenAPI(ctx, storage.AccessToken, matchedPath)
		case model.Match302AccessMethodCookie:
			if strings.TrimSpace(storage.Cookie) == "" {
				attemptErrors = append(attemptErrors, fmt.Errorf("Cookie 缺失"))
				continue
			}
			var client *driver.Pan115Client
			client, err = h.web115Svc.NewClient(storage.Cookie)
			if err == nil {
				file, found, err = h.web115Svc.ResolveFilePathWithClient(client, matchedPath)
			}
		}
		if err != nil {
			attemptErrors = append(attemptErrors, fmt.Errorf("%s 解析失败: %w", method, err))
			continue
		}
		if !found || strings.TrimSpace(file.PickCode) == "" {
			attemptErrors = append(attemptErrors, fmt.Errorf("%s 未返回有效 pickcode", method))
			continue
		}
		if len(attemptErrors) > 0 && h.logger != nil {
			h.logger.Warnf("[EMBY PROXY] Match302 pickcode 已降级到 %s mode=%s", method, mode)
		}
		return strings.TrimSpace(file.PickCode), nil
	}
	return "", fmt.Errorf("Match302 pickcode 解析失败 mode=%s: %w", mode, errors.Join(attemptErrors...))
}
