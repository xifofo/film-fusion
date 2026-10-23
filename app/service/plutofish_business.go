package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"film-fusion/app/utils/pathhelper"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PlutoFishAccountMapping explicitly binds a PlutoFish receiving account to
// this FilmFusion user's CloudPath. These IDs are not 115 provider UIDs.
type PlutoFishAccountMapping struct {
	TargetAccountID string `json:"targetAccountID"`
	CloudPathID     uint   `json:"cloudPathID"`
}

type PlutoFishBusinessOptions struct {
	UserID   uint                      `json:"-"`
	Mappings []PlutoFishAccountMapping `json:"mappings"`
}

type PlutoFishBusinessResult struct {
	LocalPath string `json:"localPath"`
	Message   string `json:"message"`
}

// PlutoFishBusiness performs only local reads and writes. It deliberately
// avoids StrmService scanning/download methods, which may query 115 or replace
// files. Playback uses the same CloudPath prefix/source/encoding conventions.
type PlutoFishBusiness struct {
	db      *gorm.DB
	options PlutoFishBusinessOptions
}

func NewPlutoFishBusiness(db *gorm.DB, options PlutoFishBusinessOptions) *PlutoFishBusiness {
	options.Mappings = append([]PlutoFishAccountMapping(nil), options.Mappings...)
	return &PlutoFishBusiness{db: db, options: options}
}

// Handle returns an error whenever the durable inbox must remain pending.
// Success proves a local file is durable, not playback or an Emby refresh.
// scope must include the normalized service address and the owner.
func (b *PlutoFishBusiness) Handle(ctx context.Context, scope string, event plutofish.Event) (PlutoFishBusinessResult, error) {
	result := PlutoFishBusinessResult{}
	fail := func(message string) (PlutoFishBusinessResult, error) { return result, errors.New(message) }
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if event.ReviewReason() != "" || event.Type != "transfer.succeeded" || event.ID == "" || strings.TrimSpace(scope) == "" {
		return fail("通知尚不满足成功处理条件，已保留待核对")
	}
	if b.db == nil || b.options.UserID == 0 {
		return fail("尚未配置 FilmFusion 本地业务所属用户")
	}
	if event.Provider != "115" {
		return fail("现有 STRM 业务仅支持显式映射的 115 网盘通知")
	}
	var mappingID uint
	for _, mapping := range b.options.Mappings {
		if mapping.TargetAccountID != "" && mapping.TargetAccountID == event.TargetAccountID {
			if mappingID != 0 || mapping.CloudPathID == 0 {
				return fail("接收网盘映射重复或无效，请保留唯一的云路径映射")
			}
			mappingID = mapping.CloudPathID
		}
	}
	if mappingID == 0 {
		return fail("接收网盘尚未绑定 FilmFusion 云路径映射")
	}
	var mapping model.CloudPath
	if err := b.db.WithContext(ctx).Where("id = ? AND user_id = ?", mappingID, b.options.UserID).First(&mapping).Error; err != nil {
		return fail("无法读取当前用户的云路径映射，请检查映射是否存在")
	}
	var storage model.CloudStorage
	if err := b.db.WithContext(ctx).Select("id", "user_id", "storage_type", "status").Where("id = ? AND user_id = ?", mapping.CloudStorageID, b.options.UserID).First(&storage).Error; err != nil {
		return fail("云路径关联的网盘不存在或不属于当前用户")
	}
	if storage.StorageType != model.StorageType115Open || storage.Status != model.StatusActive || mapping.LinkType != model.LinkTypeStrm {
		return fail("云路径必须使用当前用户的启用状态 115 网盘及 STRM 类型")
	}
	if !plutoFishFileComponent(event.FileName) || !plutoFishText(event.TargetDirectoryPath) || !strings.HasPrefix(event.TargetDirectoryPath, "/") || strings.ContainsRune(event.TargetDirectoryPath, '\\') {
		return fail("缺少有效的目标目录完整路径或文件名；未查询网盘，未猜测播放地址")
	}
	directory, err := pathhelper.NormalizeUntrustedPath(event.TargetDirectoryPath)
	if err != nil {
		return fail("目标目录路径包含不安全的路径片段")
	}
	cloudFile := path.Join(directory, event.FileName)
	source, err := pathhelper.NormalizeUntrustedPath(mapping.SourcePath)
	if err != nil {
		return fail("云路径的源目录配置无效")
	}
	actualSource, eventPath := source, cloudFile
	switch mapping.SourceType {
	case model.SourceTypeCloudDrive2:
		if source == "/" {
			return fail("CloudDrive2 源目录必须包含明确的挂载名称")
		}
		mount := strings.Split(strings.TrimPrefix(source, "/"), "/")[0]
		actualSource = pathhelper.EnsureLeadingSlash(pathhelper.RemoveFirstDir(source))
		eventPath = path.Join("/", mount, cloudFile)
	case model.SourceTypeMoviePilot2:
	default:
		return fail("云路径的源类型不受现有 STRM 业务支持")
	}
	if !pathhelper.IsSubPath(cloudFile, actualSource) {
		return fail("转存文件不在所绑定的云路径源目录范围内")
	}
	var rules model.FilterRule
	filter := strings.TrimSpace(mapping.FilterRules)
	if filter != "" && json.Unmarshal([]byte(filter), &rules) != nil {
		return fail("云路径过滤规则无效，请修正后重试")
	}
	if (len(rules.Download) > 0 && pathhelper.IsFileMatchedByFilter(cloudFile, filter, "download")) || !pathhelper.IsFileMatchedByFilter(cloudFile, filter, "include") {
		return fail("文件被云路径规则排除或属于下载类文件，已保留待处理且未执行下载")
	}
	rootPath := strings.TrimSpace(mapping.LocalPath)
	if !filepath.IsAbs(rootPath) {
		return fail("云路径未配置有效的本地绝对路径")
	}
	rootPath = filepath.Clean(rootPath)
	prefix := strings.TrimSpace(mapping.ContentPrefix)
	if !plutoFishContentPrefix(prefix) {
		return fail("云路径未配置有效的播放内容前缀；请使用已有的绝对路径或 HTTP(S) 地址")
	}
	if mapping.ContentEncodeURI {
		parts := strings.Split(eventPath, "/")
		for i, part := range parts {
			parts[i] = url.PathEscape(part)
		}
		eventPath = strings.Join(parts, "/")
	} else if strings.HasPrefix(prefix, "http://") || strings.HasPrefix(prefix, "https://") {
		if strings.ContainsAny(eventPath, "?#%") {
			return fail("目标路径包含 URL 特殊字符，请在云路径中启用内容 URI 编码后重试")
		}
	}
	content := pathhelper.SafeFilePathJoin(prefix, eventPath)
	if !plutoFishText(content) {
		return fail("生成的 STRM 内容无效，已保留待处理")
	}
	name := strings.TrimSuffix(event.FileName, filepath.Ext(event.FileName)) + ".strm"
	relativeDir := filepath.Join("PlutoFish", plutoFishDigest(scope), plutoFishDigest(event.TransferID))
	relative := filepath.Join(relativeDir, name)
	result.LocalPath = filepath.Join(rootPath, relative)
	// Pin a validated plan independently of the current mapping before touching
	// disk. Without this, a changed LocalPath between a crash and receipt commit
	// could generate a second file outside the original manifest's directory.
	plan := model.PlutoFishBusinessPlan{Scope: scope, TransferID: event.TransferID, RootPath: rootPath, RelativePath: relative, Content: content}
	if err := b.claimPlan(ctx, plan); err != nil {
		if errors.Is(err, errPlutoFishPlanConflict) {
			return fail("此转存任务已绑定不同的本地输出路径或内容；已保留原计划，请恢复原映射后重试或人工核对")
		}
		return fail("无法可靠保存本地 STRM 处理计划，未写入文件，请稍后重试")
	}
	// A stable manifest prevents a changed filename after a crash from creating
	// another output for the same transfer before inbox deduplication commits.
	manifest, _ := json.Marshal(struct {
		FileName string `json:"fileName"`
		Content  string `json:"content"`
	}{name, content})
	if err := plutoFishWriteOnce(ctx, rootPath, filepath.Join(relativeDir, ".transfer.json"), string(manifest)); err != nil {
		if errors.Is(err, errPlutoFishSTRMConflict) {
			return fail("此转存任务已有不同的本地 STRM 记录，已保留原文件并暂停处理")
		}
		return fail("无法可靠写入本地 STRM 任务记录，请检查目录权限后重试")
	}
	if err := plutoFishWriteOnce(ctx, rootPath, relative, content); err != nil {
		if errors.Is(err, errPlutoFishSTRMConflict) {
			return fail("本地 STRM 已存在且内容不同，已保留原文件")
		}
		return fail("本地 STRM 写入未完成，请检查目录权限后重试")
	}
	result.Message = "本地 STRM 已生成或已存在相同内容；未执行网盘请求或媒体库刷新"
	return result, nil
}

var errPlutoFishPlanConflict = errors.New("existing PlutoFish business plan differs")

func (b *PlutoFishBusiness) claimPlan(ctx context.Context, plan model.PlutoFishBusinessPlan) error {
	return b.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope"}, {Name: "transfer_id"}}, DoNothing: true}).Create(&plan).Error; err != nil {
			return err
		}
		var existing model.PlutoFishBusinessPlan
		if err := tx.Where("scope = ? AND transfer_id = ?", plan.Scope, plan.TransferID).Take(&existing).Error; err != nil {
			return err
		}
		if existing.RootPath != plan.RootPath || existing.RelativePath != plan.RelativePath || existing.Content != plan.Content {
			return errPlutoFishPlanConflict
		}
		return nil
	})
}

func plutoFishDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func plutoFishText(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func plutoFishFileComponent(value string) bool {
	return plutoFishText(value) && value != "." && value != ".." && !strings.ContainsAny(value, `/\`)
}

func plutoFishContentPrefix(value string) bool {
	if !plutoFishText(value) {
		return false
	}
	if filepath.IsAbs(value) {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

var errPlutoFishSTRMConflict = errors.New("existing PlutoFish STRM content differs")

// Publish a fsynced complete file with an atomic hard link, never replacing an
// existing file. os.Root confines relative operations, including symlinks.
func plutoFishWriteOnce(ctx context.Context, rootPath, relative, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsLocal(relative) {
		return pathhelper.ErrUnsafePath
	}
	if err := os.MkdirAll(rootPath, 0755); err != nil {
		return err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	if exists, err := plutoFishExistingContent(root, relative, content); exists || err != nil {
		if err != nil {
			return err
		}
		return plutoFishSyncDirectories(root, filepath.Dir(relative))
	}
	if err := root.MkdirAll(filepath.Dir(relative), 0755); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(relative), ".plutofish-"+hex.EncodeToString(nonce[:]))
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err = io.WriteString(file, content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Link(temporary, relative); err != nil {
		if !os.IsExist(err) {
			return err
		}
		if exists, existingErr := plutoFishExistingContent(root, relative, content); !exists || existingErr != nil {
			if existingErr != nil {
				return existingErr
			}
			return err
		}
	}
	if err := root.Remove(temporary); err != nil {
		return err
	}
	return plutoFishSyncDirectories(root, filepath.Dir(relative))
}

func plutoFishExistingContent(root *os.Root, relative, content string) (bool, error) {
	info, err := root.Lstat(relative)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return true, errPlutoFishSTRMConflict
	}
	file, err := root.Open(relative)
	if err != nil {
		return true, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(len(content))+1))
	if err != nil {
		return true, err
	}
	if string(data) != content {
		return true, errPlutoFishSTRMConflict
	}
	return true, nil
}

func plutoFishSyncDirectories(root *os.Root, relative string) error {
	for {
		dir, err := root.Open(relative)
		if err != nil {
			return err
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if relative == "." {
			return nil
		}
		relative = filepath.Dir(relative)
	}
}
