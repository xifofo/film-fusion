package embyhelper

import (
	"context"
	"film-fusion/app/config"
	"fmt"
	"time"

	"resty.dev/v3"
)

const embyRequestTimeout = 30 * time.Second

// EmbyClient Emby客户端
type EmbyClient struct {
	config       *config.Config
	client       *resty.Client
	operationCtx context.Context
}

// New 创建新的Emby客户端
func New(cfg *config.Config) *EmbyClient {
	client := resty.New()
	client.SetTimeout(embyRequestTimeout)
	client.SetBaseURL(cfg.Emby.URL)
	client.SetQueryParam("api_key", cfg.Emby.APIKey)

	return &EmbyClient{
		config: cfg,
		client: client,
	}
}

// Close releases the client's HTTP resources. Only the owner should close it;
// request-scoped copies returned by WithContext share the same HTTP client.
func (e *EmbyClient) Close() error { return e.client.Close() }

// Reload 根据当前配置重新应用连接参数（地址/APIKey），用于配置热更新后无需重启即可生效。
// 由于各服务共享同一 *EmbyClient 指针，这里就地更新即可对所有调用方生效。
func (e *EmbyClient) Reload() {
	if e == nil || e.client == nil || e.config == nil {
		return
	}
	e.client.SetBaseURL(e.config.Emby.URL)
	e.client.SetQueryParam("api_key", e.config.Emby.APIKey)
}

// GetPlaybackInfo 获取播放信息
func (e *EmbyClient) GetPlaybackInfo(itemID string) ([]interface{}, error) {
	return e.GetPlaybackInfoContext(e.requestContext(), itemID)
}

func (e *EmbyClient) GetPlaybackInfoContext(ctx context.Context, itemID string) ([]interface{}, error) {
	var response map[string]any

	resp, err := e.client.R().
		SetContext(ctx).
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

func (e *EmbyClient) WithContext(ctx context.Context) *EmbyClient {
	if e == nil {
		return nil
	}
	scoped := *e
	scoped.operationCtx = ctx
	return &scoped
}
func (e *EmbyClient) requestContext() context.Context {
	if e != nil && e.operationCtx != nil {
		return e.operationCtx
	}
	return context.Background()
}
