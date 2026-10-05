package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"gorm.io/gorm"
)

type BalanceSourceFile struct {
	SourceFilePath string
	MatchedPath    string
	RelativePath   string
	FileName       string
	PickCode       string
	FileID         string
	SHA1           string
	Size           int64
	IsCollect      bool
	FileType       int
}

type balanceSourceFileResolver interface {
	NewClient(cookie string) (*driver.Pan115Client, error)
	ResolveFilePathWithClient(client *driver.Pan115Client, filePath string) (Web115File, bool, error)
	ResolveFilePathWithOpenAPI(ctx context.Context, accessToken, filePath string) (Web115File, bool, error)
}

type BalanceAssignmentService struct {
	logger           *logger.Logger
	web115Svc        *Web115Service
	directory115Svc  balanceDirectoryService
	sourceFile115Svc balanceSourceFileResolver
}

func NewBalanceAssignmentService(log *logger.Logger) *BalanceAssignmentService {
	web115Svc := NewWeb115Service(log)
	return &BalanceAssignmentService{
		logger:           log,
		web115Svc:        web115Svc,
		directory115Svc:  web115Svc,
		sourceFile115Svc: web115Svc,
	}
}

func (s *BalanceAssignmentService) FindMatch(filePath string) (*model.Match302, string, error) {
	var matches []model.Match302
	if err := database.DB.Preload("CloudStorage").
		Preload("PoolMembers.CloudStorage").
		Find(&matches).Error; err != nil {
		return nil, "", err
	}

	for i := range matches {
		match := &matches[i]
		if match.CloudStorage == nil || match.CloudStorage.StorageType != model.StorageType115Open {
			continue
		}
		matchedPath := match.GetMatchedPath(filePath)
		if matchedPath == filePath {
			continue
		}
		match.NormalizeBalanceDefaults()
		return match, matchedPath, nil
	}
	return nil, "", gorm.ErrRecordNotFound
}

func (s *BalanceAssignmentService) ResolveSourceFileInfo(ctx context.Context, match *model.Match302, sourcePath, matchedPath string) (BalanceSourceFile, error) {
	if match == nil {
		return BalanceSourceFile{}, fmt.Errorf("Match302 规则为空")
	}
	if match.CloudStorage == nil {
		var storage model.CloudStorage
		if err := database.DB.First(&storage, match.CloudStorageID).Error; err != nil {
			return BalanceSourceFile{}, fmt.Errorf("源账号不存在: %w", err)
		}
		match.CloudStorage = &storage
	}
	storage := match.CloudStorage
	mode := storage.Match302AccessModeValue()
	if !storage.HasMatch302AccessCredential() {
		switch mode {
		case model.Match302AccessModeOpenAPIOnly:
			return BalanceSourceFile{}, fmt.Errorf("源账号处于仅 OpenAPI 模式但 AccessToken 缺失")
		case model.Match302AccessModeCookieOnly:
			return BalanceSourceFile{}, fmt.Errorf("源账号处于仅 Cookie 模式但 Cookie 缺失")
		default:
			return BalanceSourceFile{}, fmt.Errorf("源账号 AccessToken 和 Cookie 均缺失")
		}
	}

	resolver := s.sourceFileResolver()
	var attemptErrors []error
	for _, method := range model.Match302AccessOrder(mode) {
		var (
			file  Web115File
			found bool
			err   error
		)
		switch method {
		case model.Match302AccessMethodOpenAPI:
			if strings.TrimSpace(storage.AccessToken) == "" {
				attemptErrors = append(attemptErrors, fmt.Errorf("OpenAPI AccessToken 缺失"))
				continue
			}
			file, found, err = resolver.ResolveFilePathWithOpenAPI(ctx, storage.AccessToken, matchedPath)
		case model.Match302AccessMethodCookie:
			if strings.TrimSpace(storage.Cookie) == "" {
				attemptErrors = append(attemptErrors, fmt.Errorf("Cookie 缺失"))
				continue
			}
			var webClient *driver.Pan115Client
			webClient, err = resolver.NewClient(storage.Cookie)
			if err == nil {
				file, found, err = resolver.ResolveFilePathWithClient(webClient, matchedPath)
			}
		}
		if err != nil {
			attemptErrors = append(attemptErrors, fmt.Errorf("%s 解析失败: %w", method, err))
			continue
		}
		if !found {
			attemptErrors = append(attemptErrors, fmt.Errorf("%s 未找到源文件", method))
			continue
		}
		if strings.TrimSpace(file.PickCode) == "" {
			attemptErrors = append(attemptErrors, fmt.Errorf("%s 返回的 pickcode 为空", method))
			continue
		}
		if len(attemptErrors) > 0 && s.logger != nil {
			s.logger.Warnf("[match302-balance] 源文件解析已降级到 %s mode=%s", method, mode)
		}
		fileName := strings.TrimSpace(file.Name)
		if fileName == "" {
			fileName = path.Base(pathhelper.EnsureLeadingSlash(matchedPath))
		}
		return buildBalanceSourceFile(sourcePath, matchedPath, match.TargetPath, fileName, file.PickCode, file.FileID, file.SHA1, file.Size), nil
	}
	return BalanceSourceFile{}, fmt.Errorf("源文件信息解析失败 mode=%s: %w", mode, errors.Join(attemptErrors...))
}

func (s *BalanceAssignmentService) sourceFileResolver() balanceSourceFileResolver {
	if s.sourceFile115Svc != nil {
		return s.sourceFile115Svc
	}
	return s.web115Svc
}

func buildBalanceSourceFile(sourcePath, matchedPath, targetPath, fileName, pickCode, fileID, sha1 string, size int64) BalanceSourceFile {
	return BalanceSourceFile{
		SourceFilePath: sourcePath,
		MatchedPath:    pathhelper.EnsureLeadingSlash(matchedPath),
		RelativePath:   relativeMediaPath(targetPath, matchedPath, fileName),
		FileName:       fileName,
		PickCode:       strings.TrimSpace(pickCode),
		FileID:         strings.TrimSpace(fileID),
		SHA1:           strings.ToUpper(strings.TrimSpace(sha1)),
		Size:           size,
	}
}
