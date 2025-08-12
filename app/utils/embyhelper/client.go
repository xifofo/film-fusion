package embyhelper

import (
	"film-fusion/app/config"
	"fmt"

	"resty.dev/v3"
)

// EmbyClient Emby客户端
type EmbyClient struct {
	config *config.Config
	client *resty.Client
}

// New 创建新的Emby客户端
func New(cfg *config.Config) *EmbyClient {
	client := resty.New()
	client.SetBaseURL(cfg.Emby.URL)
	client.SetQueryParam("api_key", cfg.Emby.APIKey)

	return &EmbyClient{
		config: cfg,
		client: client,
	}
}

// GetPlaybackInfo 获取播放信息
func (e *EmbyClient) GetPlaybackInfo(itemID string) ([]interface{}, error) {
	var response map[string]any

	resp, err := e.client.R().
		SetResult(&response).
		Get(fmt.Sprintf("/Items/%s/PlaybackInfo", itemID))

	if err != nil {
		return nil, fmt.Errorf("请求播放信息失败: %w", err)
	}

	if resp.StatusCode() != 200 {
		return nil, fmt.Errorf("获取播放信息失败，状态码: %d, 响应: %s", resp.StatusCode(), resp.String())
	}

	// 检查响应是否为空
	if response == nil {
		return nil, fmt.Errorf("播放信息响应为空")
	}

	// 检查 MediaSources 是否存在
	mediaSourcesRaw, exists := response["MediaSources"]
	if !exists {
		return nil, fmt.Errorf("响应中不包含 MediaSources 字段")
	}

	// 转换为 []any 类型
	mediaSources, ok := mediaSourcesRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("MediaSources 字段格式错误，无法转换为数组")
	}

	// 检查是否有媒体源
	if len(mediaSources) == 0 {
		return nil, fmt.Errorf("MediaSources 为空，itemID: %s", itemID)
	}

	// 记录调试信息
	fmt.Printf("成功获取媒体播放信息: ItemID=%s, MediaSources数量=%d\n", itemID, len(mediaSources))

	return mediaSources, nil
}
