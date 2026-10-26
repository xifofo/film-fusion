// PlutoFish 成功通知按已有云路径映射生成本地 STRM，处理计划与去重信息仅存数据库。
package service

import (
	"context"
	"crypto/rand"
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

// plan 只读取本地配置，供正常生成和历史修复共用路径校验与内容规则。
func (b *PlutoFishBusiness) plan(ctx context.Context, scope string, event plutofish.Event) (model.PlutoFishBusinessPlan, error) {
	result := model.PlutoFishBusinessPlan{}
	fail := func(message string) (model.PlutoFishBusinessPlan, error) { return result, errors.New(message) }
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
	if !plutoFishFileComponent(event.FileName) || !plutoFishText(event.TargetDirectoryPath) || !strings.HasPrefix(event.TargetDirectoryPath, "/") || strings.ContainsRune(event.TargetDirectoryPath, '\\') {
		return fail("缺少有效的目标目录完整路径或文件名；未查询网盘，未猜测播放地址")
	}
	directory, err := pathhelper.NormalizeUntrustedPath(event.TargetDirectoryPath)
	if err != nil {
		return fail("目标目录路径包含不安全的路径片段")
	}
	cloudFile := path.Join(directory, event.FileName)
	mapping, eventPath, err := b.selectMapping(ctx, event.TargetAccountID, cloudFile)
	if err != nil {
		return result, err
	}
	var storage model.CloudStorage
	if err := b.db.WithContext(ctx).Select("id", "user_id", "storage_type", "status").Where("id = ? AND user_id = ?", mapping.CloudStorageID, b.options.UserID).First(&storage).Error; err != nil {
		return fail("云路径关联的网盘不存在或不属于当前用户")
	}
	if storage.StorageType != model.StorageType115Open || storage.Status != model.StatusActive || mapping.LinkType != model.LinkTypeStrm {
		return fail("云路径必须使用当前用户的启用状态 115 网盘及 STRM 类型")
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
	// 本地目录与普通 STRM 使用同一份映射路径；URI 编码只影响播放内容。
	// SafeRelativePath 保留 CD2 挂载名和云盘目录层级，并限制写入在本地根目录内。
	relative, err := pathhelper.SafeRelativePath(eventPath)
	if err != nil {
		return fail("目标文件无法安全映射到本地 STRM 路径")
	}
	relative = strings.TrimSuffix(relative, filepath.Ext(relative)) + ".strm"

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

	return model.PlutoFishBusinessPlan{Scope: scope, TransferID: event.TransferID, RootPath: rootPath, RelativePath: relative, Content: content}, nil
}

// One receiving account may cover several libraries. Select the most specific
// matching source directory before applying its filters and output settings.
// Equal matches remain for review; configuration order must never decide a write.
func (b *PlutoFishBusiness) selectMapping(ctx context.Context, accountID, cloudFile string) (model.CloudPath, string, error) {
	fail := func(message string) (model.CloudPath, string, error) {
		return model.CloudPath{}, "", errors.New(message)
	}
	ids := []uint{}
	seen := map[uint]bool{}
	for _, entry := range b.options.Mappings {
		if entry.TargetAccountID == "" || entry.TargetAccountID != accountID {
			continue
		}
		if entry.CloudPathID == 0 || seen[entry.CloudPathID] {
			return fail("接收网盘映射重复或无效，请移除重复的云路径映射")
		}
		seen[entry.CloudPathID] = true
		ids = append(ids, entry.CloudPathID)
	}
	if len(ids) == 0 {
		return fail("接收网盘尚未绑定 FilmFusion 云路径映射")
	}
	var mappings []model.CloudPath
	if err := b.db.WithContext(ctx).Where("id IN ? AND user_id = ?", ids, b.options.UserID).Find(&mappings).Error; err != nil || len(mappings) != len(ids) {
		return fail("无法读取当前用户的云路径映射，请检查映射是否存在")
	}
	var selected model.CloudPath
	var eventPath string
	bestLength, matches := -1, 0
	for _, mapping := range mappings {
		source, err := pathhelper.NormalizeUntrustedPath(mapping.SourcePath)
		if err != nil {
			return fail("云路径的源目录配置无效")
		}
		actualSource, mappedPath := source, cloudFile
		switch mapping.SourceType {
		case model.SourceTypeCloudDrive2:
			if source == "/" {
				return fail("CloudDrive2 源目录必须包含明确的挂载名称")
			}
			mount := strings.Split(strings.TrimPrefix(source, "/"), "/")[0]
			actualSource = pathhelper.EnsureLeadingSlash(pathhelper.RemoveFirstDir(source))
			mappedPath = path.Join("/", mount, cloudFile)
		case model.SourceTypeMoviePilot2:
		default:
			return fail("云路径的源类型不受现有 STRM 业务支持")
		}
		if !pathhelper.IsSubPath(cloudFile, actualSource) {
			continue
		}
		if len(actualSource) > bestLength {
			selected, eventPath, bestLength, matches = mapping, mappedPath, len(actualSource), 1
		} else if len(actualSource) == bestLength {
			matches++
		}
	}
	if matches == 0 {
		return fail("转存文件不在所绑定的云路径源目录范围内")
	}
	if matches > 1 {
		return fail("转存目录同时匹配多条同等优先级的云路径映射，请调整源目录范围后重试")
	}
	return selected, eventPath, nil
}

// Handle 按持久化计划生成本地文件；成功不代表网盘播放或 Emby 刷新成功。
func (b *PlutoFishBusiness) Handle(ctx context.Context, scope string, event plutofish.Event) (PlutoFishBusinessResult, error) {
	plan, err := b.plan(ctx, scope, event)
	result := PlutoFishBusinessResult{LocalPath: filepath.Join(plan.RootPath, plan.RelativePath)}
	if err != nil {
		return PlutoFishBusinessResult{}, err
	}
	fail := func(message string) (PlutoFishBusinessResult, error) { return result, errors.New(message) }

	// 写文件前在数据库固定路径与内容，崩溃恢复时仍校验原计划；
	// 不再创建哈希任务目录或 .transfer.json，也不能因映射变化生成第二份文件。
	if err := b.claimPlan(ctx, plan); err != nil {
		if errors.Is(err, errPlutoFishPlanConflict) {
			return fail("此转存任务已绑定不同的本地输出路径或内容；已保留原计划，请核对历史文件和映射配置后再处理")
		}
		return fail("无法可靠保存本地 STRM 处理计划，未写入文件，请稍后重试")
	}

	if err := plutoFishWriteOnce(ctx, plan.RootPath, plan.RelativePath, plan.Content); err != nil {
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
