package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"film-fusion/app/model"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
)

func (s *Web115Service) cleanupTransferFileOpenAPI(ctx context.Context, assignment *model.Match302BalanceAssignment) error {
	storage := assignment.PlaybackStorage
	if strings.TrimSpace(storage.AccessToken) == "" {
		return fmt.Errorf("目标账号 OpenAPI AccessToken 缺失")
	}
	client := s.newOpenAPIClient(storage.AccessToken)
	fileID := strings.TrimSpace(assignment.TargetFileID)
	hardDelete := assignment.Match302 != nil && assignment.Match302.CleanupMode == model.Match302CleanupModeHardDelete
	if hardDelete {
		// Resume a prior cleanup which moved the file to the recycle bin but
		// failed to permanently delete it. Never delete the whole recycle bin.
		tid, err := findOpenAPIRecycleItem(ctx, client, assignment)
		if err != nil {
			return err
		}
		if tid != "" {
			_, err = client.RbDelete(ctx, tid)
			return err
		}
	}
	if fileID == "" && strings.TrimSpace(assignment.TargetPath) != "" {
		file, found, err := s.ResolveFilePathWithOpenAPI(ctx, storage.AccessToken, assignment.TargetPath)
		if err != nil {
			return fmt.Errorf("OpenAPI 回查待清理文件失败: %w", err)
		}
		if !found || file.PickCode != assignment.TargetPickcode || !strings.EqualFold(file.SHA1, assignment.SHA1) || file.Size != assignment.Size {
			return fmt.Errorf("待清理文件与秒传记录不一致")
		}
		fileID = strings.TrimSpace(file.FileID)
	}
	if fileID == "" {
		return fmt.Errorf("目标文件 ID 缺失")
	}
	if _, err := client.DelFile(ctx, &sdk115.DelFileReq{FileIDs: fileID}); err != nil {
		return err
	}
	if !hardDelete {
		return nil
	}
	tid, err := findOpenAPIRecycleItem(ctx, client, assignment)
	if err != nil {
		return fmt.Errorf("已移入回收站，但查询回收站失败: %w", err)
	}
	if tid == "" {
		return fmt.Errorf("已移入回收站，但未找到匹配的回收站条目")
	}
	if _, err := client.RbDelete(ctx, tid); err != nil {
		return fmt.Errorf("彻底删除回收站条目失败: %w", err)
	}
	return nil
}

func findOpenAPIRecycleItem(ctx context.Context, client *sdk115.Client, assignment *model.Match302BalanceAssignment) (string, error) {
	// v0.2.2's RbList writes to an uninitialized Files map. Decode the same
	// object here; deletion uses the entry's id, never its collection index.
	const limit = 1150
	for offset := 0; offset < 3*limit; {
		var raw map[string]json.RawMessage
		_, err := client.AuthRequest(ctx, sdk115.ApiFsRbList, http.MethodGet, &raw,
			sdk115.ReqWithQuery(sdk115.Form{"offset": strconv.Itoa(offset), "limit": strconv.Itoa(limit)}))
		if err != nil {
			return "", err
		}
		items := 0
		for key, value := range raw {
			switch key {
			case "offset", "limit", "count", "rb_pass":
				continue
			}
			var file sdk115.RbListResp_FileInfo
			if err := json.Unmarshal(value, &file); err != nil {
				return "", err
			}
			items++
			matches := assignment.TargetPickcode != "" && file.PickCode == assignment.TargetPickcode
			if assignment.TargetPickcode == "" {
				matches = assignment.TargetFileID != "" && file.ID == assignment.TargetFileID
			}
			if tid := strings.TrimSpace(file.ID); matches && tid != "" && tid != "0" {
				return tid, nil
			}
		}
		offset += items
		count, _ := strconv.Atoi(strings.Trim(string(raw["count"]), `"`))
		if items == 0 || (count > 0 && offset >= count) || (count == 0 && items < limit) {
			break
		}
	}
	return "", nil
}
