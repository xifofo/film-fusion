// 通知配置发现通过服务端代理请求，已保存密钥只允许发回原服务地址。
package service

import (
	"context"
	"errors"
	"strings"

	"film-fusion/app/plutofish"
)

// Accounts 使用新输入或当前管理员已保存的密钥发现网盘，不保存配置或启动消费。
func (s *PlutoFishService) Accounts(ctx context.Context, baseURL, apiKey string, userID uint) (plutofish.ReceivingAccounts, error) {
	base, err := plutofish.NormalizeBaseURL(baseURL)
	if err != nil {
		return plutofish.ReceivingAccounts{}, err
	}
	if userID == 0 {
		return plutofish.ReceivingAccounts{}, errors.New("缺少本地管理员身份")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		old, err := s.load(ctx)
		if err != nil {
			return plutofish.ReceivingAccounts{}, err
		}
		if old.UserID != userID || old.Config.BaseURL != base {
			return plutofish.ReceivingAccounts{}, errors.New("请为当前服务地址重新填写 API Key")
		}
		apiKey = old.Config.APIKey
	}
	return plutofish.DiscoverAccounts(ctx, base, apiKey)
}
