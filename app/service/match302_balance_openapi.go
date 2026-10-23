package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"film-fusion/app/model"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

// Select the target protocol before any writes. An ambiguous upload failure
// must not trigger a second upload through a different protocol.
func useOpenAPITransfer(storage *model.CloudStorage) bool {
	return storage != nil && (storage.Match302AccessModeValue() == model.Match302AccessModeOpenAPIOnly ||
		(storage.Match302AccessModeValue() == model.Match302AccessModeAuto && strings.TrimSpace(storage.AccessToken) != ""))
}

func (s *BalanceAssignmentService) prepareTransferSource(ctx context.Context, assignment *model.Match302BalanceAssignment) (BalanceSourceFile, error) {
	matchedPath := assignment.Match302.GetMatchedPath(assignment.SourceFilePath)
	info, err := s.resolveSourceFileInfo(ctx, assignment.Match302, assignment.SourceFilePath, matchedPath, true)
	if err != nil {
		return info, err
	}
	if info.Size >= 115*1024*1024 && isCollectLike(info) {
		return info, fmt.Errorf("特殊资源或合集资源暂不支持秒传")
	}
	if err := s.updateAssignmentSourceInfo(assignment, info); err != nil {
		return info, err
	}
	return info, nil
}

func (s *BalanceAssignmentService) transferSingleOpenAPI(ctx context.Context, assignment *model.Match302BalanceAssignment) (transferSingleResult, error) {
	target := assignment.PlaybackStorage
	if strings.TrimSpace(target.AccessToken) == "" {
		return transferSingleResult{}, fmt.Errorf("目标账号处于仅 OpenAPI 模式但 AccessToken 缺失")
	}
	sourceInfo, err := s.prepareTransferSource(ctx, assignment)
	if err != nil {
		return transferSingleResult{}, err
	}
	if err := s.cleanupStorageCacheForQuota(ctx, assignment); err != nil {
		return transferSingleResult{}, err
	}
	targetRoot := s.targetRootPath(assignment.Match302ID, assignment.PlaybackStorageID)
	targetDir, targetName := splitTargetPath(targetRoot, sourceInfo.RelativePath)
	targetDirID, err := s.ensureDirPath(ctx, nil, model.Match302AccessModeOpenAPIOnly, target.AccessToken, targetDir)
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("OpenAPI 创建目标目录失败: %w", err)
	}
	client := s.web115Svc.newOpenAPIClient(target.AccessToken)
	existing, found, err := findOpenAPITransferFile(ctx, client, targetDirID, targetName, "")
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("OpenAPI 查询目标文件失败: %w", err)
	}
	if found {
		return checkedOpenAPITransferResult(existing, sourceInfo, joinCloudPath(targetDir, targetName))
	}
	result, err := rapidUploadOpenAPI(ctx, client, &sdk115.UploadInitReq{
		FileName: targetName, FileSize: sourceInfo.Size, Target: targetDirID,
		FileID: strings.ToUpper(sourceInfo.SHA1), TopUpload: "1",
	}, func(signCheck string) (string, error) {
		return s.web115Svc.readTransferRangeHash(ctx, assignment.Match302.CloudStorage, sourceInfo, signCheck)
	})
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("OpenAPI 秒传失败: %w", err)
	}
	if strings.TrimSpace(result.FileID) != "" && strings.TrimSpace(result.PickCode) != "" {
		return transferSingleResult{
			FileID: result.FileID, PickCode: result.PickCode, TargetPath: joinCloudPath(targetDir, targetName),
			SHA1: sourceInfo.SHA1, Size: sourceInfo.Size,
		}, nil
	}
	// A successful init may omit file_id. Resolve the target account's own
	// receipt, never reuse the source account's file ID or pickcode.
	existing, found, err = findOpenAPITransferFile(ctx, client, targetDirID, targetName, result.PickCode)
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("OpenAPI 秒传已命中，但回查目标文件失败: %w", err)
	}
	if !found {
		return transferSingleResult{}, fmt.Errorf("OpenAPI 秒传已命中，但目标文件尚不可见，请稍后重试")
	}
	return checkedOpenAPITransferResult(existing, sourceInfo, joinCloudPath(targetDir, targetName))
}

type openAPIUploadInitializer interface {
	UploadInit(context.Context, *sdk115.UploadInitReq) (*sdk115.UploadInitResp, error)
}

func rapidUploadOpenAPI(ctx context.Context, client openAPIUploadInitializer, req *sdk115.UploadInitReq, readRangeHash web115RangeHashFunc) (*sdk115.UploadInitResp, error) {
	request := *req
	seen := make(map[string]bool)
	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := client.UploadInit(ctx, &request)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, fmt.Errorf("秒传初始化响应为空")
		}
		switch result.Status {
		case 2:
			return result, nil
		case 1:
			return nil, fmt.Errorf("秒传未命中，需要完整上传；本任务仅执行秒传")
		case 7:
			challenge := result.SignKey + ":" + result.SignCheck
			if result.SignKey == "" || result.SignCheck == "" || seen[challenge] || readRangeHash == nil {
				return nil, fmt.Errorf("秒传二次验证参数缺失或重复")
			}
			seen[challenge] = true
			value, err := readRangeHash(result.SignCheck)
			if err != nil {
				return nil, fmt.Errorf("秒传二次验证 range hash 失败: %w", err)
			}
			decoded, err := hex.DecodeString(value)
			if err != nil || len(decoded) != 20 {
				return nil, fmt.Errorf("秒传二次验证 range hash 无效")
			}
			request.SignKey, request.SignVal = result.SignKey, strings.ToUpper(value)
		default:
			return nil, fmt.Errorf("秒传返回未知状态 status=%d", result.Status)
		}
	}
	return nil, fmt.Errorf("秒传二次验证次数超限")
}

func findOpenAPITransferFile(ctx context.Context, client *sdk115.Client, dirID, name, pickCode string) (Web115File, bool, error) {
	const limit = 1150
	for offset := int64(0); ; {
		result, err := client.GetFiles(ctx, &sdk115.GetFilesReq{
			CID: dirID, Limit: limit, Offset: offset, Cur: 1, O: "file_name", ASC: true,
		})
		if err != nil {
			return Web115File{}, false, err
		}
		for _, file := range result.Data {
			if file.Fc == "1" && file.Fn == name && (pickCode == "" || file.Pc == pickCode) {
				return Web115File{FileID: file.Fid, Name: file.Fn, PickCode: file.Pc,
					SHA1: file.Sha1, Size: file.FS, IsFile: true, Complete: file.Fta == "1"}, true, nil
			}
		}
		offset += int64(len(result.Data))
		if len(result.Data) == 0 || offset >= result.Count {
			return Web115File{}, false, nil
		}
	}
}

func checkedOpenAPITransferResult(file Web115File, source BalanceSourceFile, targetPath string) (transferSingleResult, error) {
	if !strings.EqualFold(file.SHA1, source.SHA1) || file.Size != source.Size {
		return transferSingleResult{}, fmt.Errorf("目标路径已存在同名文件但 sha1/size 不一致: %s", file.Name)
	}
	if !file.Complete || strings.TrimSpace(file.FileID) == "" || strings.TrimSpace(file.PickCode) == "" {
		return transferSingleResult{}, fmt.Errorf("目标文件尚未上传完成或缺少文件 ID/pickcode")
	}
	return transferSingleResult{FileID: file.FileID, PickCode: file.PickCode, TargetPath: targetPath, SHA1: source.SHA1, Size: source.Size}, nil
}

func (s *Web115Service) readTransferRangeHash(ctx context.Context, source *model.CloudStorage, info BalanceSourceFile, signCheck string) (string, error) {
	start, end, err := parseWeb115Range(signCheck)
	if err != nil || info.Size <= 0 || end >= info.Size || end-start >= maxTransferRangeBytes {
		return "", fmt.Errorf("秒传校验范围无效: %q", signCheck)
	}
	var attemptErrors []error
	for _, method := range model.Match302AccessOrder(source.Match302AccessModeValue()) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var download *driver.DownloadInfo
		if method == model.Match302AccessMethodOpenAPI {
			download, err = s.downloadWithOpenAPIForRange(ctx, source.AccessToken, info.PickCode, web115TransferUA, &info)
		} else {
			var client *driver.Pan115Client
			client, err = s.newClientWithContext(ctx, source.Cookie, 0, s.transferHTTPClient())
			if err == nil {
				download, err = s.DownloadForP115Transfer(ctx, model.Match302AccessModeCookieOnly, "", client, info)
			}
		}
		if err == nil {
			var digest string
			digest, err = hashTransferHTTPRange(ctx, s.transferHTTPClient(), download.Url.Url, download.Header, info.Size, start, end)
			if err == nil {
				return digest, nil
			}
		}
		attemptErrors = append(attemptErrors, fmt.Errorf("%s: %w", method, err))
	}
	return "", errors.Join(attemptErrors...)
}
