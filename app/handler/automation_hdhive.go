package handler

import (
	"context"
	"strings"

	"film-fusion/app/service"
)

func (h *HDHiveHandler) QueryAutomationHDHive(ctx context.Context, mediaType, tmdbID string) ([]service.AutomationHDHiveResource, error) {
	client, err := h.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.QueryResources(ctx, strings.TrimSpace(mediaType), strings.TrimSpace(tmdbID))
	if err != nil && shouldRefreshHDHiveToken(err) {
		if refreshed, refreshErr := h.refreshClient(ctx, "自动化查询资源时 access token 过期"); refreshErr == nil {
			resp, err = refreshed.QueryResources(ctx, strings.TrimSpace(mediaType), strings.TrimSpace(tmdbID))
		} else {
			err = refreshErr
		}
	}
	if err != nil {
		return nil, err
	}
	result := make([]service.AutomationHDHiveResource, 0, len(resp.Data))
	for _, resource := range resp.Data {
		mapped := service.AutomationHDHiveResource{
			Slug: resource.Slug, MediaURL: resource.MediaURL,
			VideoResolution:  append([]string(nil), resource.VideoResolution...),
			Source:           append([]string(nil), resource.Source...),
			SubtitleLanguage: append([]string(nil), resource.SubtitleLanguage...),
			IsUnlocked:       resource.IsUnlocked,
		}
		if resource.Title != nil {
			mapped.Title = *resource.Title
		}
		if resource.PanType != nil {
			mapped.PanType = *resource.PanType
		}
		if resource.ShareSize != nil {
			mapped.ShareSize = *resource.ShareSize
		}
		if resource.UnlockPoints != nil {
			mapped.UnlockPoints = *resource.UnlockPoints
		}
		result = append(result, mapped)
	}
	return result, nil
}

func (h *HDHiveHandler) UnlockAutomationHDHive(ctx context.Context, slug string) (service.AutomationHDHiveUnlockResult, error) {
	result := service.AutomationHDHiveUnlockResult{}
	client, err := h.client()
	if err != nil {
		return result, err
	}
	resp, err := client.UnlockResource(ctx, strings.TrimSpace(slug))
	if err != nil && shouldRefreshHDHiveToken(err) {
		if refreshed, refreshErr := h.refreshClient(ctx, "自动化解锁资源时 access token 过期"); refreshErr == nil {
			resp, err = refreshed.UnlockResource(ctx, strings.TrimSpace(slug))
		} else {
			err = refreshErr
		}
	}
	if err != nil {
		return result, err
	}
	return service.AutomationHDHiveUnlockResult{
		URL: resp.Data.URL, AccessCode: resp.Data.AccessCode, FullURL: resp.Data.FullURL,
		AlreadyOwned: resp.Data.AlreadyOwned,
	}, nil
}
