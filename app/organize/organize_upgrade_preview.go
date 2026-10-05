package organize

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/pathhelper"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/xifofo/medianame"
)

type OrganizeUpgradeDirectory struct {
	Path     string `json:"path"`
	ID       string `json:"id"`
	Manifest string `json:"manifest"`
}

type OrganizeUpgradeExisting struct {
	Sidecars  []service.Web115File      `json:"sidecars,omitempty"`
	File      service.Web115File        `json:"file"`
	CloudPath string                    `json:"cloud_path"`
	MediaKey  string                    `json:"media_key"`
	Facts     service.MediaUpgradeFacts `json:"facts"`
	Rank      service.MediaUpgradeRank  `json:"rank"`
	Bindings  []OrganizeUpgradeBinding  `json:"bindings"`
}

type OrganizeUpgradeBinding struct {
	Root     string `json:"root"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	CacheKey string `json:"cache_key"`
}

type OrganizeUpgradeDecision struct {
	Action   string                      `json:"action"`
	Reason   string                      `json:"reason"`
	MediaKey string                      `json:"media_key"`
	Facts    service.MediaUpgradeFacts   `json:"facts"`
	Rank     service.MediaUpgradeRank    `json:"rank"`
	Existing []OrganizeUpgradeExisting   `json:"existing"`
	Bindings []OrganizeUpgradeBinding    `json:"bindings"`
	Changes  []service.UpgradeFileChange `json:"changes"`
}

func upgradeVideo(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".wmv", ".m4v", ".ts", ".m2ts", ".mpg", ".mpeg", ".iso":
		return true
	}
	return false
}

func upgradeMediaKey(tmdbID, kind, filename string, facts service.MediaUpgradeFacts) (string, error) {
	parsed := medianame.ParsePath(filename)
	if strings.TrimSpace(tmdbID) == "" || (parsed.IDs.TMDB != "" && parsed.IDs.TMDB != tmdbID) {
		return "", errors.New("媒体身份缺失或 TMDB ID 冲突")
	}
	key := kind + ":" + tmdbID
	if kind == "movie" && parsed.Type == medianame.TypeTV {
		return "", errors.New("文件季集标记与电影类型冲突")
	}
	if kind == "tv" {
		if parsed.Season == nil || parsed.Episode == nil || (parsed.SeasonEnd != nil && *parsed.SeasonEnd != *parsed.Season) {
			return "", errors.New("无法确定目标季集")
		}
		episodes := parsed.Episodes
		if len(episodes) == 0 {
			episodes = []int{*parsed.Episode}
		}
		// First release handles a single episode per file. A pack must be split or
		// explicitly reviewed instead of discarding other episodes in an old pack.
		if len(episodes) != 1 || (parsed.EpisodeEnd != nil && *parsed.EpisodeEnd != *parsed.Episode) {
			return "", errors.New("多集文件需要人工核对完整覆盖范围")
		}
		key += fmt.Sprintf(":s%d:e%d", *parsed.Season, *parsed.Episode)
	} else if kind != "movie" {
		return "", errors.New("媒体类型不明确")
	}
	return fmt.Sprintf("%s:part%d:cut=%s", key, facts.Part, facts.Cut), nil
}

func (h *Service) upgradeDirectory(client *driver.Pan115Client, storageID uint, dirPath string) (OrganizeUpgradeDirectory, []service.Web115File, error) {
	resolver := newDirResolver(client, h.web115Svc, service.NewWeb115DirCache(web115DirCacheTTL), storageID)
	resolved, err := h.resolveTargetDir(resolver, dirPath)
	snapshot := OrganizeUpgradeDirectory{Path: dirPath}
	if err != nil {
		return snapshot, nil, err
	}
	if resolved.NeedCreate {
		return snapshot, nil, nil
	}
	snapshot.ID = resolved.ExistingID
	files, err := h.upgradeList(client, snapshot.ID)
	if err != nil {
		return snapshot, nil, err
	}
	manifest := make([]organizeSourceIdentity, 0, len(files))
	for _, file := range files {
		manifest = append(manifest, sourceIdentity(file))
	}
	snapshot.Manifest = organizeManifest(manifest)
	return snapshot, files, nil
}

func (h *Service) upgradeList(client *driver.Pan115Client, id string) ([]service.Web115File, error) {
	var files []service.Web115File
	for offset := 0; ; offset += 1150 {
		page, err := h.web115Svc.GetFilesWithClient(client, id, offset, 1150)
		if err != nil {
			return nil, err
		}
		for _, file := range page.Items {
			file.ParentID = id
			files = append(files, file)
		}
		if (page.Total > 0 && int64(offset+1150) >= page.Total) || (page.Total <= 0 && len(page.Items) < 1150) {
			break
		}
	}
	return files, nil
}

func upgradeBindings(dir model.CloudDirectory, mappings []model.CloudPath, cloudPath string) []OrganizeUpgradeBinding {
	bindings := []OrganizeUpgradeBinding{}
	seen := map[string]bool{}
	add := func(root, prefix, eventPath string, encode bool) {
		if strings.TrimSpace(root) == "" {
			return
		}
		root = filepath.Clean(root)
		strm, content := buildStrmInfo(root, prefix, eventPath, encode)
		if seen[strm] {
			return
		}
		seen[strm] = true
		bindings = append(bindings, OrganizeUpgradeBinding{Root: root, Path: strm, Content: content, CacheKey: pathhelper.SafeFilePathJoin(prefix, eventPath)})
	}
	add(dir.SavePath, dir.ContentPrefix, cloudPath, dir.ContentEncodeURI)
	for _, mapping := range mappings {
		filterType := "include"
		if isSubtitleFile(cloudPath) {
			filterType = "download"
		}
		if !pathhelper.IsFileMatchedByFilter(cloudPath, mapping.FilterRules, filterType) {
			continue
		}
		eventPath := cloudPath
		if mapping.SourceType == model.SourceTypeCloudDrive2 {
			mount := strings.Split(strings.Trim(mapping.SourcePath, "/"), "/")[0]
			if mount == "" {
				continue
			}
			eventPath = path.Join("/", mount, cloudPath)
		}
		if pathhelper.IsSubPath(eventPath, mapping.SourcePath) {
			add(mapping.LocalPath, mapping.ContentPrefix, eventPath, mapping.ContentEncodeURI)
		}
	}
	return bindings
}

func upgradeAssociatedArtifact(videoStem, name string) bool {
	if strings.EqualFold(name, "movie.nfo") || strings.EqualFold(name, "tvshow.nfo") {
		return false
	}
	if strings.EqualFold(name, videoStem+".nfo") || strings.EqualFold(name, videoStem+"-mediainfo.json") || strings.EqualFold(name, videoStem+".mediainfo.json") {
		return true
	}
	for _, suffix := range []string{"-mediainfo.json", ".mediainfo.json"} {
		if strings.HasSuffix(strings.ToLower(name), suffix) {
			media := name[:len(name)-len(suffix)]
			if strings.EqualFold(strings.TrimSuffix(media, path.Ext(media)), videoStem) && (upgradeVideo(media) || strings.EqualFold(path.Ext(media), ".strm")) {
				return true
			}
		}
	}
	if !isSubtitleFile(name) {
		return false
	}
	stem, _ := splitOrganizeSubtitleName(name)
	stem, _ = stripOrganizeSubtitleQualifier(stem)
	return strings.EqualFold(stem, videoStem)
}

func planUpgradeLocalChanges(dir model.CloudDirectory, mappings []model.CloudPath, item Organize115ItemResult, group []Organize115ItemResult) error {
	decision := item.Upgrade
	changes := map[string]service.UpgradeFileChange{}
	oldPaths := map[string]string{}
	for _, existing := range decision.Existing {
		for _, binding := range existing.Bindings {
			oldPaths[binding.Path] = binding.Content
			snapshot, err := service.ReadUpgradeFile(binding.Root, binding.Path)
			if err != nil {
				return err
			}
			if snapshot.Exists && strings.TrimSpace(string(snapshot.Data)) != strings.TrimSpace(binding.Content) {
				return fmt.Errorf("旧 STRM 指向与登记不符: %s", binding.Path)
			}
			changes[binding.Path] = service.UpgradeFileChange{Before: snapshot}
			stem := strings.TrimSuffix(filepath.Base(binding.Path), filepath.Ext(binding.Path))
			names, err := service.ListUpgradeSiblings(binding.Root, binding.Path)
			if err != nil {
				return err
			}
			for _, name := range names {
				if !upgradeAssociatedArtifact(stem, name) {
					continue
				}
				artifact, err := service.ReadUpgradeFile(binding.Root, filepath.Join(filepath.Dir(binding.Path), name))
				if err != nil {
					return err
				}
				if artifact.Exists {
					changes[artifact.Path] = service.UpgradeFileChange{Before: artifact}
				}
			}
		}
	}
	for _, binding := range decision.Bindings {
		snapshot, err := service.ReadUpgradeFile(binding.Root, binding.Path)
		if err != nil {
			return err
		}
		if snapshot.Exists {
			if _, owned := oldPaths[binding.Path]; !owned {
				return fmt.Errorf("目标 STRM 已存在但无法绑定旧文件: %s", binding.Path)
			}
		}
		changes[binding.Path] = service.UpgradeFileChange{Before: snapshot, Write: true, Data: []byte(binding.Content)}
	}
	for _, subtitle := range group {
		if !subtitle.IsSubtitle || subtitle.MatchedVideoFileID != item.FileID {
			continue
		}
		if subtitle.FileSize <= 0 || subtitle.FileSize > 2*1024*1024 || subtitle.PickCode == "" {
			return fmt.Errorf("字幕 %s 缺少大小、下载标识或超过 2 MiB，需要人工处理", subtitle.FileName)
		}
		for _, binding := range upgradeBindings(dir, mappings, subtitle.TargetPath) {
			target := strings.TrimSuffix(binding.Path, ".strm") + path.Ext(subtitle.TargetPath)
			snapshot, err := service.ReadUpgradeFile(binding.Root, target)
			if err != nil {
				return err
			}
			if _, owned := changes[target]; snapshot.Exists && !owned {
				return fmt.Errorf("目标字幕已存在但无法绑定旧文件: %s", target)
			}
			changes[target] = service.UpgradeFileChange{Before: snapshot, Write: true, DownloadPickCode: subtitle.PickCode, DownloadSize: subtitle.FileSize}
		}
	}
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		decision.Changes = append(decision.Changes, changes[key])
	}
	return nil
}

func (h *Service) prepareUpgradePreview(dir model.CloudDirectory, result *Organize115CookieResult) error {
	policy, err := service.ParseMediaUpgradePolicy(dir.UpgradePolicy)
	if err != nil || !policy.Enabled {
		return err
	}
	if service.IsUpgradeRecoveryPath("/" + dir.DirectoryName) {
		return errors.New("恢复目录不能作为洗版媒体库")
	}
	if !filepath.IsAbs(dir.SavePath) {
		return errors.New("开启洗版需要配置绝对 STRM 保存路径")
	}
	result.UpgradePolicy = &policy
	var storage model.CloudStorage
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", dir.CloudStorageID, dir.UserID).First(&storage).Error; err != nil {
		return err
	}
	client, err := h.organizeClient(storage.Cookie)
	if err != nil {
		return err
	}
	var mappings []model.CloudPath
	if err := h.store().WithContext(h.requestContext()).Where("user_id = ? AND cloud_storage_id = ?", dir.UserID, dir.CloudStorageID).Find(&mappings).Error; err != nil {
		return err
	}
	type directoryFiles struct {
		snapshot OrganizeUpgradeDirectory
		files    []service.Web115File
	}
	directories := map[string]directoryFiles{}
	loadDir := func(dirPath string) (directoryFiles, error) {
		if found, ok := directories[dirPath]; ok {
			return found, nil
		}
		snapshot, files, err := h.upgradeDirectory(client, storage.ID, dirPath)
		if err != nil {
			return directoryFiles{}, err
		}
		value := directoryFiles{snapshot, files}
		directories[dirPath] = value
		result.UpgradeDirectories = append(result.UpgradeDirectories, snapshot)
		return value, nil
	}
	for gi := range result.Groups {
		for ii := range result.Groups[gi].Items {
			item := &result.Groups[gi].Items[ii]
			if item.IsSubtitle || item.Error != "" {
				continue
			}
			decision := &OrganizeUpgradeDecision{Action: "add", Reason: "媒体库中没有同一版本身份的文件", Facts: service.ParseMediaUpgradeFacts(item.FileName, item.FileSize), Existing: []OrganizeUpgradeExisting{}}
			item.Upgrade = decision
			decision.Rank = service.RankMediaUpgrade(policy, decision.Facts)
			kind := "movie"
			if isOrganizeTVMedia(item.MediaType, item.Category) {
				kind = "tv"
			} else if !isOrganizeMovieMedia(item.MediaType, item.Category) {
				kind = ""
			}
			decision.MediaKey, err = upgradeMediaKey(item.TmdbID, kind, item.TargetPath, decision.Facts)
			if err != nil {
				decision.Action, decision.Reason = "review", err.Error()
				continue
			}
			if item.FileSize <= 0 {
				decision.Action, decision.Reason = "review", "源文件大小未知或为空"
				continue
			}
			if decision.Rank.Rejected {
				decision.Action, decision.Reason = "keep", decision.Rank.Label
				continue
			}
			if decision.Rank.Tier == 0 {
				decision.Action, decision.Reason = "review", decision.Rank.Label
				continue
			}
			var records []model.OrganizeMediaFile
			if err := h.store().WithContext(h.requestContext()).Where("user_id = ? AND cloud_storage_id = ? AND cloud_directory_id = ? AND media_key = ?", dir.UserID, dir.CloudStorageID, dir.ID, decision.MediaKey).Find(&records).Error; err != nil {
				return err
			}
			paths := map[string]bool{item.TargetDir: true}
			for _, record := range records {
				paths[path.Dir(record.CloudPath)] = true
			}
			if item.LocalExists && item.LocalDir != "" {
				if rel, err := filepath.Rel(dir.SavePath, item.LocalDir); err == nil && filepath.IsLocal(rel) {
					paths["/"+filepath.ToSlash(rel)] = true
				}
			}
			seen := map[string]bool{}
			for dirPath := range paths {
				listing, err := loadDir(dirPath)
				if err != nil {
					return fmt.Errorf("读取库内版本失败: %w", err)
				}
				for _, file := range listing.files {
					if !file.IsFile || !upgradeVideo(file.Name) || seen[file.FileID] {
						continue
					}
					seen[file.FileID] = true
					if file.FileID == item.FileID {
						decision.Action, decision.Reason = "keep", "该文件已在媒体库中"
						continue
					}
					oldPath := path.Join(dirPath, file.Name)
					facts := service.ParseMediaUpgradeFacts(file.Name, file.Size)
					tracked := false
					for _, record := range records {
						if record.FileID == file.FileID && record.PickCode == file.PickCode && record.Size == file.Size && record.CloudPath == oldPath {
							if json.Unmarshal([]byte(record.FactsJSON), &facts) == nil {
								tracked = true
							}
						}
					}
					if !tracked && extractTmdbIDFromName(dirPath) != item.TmdbID {
						decision.Action, decision.Reason = "review", "库内目录缺少可验证的 TMDB 标记，请先核对旧文件归属"
						continue
					}
					key, keyErr := upgradeMediaKey(item.TmdbID, kind, oldPath, facts)
					if keyErr != nil {
						decision.Action, decision.Reason = "review", "库内文件: "+keyErr.Error()
						continue
					}
					if key != decision.MediaKey {
						if oldPath == item.TargetPath {
							decision.Action, decision.Reason = "review", "目标同名文件属于其他季集、Part 或剪辑版本"
						}
						continue
					}
					existing := OrganizeUpgradeExisting{File: file, CloudPath: oldPath, MediaKey: key, Facts: facts, Rank: service.RankMediaUpgrade(policy, facts), Bindings: upgradeBindings(dir, mappings, oldPath)}
					var registered []model.OrganizeManagedStrm
					if err := h.store().WithContext(h.requestContext()).Where("user_id = ? AND cloud_storage_id = ? AND file_id = ? AND retired = ? AND content <> ?", dir.UserID, dir.CloudStorageID, file.FileID, false, "").Find(&registered).Error; err != nil {
						return err
					}
					for _, binding := range registered {
						found := false
						prior := OrganizeUpgradeBinding{Root: binding.Root, Path: binding.Path, Content: binding.Content, CacheKey: binding.CacheKey}
						for i := range existing.Bindings {
							if existing.Bindings[i].Path == prior.Path {
								existing.Bindings[i] = prior
								found = true
								break
							}
						}
						if !found {
							existing.Bindings = append(existing.Bindings, prior)
						}
					}
					videoStem := strings.TrimSuffix(file.Name, path.Ext(file.Name))
					for _, candidate := range listing.files {
						if !candidate.IsFile || !upgradeAssociatedArtifact(videoStem, candidate.Name) {
							continue
						}
						existing.Sidecars = append(existing.Sidecars, candidate)
					}
					decision.Existing = append(decision.Existing, existing)
				}
			}
			if decision.Action == "add" && len(decision.Existing) > 0 {
				decision.Action = "replace"
				for _, existing := range decision.Existing {
					action, reason := service.CompareMediaUpgrade(policy, decision.Facts, existing.Facts)
					decision.Reason = reason
					if action != "replace" {
						decision.Action = action
						break
					}
				}
			}
			decision.Bindings = upgradeBindings(dir, mappings, item.TargetPath)
			if decision.Action == "add" || decision.Action == "replace" {
				if err := planUpgradeLocalChanges(dir, mappings, *item, result.Groups[gi].Items); err != nil {
					decision.Action, decision.Reason = "review", err.Error()
				}
			}
		}
	}
	// Select one incoming file per logical identity. Never let two candidates in
	// this preview both replace the same old file or write the same STRM.
	winners := map[string]*OrganizeUpgradeDecision{}
	for gi := range result.Groups {
		for ii := range result.Groups[gi].Items {
			decision := result.Groups[gi].Items[ii].Upgrade
			if decision == nil || (decision.Action != "add" && decision.Action != "replace") {
				continue
			}
			if prior, ok := winners[decision.MediaKey]; ok {
				better := decision.Rank.Tier < prior.Rank.Tier || (decision.Rank.Tier == prior.Rank.Tier && policy.SameTier && decision.Rank.Score > prior.Rank.Score)
				if better {
					prior.Action, prior.Reason = "keep", "本批有更优候选"
					winners[decision.MediaKey] = decision
				} else {
					decision.Action, decision.Reason = "keep", "本批已选择同一媒体的另一候选"
				}
			} else {
				winners[decision.MediaKey] = decision
			}
		}
	}
	result.Items = nil
	writes := map[string]*OrganizeUpgradeDecision{}
	for gi := range result.Groups {
		for ii := range result.Groups[gi].Items {
			decision := result.Groups[gi].Items[ii].Upgrade
			if decision == nil || (decision.Action != "add" && decision.Action != "replace") {
				continue
			}
			for _, change := range decision.Changes {
				if !change.Write {
					continue
				}
				if other := writes[change.Before.Path]; other != nil && other != decision {
					other.Action, other.Reason = "review", "多个候选指向同一 STRM 路径"
					decision.Action, decision.Reason = "review", other.Reason
				}
				writes[change.Before.Path] = decision
			}
		}
	}
	for _, group := range result.Groups {
		result.Items = append(result.Items, group.Items...)
	}
	return nil
}
