package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"film-fusion/app/model"
	sdk115 "github.com/OpenListTeam/115-sdk-go"
)

// Fetch into the journal before moving any cloud file. An asynchronous download
// cannot then outlive a failed upgrade and overwrite a restored subtitle.
func FetchUpgradeSubtitle(storage *model.CloudStorage, pickcode string, size int64) ([]byte, error) {
	if storage == nil || storage.AccessToken == "" || pickcode == "" || size <= 0 || size > 2*1024*1024 {
		return nil, errors.New("洗版字幕需要有效的 115 Open 令牌、下载标识和不超过 2 MiB 的文件")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := sdk115.New()
	client.SetAccessToken(storage.AccessToken)
	const userAgent = "Mozilla/5.0"
	urls, err := client.DownURL(ctx, pickcode, userAgent)
	if err != nil {
		return nil, fmt.Errorf("读取洗版字幕下载地址失败: %w", err)
	}
	if len(urls) != 1 {
		return nil, errors.New("字幕下载地址数量不符")
	}
	for _, entry := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL.URL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("字幕下载失败 HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != size {
			return nil, errors.New("字幕下载大小与云盘记录不符")
		}
		return data, nil
	}
	return nil, errors.New("字幕下载地址为空")
}
