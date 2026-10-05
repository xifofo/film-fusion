package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

type balanceDirectoryService interface {
	ResolveDirPathWithClient(client *driver.Pan115Client, dir string) (string, bool, error)
	GetDirectoriesWithClient(client *driver.Pan115Client, cid string, offset, limit int) (Web115ListResult, error)
	GetDirectoriesWithOpenAPI(ctx context.Context, accessToken, cid string, offset, limit int) (Web115ListResult, error)
	MkdirWithClient(client *driver.Pan115Client, parentID, name string) (string, error)
	MkdirWithOpenAPI(ctx context.Context, accessToken, parentID, name string) (string, error)
}

func (s *BalanceAssignmentService) targetRootPath(matchID, storageID uint) string {
	var member model.Match302BalanceMember
	if err := database.DB.Where("match302_id = ? AND cloud_storage_id = ?", matchID, storageID).First(&member).Error; err == nil {
		if root := strings.TrimSpace(member.TargetRootPath); root != "" {
			return pathhelper.EnsureLeadingSlash(root)
		}
	}
	return model.DefaultMatch302BalanceTargetRoot(matchID)
}

func (s *BalanceAssignmentService) ensureDirPath(ctx context.Context, client *driver.Pan115Client, accessMode, accessToken, dirPath string) (string, error) {
	dirPath = path.Clean(pathhelper.EnsureLeadingSlash(dirPath))
	if dirPath == "/" || dirPath == "." {
		return "0", nil
	}
	directorySvc := s.directoryService()
	accessMode = model.NormalizeMatch302AccessMode(accessMode)
	if accessMode != model.Match302AccessModeOpenAPIOnly {
		if cid, ok, err := directorySvc.ResolveDirPathWithClient(client, dirPath); err == nil && ok {
			return cid, nil
		} else if err != nil {
			s.warnDirectoryFallback("Cookie 完整路径查询失败，回退逐级查询 path=%s err=%s", dirPath, compact115Error(err))
		}
	}

	currentID := "0"
	for _, segment := range splitCloudPath(dirPath) {
		list, err := s.getDirectoriesWithFallback(ctx, directorySvc, client, accessMode, accessToken, currentID)
		if err != nil {
			return "", err
		}
		found := findDirectoryID(list, segment)
		if found == "" {
			created, err := s.mkdirWithFallback(ctx, directorySvc, client, accessMode, accessToken, currentID, segment)
			if err != nil {
				return "", err
			}
			found = created
		}
		currentID = found
	}
	return currentID, nil
}

func (s *BalanceAssignmentService) directoryService() balanceDirectoryService {
	if s.directory115Svc != nil {
		return s.directory115Svc
	}
	return s.web115Svc
}

func (s *BalanceAssignmentService) getDirectoriesWithFallback(
	ctx context.Context,
	directorySvc balanceDirectoryService,
	client *driver.Pan115Client,
	accessMode, accessToken, parentID string,
) (Web115ListResult, error) {
	accessMode = model.NormalizeMatch302AccessMode(accessMode)
	if accessMode == model.Match302AccessModeOpenAPIOnly {
		if strings.TrimSpace(accessToken) == "" {
			return Web115ListResult{}, fmt.Errorf("仅 OpenAPI 模式下目标账号 AccessToken 为空")
		}
		return directorySvc.GetDirectoriesWithOpenAPI(ctx, accessToken, parentID, 0, int(driver.MaxDirPageLimit))
	}
	list, cookieErr := directorySvc.GetDirectoriesWithClient(client, parentID, 0, int(driver.MaxDirPageLimit))
	if cookieErr == nil {
		return list, nil
	}

	if accessMode == model.Match302AccessModeCookieOnly || strings.TrimSpace(accessToken) == "" {
		return Web115ListResult{}, fmt.Errorf(
			"Cookie 查询目录失败且当前模式不允许回退 Open API: %s",
			compact115Error(cookieErr),
		)
	}
	openList, openErr := directorySvc.GetDirectoriesWithOpenAPI(ctx, accessToken, parentID, 0, int(driver.MaxDirPageLimit))
	if openErr != nil {
		return Web115ListResult{}, fmt.Errorf(
			"Cookie 查询目录失败: %s；Open API 查询目录失败: %w",
			compact115Error(cookieErr),
			openErr,
		)
	}
	s.warnDirectoryFallback(
		"Cookie 查询目录失败，已使用 Open API 回退 parent_id=%s err=%s",
		parentID,
		compact115Error(cookieErr),
	)
	return openList, nil
}

func (s *BalanceAssignmentService) mkdirWithFallback(
	ctx context.Context,
	directorySvc balanceDirectoryService,
	client *driver.Pan115Client,
	accessMode, accessToken, parentID, name string,
) (string, error) {
	accessMode = model.NormalizeMatch302AccessMode(accessMode)
	if accessMode == model.Match302AccessModeOpenAPIOnly {
		if strings.TrimSpace(accessToken) == "" {
			return "", fmt.Errorf("仅 OpenAPI 模式下目标账号 AccessToken 为空")
		}
		return directorySvc.MkdirWithOpenAPI(ctx, accessToken, parentID, name)
	}
	cid, cookieErr := directorySvc.MkdirWithClient(client, parentID, name)
	if cookieErr == nil && strings.TrimSpace(cid) != "" {
		return strings.TrimSpace(cid), nil
	}
	if cookieErr == nil {
		cookieErr = fmt.Errorf("Cookie 创建目录成功但 cid 为空")
	}
	if is115DirectoryExistsError(cookieErr) {
		return s.resolveExistingDirectory(ctx, directorySvc, client, accessMode, accessToken, parentID, name)
	}
	if accessMode == model.Match302AccessModeCookieOnly || strings.TrimSpace(accessToken) == "" {
		return "", fmt.Errorf(
			"Cookie 创建目录失败且当前模式不允许回退 Open API: %s",
			compact115Error(cookieErr),
		)
	}

	cid, openErr := directorySvc.MkdirWithOpenAPI(ctx, accessToken, parentID, name)
	if openErr == nil && strings.TrimSpace(cid) != "" {
		s.warnDirectoryFallback(
			"Cookie 创建目录失败，已使用 Open API 回退 parent_id=%s name=%q err=%s",
			parentID,
			name,
			compact115Error(cookieErr),
		)
		return strings.TrimSpace(cid), nil
	}
	if openErr == nil {
		openErr = fmt.Errorf("Open API 创建目录成功但 file_id 为空")
	}
	if is115DirectoryExistsError(openErr) {
		return s.resolveExistingDirectory(ctx, directorySvc, client, accessMode, accessToken, parentID, name)
	}
	return "", fmt.Errorf(
		"Cookie 创建目录失败: %s；Open API 创建目录失败: %w",
		compact115Error(cookieErr),
		openErr,
	)
}

func (s *BalanceAssignmentService) resolveExistingDirectory(
	ctx context.Context,
	directorySvc balanceDirectoryService,
	client *driver.Pan115Client,
	accessMode, accessToken, parentID, name string,
) (string, error) {
	list, err := s.getDirectoriesWithFallback(ctx, directorySvc, client, accessMode, accessToken, parentID)
	if err != nil {
		return "", fmt.Errorf("目录 %q 已存在，但重新查询目录失败: %w", name, err)
	}
	if cid := findDirectoryID(list, name); cid != "" {
		return cid, nil
	}
	return "", fmt.Errorf("目录 %q 已存在，但重新查询后未找到目录 ID", name)
}

func findDirectoryID(list Web115ListResult, name string) string {
	for _, item := range list.Items {
		if item.Name == name {
			return strings.TrimSpace(item.FileID)
		}
	}
	return ""
}

func is115DirectoryExistsError(err error) bool {
	if errors.Is(err, driver.ErrExist) {
		return true
	}
	var openErr *sdk115.Error
	return errors.As(err, &openErr) && openErr.Code == 20004
}

func compact115Error(err error) string {
	if err == nil {
		return ""
	}
	message := strings.Join(strings.Fields(strings.TrimSpace(err.Error())), " ")
	const maxRunes = 500
	runes := []rune(message)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return message
}

func (s *BalanceAssignmentService) warnDirectoryFallback(format string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Warnf(format, args...)
	}
}

func relativeMediaPath(targetRoot, matchedPath, fileName string) string {
	matched := path.Clean(pathhelper.EnsureLeadingSlash(matchedPath))
	root := path.Clean(pathhelper.EnsureLeadingSlash(targetRoot))
	relative := strings.TrimPrefix(matched, strings.TrimRight(root, "/")+"/")
	if relative == matched {
		relative = fileName
	}
	relative = strings.TrimLeft(relative, "/")
	if relative == "" || relative == "." {
		return fileName
	}
	return relative
}

func splitTargetPath(targetRoot, relative string) (dirPath, fileName string) {
	relative = strings.TrimLeft(path.Clean("/"+relative), "/")
	fileName = path.Base(relative)
	if fileName == "." || fileName == "/" {
		fileName = relative
	}
	dir := path.Dir(relative)
	if dir == "." || dir == "/" {
		return pathhelper.EnsureLeadingSlash(targetRoot), fileName
	}
	return path.Clean(pathhelper.EnsureLeadingSlash(path.Join(targetRoot, dir))), fileName
}

func joinCloudPath(dir, name string) string {
	return path.Clean(pathhelper.EnsureLeadingSlash(path.Join(dir, name)))
}

func splitCloudPath(p string) []string {
	parts := strings.Split(strings.Trim(path.Clean(pathhelper.EnsureLeadingSlash(p)), "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" && part != "." {
			out = append(out, part)
		}
	}
	return out
}
