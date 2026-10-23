// 秒传元数据补齐只查询源文件所在目录，避免将路径定位成功当成完整的上传信息。
package service

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"strings"
)

func validateTransferSourceInfo(info BalanceSourceFile) error {
	var problems []string
	sha1 := strings.TrimSpace(info.SHA1)
	if sha1 == "" {
		problems = append(problems, "SHA1 缺失")
	} else if hash, err := hex.DecodeString(sha1); err != nil || len(hash) != 20 {
		problems = append(problems, fmt.Sprintf("SHA1 格式错误（长度 %d，要求 40 位十六进制）", len(sha1)))
	}
	if info.Size <= 0 {
		problems = append(problems, fmt.Sprintf("文件大小无效（%d 字节）", info.Size))
	}
	if strings.TrimSpace(info.PickCode) == "" {
		problems = append(problems, "pickcode 缺失")
	}
	if len(problems) > 0 {
		return fmt.Errorf("源文件无法秒传：%s", strings.Join(problems, "；"))
	}
	return nil
}

func (s *Web115Service) completeOpenAPITransferSource(ctx context.Context, token string, source *BalanceSourceFile) error {
	filePath := source.MatchedPath
	if decoded, err := url.PathUnescape(filePath); err == nil {
		filePath = decoded
	}
	filePath = path.Clean("/" + strings.TrimPrefix(filePath, "/"))
	parent, name := path.Dir(filePath), path.Base(filePath)
	parentID := "0"
	if parent != "/" {
		info, err := s.newOpenAPIClient(token).GetFolderInfoByPath(ctx, parent)
		if err != nil {
			return err
		}
		parentID = strings.TrimSpace(info.FileID)
		if parentID == "" || parentID == "0" {
			return fmt.Errorf("源文件父目录未返回有效 ID")
		}
	}

	const limit = 1150
	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return err
		}
		list, err := s.GetDirectoryEntriesWithOpenAPI(ctx, token, parentID, offset, limit)
		if err != nil {
			return err
		}
		for _, file := range list.Items {
			if !file.IsFile || file.Name != name || (source.FileID != "" && file.FileID != source.FileID) {
				continue
			}
			if source.PickCode != "" && file.PickCode != "" && source.PickCode != file.PickCode {
				return fmt.Errorf("源文件 pickcode 在查询期间发生变化，请重试")
			}
			// 整体使用同一条文件记录，不能拼接不同版本的 SHA1 与大小。
			source.FileID = file.FileID
			source.PickCode = strings.TrimSpace(file.PickCode)
			source.SHA1 = strings.ToUpper(strings.TrimSpace(file.SHA1))
			source.Size = file.Size
			return validateTransferSourceInfo(*source)
		}
		offset += len(list.Items)
		if len(list.Items) == 0 || (list.Total > 0 && int64(offset) >= list.Total) || (list.Total <= 0 && len(list.Items) < limit) {
			return fmt.Errorf("源文件父目录中未找到名称和 ID 一致的文件")
		}
	}
}
