package config

import "fmt"

const DefaultBoundAccountWaitSeconds = 10

// 为代理服务器的 30 秒响应超时预留文件定位与直链获取时间。
const MaxBoundAccountWaitSeconds = 20

// EffectiveBoundAccountWaitSeconds 兼容尚未配置此字段的旧配置和内存调用方。
func (c EmbyConfig) EffectiveBoundAccountWaitSeconds() int {
	if c.BoundAccountWaitSeconds == 0 {
		return DefaultBoundAccountWaitSeconds
	}
	return c.BoundAccountWaitSeconds
}

// ValidateBoundAccountWaitSeconds 校验指定账号等待时长；写入接口要求显式提供有效秒数。
func ValidateBoundAccountWaitSeconds(seconds int) error {
	if seconds < 1 || seconds > MaxBoundAccountWaitSeconds {
		return fmt.Errorf("指定账号等待超时必须为 1–%d 秒", MaxBoundAccountWaitSeconds)
	}
	return nil
}
