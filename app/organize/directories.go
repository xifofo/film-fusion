package organize

import (
	"errors"

	"film-fusion/app/model"
	"film-fusion/app/service"

	"fmt"

	"path"

	"sort"

	"strings"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

func (h *Service) buildMediaLookupTargetDir(directoryName, category string, info service.MoviePilotMediaInfo) (string, error) {
	probeName := "FilmFusion.Media.Lookup.Probe.mkv"
	targetPath, err := h.buildTargetPathWithDirectory(directoryName, category, service.MediaRenameInput{
		Info: info, MediaType: info.MediaType, Category: info.Category,
		RecognizeName: probeName, TransferName: probeName, OriginalName: probeName,
	})
	if err != nil {
		return "", err
	}
	targetDir := path.Dir(targetPath)
	if isOrganizeTVMedia(info.MediaType, info.Category) {
		if _, ok := parseSeasonDirNumber(path.Base(targetDir)); ok {
			targetDir = path.Dir(targetDir)
		}
	}
	return targetDir, nil
}

func (h *Service) resolveAndPrepareDirectories(storage *model.CloudStorage, webClient *driver.Pan115Client, items *[]Organize115ItemResult, dryRun bool) ([]Organize115DirDebug, error) {
	if items == nil || len(*items) == 0 {
		return nil, nil
	}

	dirMap := make(map[string]*Organize115DirDebug)
	for _, item := range *items {
		if strings.TrimSpace(item.TargetDir) == "" {
			continue
		}
		if _, exists := dirMap[item.TargetDir]; !exists {
			dirMap[item.TargetDir] = &Organize115DirDebug{
				TargetDir: item.TargetDir,
			}
		}
	}

	resolver := newDirResolver(webClient, h.web115Svc, h.dirCache, storage.ID)
	for dirPath, debug := range dirMap {
		resolved, err := h.resolveTargetDir(resolver, dirPath)
		if err != nil {
			debug.Error = err.Error()
			return nil, err
		}
		debug.TargetDir = resolved.TargetDir
		debug.ExistingDir = resolved.ExistingDir
		debug.ExistingID = resolved.ExistingID
		debug.MissingDirs = resolved.MissingDirs
		debug.NeedCreate = resolved.NeedCreate
		debug.Lookups = resolved.Lookups

		finalID := resolved.ExistingID
		if resolved.NeedCreate {
			if !dryRun {
				createdID, err := h.createDirectories(resolver, resolved.ExistingID, resolved.ExistingDir, resolved.MissingDirs)
				if err != nil {
					debug.Error = err.Error()
					return nil, err
				}
				finalID = createdID
			}
		}
		if strings.TrimSpace(finalID) == "" {
			finalID = "0"
		}
		debug.FinalID = finalID
	}

	for i := range *items {
		item := &(*items)[i]
		if debug, ok := dirMap[item.TargetDir]; ok {
			item.TargetDirID = debug.FinalID
			item.NeedCreate = debug.NeedCreate
			item.MissingDirs = debug.MissingDirs
		}
	}

	keys := make([]string, 0, len(dirMap))
	for k := range dirMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	debugs := make([]Organize115DirDebug, 0, len(keys))
	for _, k := range keys {
		debugs = append(debugs, *dirMap[k])
	}

	return debugs, nil
}

func newDirResolver(webClient *driver.Pan115Client, svc *service.Web115Service, cache *service.Web115DirCache, storageID uint) *dirResolver {
	return &dirResolver{
		webClient: webClient,
		web115Svc: svc,
		cache:     cache,
		storageID: storageID,
		local:     make(map[string]map[string]string),
	}
}

// loadChildren 保证 r.local[parentID] 被填充：
//   - force=false 时，依次尝试 local -> global cache -> 115；命中即返回
//   - force=true  时，跳过两层缓存直接去 115 拉，拉回后更新两层
func (r *dirResolver) loadChildren(parentID string, force bool) error {
	if !force {
		if _, ok := r.local[parentID]; ok {
			return nil
		}
		if children, ok := r.cache.Get(r.storageID, parentID); ok {
			r.local[parentID] = children
			return nil
		}
	}
	children := make(map[string]string)
	offset := 0
	for {
		listResp, err := r.web115Svc.GetDirectoriesWithClient(r.webClient, parentID, offset, 0)
		if err != nil {
			return err
		}
		for _, it := range listResp.Items {
			if _, exists := children[it.Name]; !exists {
				children[it.Name] = it.FileID
			}
		}
		pageLen := len(listResp.Items)
		if pageLen == 0 {
			break
		}
		if listResp.Total > 0 {
			if int64(offset+pageLen) >= listResp.Total {
				break
			}
		} else if pageLen < int(driver.MaxDirPageLimit) {
			break
		}
		offset += pageLen
	}
	r.local[parentID] = children
	r.cache.Set(r.storageID, parentID, children)
	return nil
}

func (r *dirResolver) findChild(parentID, name string) (string, error) {
	if err := r.loadChildren(parentID, false); err != nil {
		return "", err
	}
	return r.local[parentID][name], nil
}

func (r *dirResolver) refreshChild(parentID, name string) (string, error) {
	if err := r.loadChildren(parentID, true); err != nil {
		return "", err
	}
	return r.local[parentID][name], nil
}

// rememberChild 在 mkdir 成功后用：将新目录写回两层缓存，不触发 115 请求。
func (r *dirResolver) rememberChild(parentID, name, childID string) {
	if name == "" || childID == "" {
		return
	}
	if bucket, ok := r.local[parentID]; ok {
		bucket[name] = childID
	}
	r.cache.AddChild(r.storageID, parentID, name, childID)
}

// resolveWholePath 走 115 files/getid 直接查「整条路径对应的 CID」。
// 命中时一次 HTTP 请求即完成，远快于逐级 loadChildren。未命中时返回 ok=false，
// 由调用方回退到逐级查找流程。
func (r *dirResolver) resolveWholePath(dir string) (string, bool, error) {
	if r == nil || r.web115Svc == nil || r.webClient == nil {
		return "", false, nil
	}
	return r.web115Svc.ResolveDirPathWithClient(r.webClient, dir)
}

// resolveTargetDirViaGetID 用 DirName2CID 从目标路径向上回退查找「已存在到哪一层」。
//
// 返回值语义：
//   - handled=true：主路径成功完成，debug 已填好（含 ExistingDir/ExistingID/MissingDirs/Lookups），
//     调用方直接返回即可；即便整条路径都不存在、走到根并落到 "/"，也算 handled
//     （因为 ResolveDirPathWithClient 对 "/" 返回 ("0", true, nil)）。
//   - handled=false 且 err != nil：getid 本身报错（风控、鉴权失效、网络），调用方应回退到 list 兜底。
//   - handled=false 且 err == nil：不会出现。
//
// lookups 按「从深到浅」追加，保持和 1a32f9f 版本一致的观测语义。
func (h *Service) resolveTargetDirViaGetID(resolver *dirResolver, cleaned string) (Organize115DirDebug, bool, error) {
	current := cleaned
	lookups := make([]Organize115DirLookup, 0, 4)
	for {
		cid, ok, err := resolver.resolveWholePath(current)
		if err != nil {
			return Organize115DirDebug{}, false, err
		}
		lookups = append(lookups, Organize115DirLookup{Path: current, ID: cid})
		if ok {
			missing := computeMissingDirs(cleaned, current)
			return Organize115DirDebug{
				TargetDir:   cleaned,
				ExistingDir: current,
				ExistingID:  cid,
				MissingDirs: missing,
				NeedCreate:  len(missing) > 0,
				Lookups:     lookups,
			}, true, nil
		}
		parent := path.Dir(current)
		if parent == current {
			// 防御：理论上 current=="/" 时 resolveWholePath 已返 ok=true 走不到这里。
			// 保留是避免 115 行为变化导致死循环。
			missing := computeMissingDirs(cleaned, "/")
			return Organize115DirDebug{
				TargetDir:   cleaned,
				ExistingDir: "/",
				ExistingID:  "0",
				MissingDirs: missing,
				NeedCreate:  len(missing) > 0,
				Lookups:     lookups,
			}, true, nil
		}
		current = parent
	}
}

func (h *Service) resolveTargetDir(resolver *dirResolver, targetDir string) (Organize115DirDebug, error) {
	cleaned := normalizeDirPath(targetDir)
	if cleaned == "/" {
		return Organize115DirDebug{
			TargetDir:   cleaned,
			ExistingDir: "/",
			ExistingID:  "0",
			NeedCreate:  false,
		}, nil
	}

	// 主路径：115 files/getid（SDK: DirName2CID）从目标路径向上逐级回退。
	//
	//   - 整路径已存在（最常见场景，如复整理/增量整理）：1 次 getid 即得 CID
	//   - 部分已存在到第 k 层：k+1 次轻量 getid（每次只传一个路径字符串）
	//
	// 相比下面的 list 兜底，getid 不返回"父下所有子目录"，流量和耗时显著更低。
	if debug, handled, err := h.resolveTargetDirViaGetID(resolver, cleaned); handled {
		return debug, nil
	} else if err != nil {
		// getid 本身挂掉（风控 / 鉴权失效 / 网络）才走下面的 list 兜底
		h.logger.Warnf("115 DirName2CID 查询失败，回退到逐级 list 查找 path=%s err=%v", cleaned, err)
	}

	parts := splitDirParts(cleaned)
	currentID := "0"
	currentPath := "/"
	lookups := []Organize115DirLookup{{Path: "/", ID: "0"}}

	for i, name := range parts {
		nextPath := path.Join(currentPath, name)
		if !strings.HasPrefix(nextPath, "/") {
			nextPath = "/" + nextPath
		}
		childID, err := resolver.findChild(currentID, name)
		if err != nil {
			return Organize115DirDebug{}, err
		}
		lookups = append(lookups, Organize115DirLookup{Path: nextPath, ID: childID})
		if childID == "" {
			return Organize115DirDebug{
				TargetDir:   cleaned,
				ExistingDir: currentPath,
				ExistingID:  currentID,
				MissingDirs: append([]string{}, parts[i:]...),
				NeedCreate:  true,
				Lookups:     lookups,
			}, nil
		}
		currentID = childID
		currentPath = nextPath
	}

	return Organize115DirDebug{
		TargetDir:   cleaned,
		ExistingDir: currentPath,
		ExistingID:  currentID,
		NeedCreate:  false,
		Lookups:     lookups,
	}, nil
}

func (h *Service) createDirectories(resolver *dirResolver, existingID, existingDir string, missing []string) (string, error) {
	if resolver == nil || resolver.webClient == nil {
		return "", fmt.Errorf("createDirectories: resolver/webClient 未初始化")
	}
	webClient := resolver.webClient
	pid := strings.TrimSpace(existingID)
	if pid == "" {
		pid = "0"
	}
	currentPath := normalizeDirPath(existingDir)
	for _, name := range missing {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		nextPath := path.Join(currentPath, name)
		h.logger.Infof("115 Mkdir 调用 pid=%s name=%q path=%s", pid, name, nextPath)
		cid, err := h.web115Svc.MkdirWithClient(webClient, pid, name)
		if err != nil {
			alreadyExists := errors.Is(err, driver.ErrExist) || strings.Contains(err.Error(), "target already exists") || strings.Contains(err.Error(), "该目录名称已存在")
			if alreadyExists {
				h.logger.Infof("115 Mkdir 目录已存在(errno=20004) pid=%s name=%q path=%s, 重新列父目录查已存在 cid", pid, name, nextPath)
			} else {
				h.logger.Warnf("115 Mkdir 失败 pid=%s name=%q path=%s err=%v", pid, name, nextPath, err)
			}
			if childID, lookupErr := resolver.refreshChild(pid, name); lookupErr == nil && childID != "" {
				h.logger.Infof("115 Mkdir 失败后通过子目录列表找到已存在目录 path=%s file_id=%s", nextPath, childID)
				pid = childID
				currentPath = nextPath
				continue
			}
			return "", fmt.Errorf("创建目录失败(%s): %w", nextPath, err)
		}
		h.logger.Infof("115 Mkdir 成功 pid=%s name=%q path=%s file_id=%s", pid, name, nextPath, cid)
		resolver.rememberChild(pid, name, cid)
		pid = cid
		currentPath = nextPath
	}
	return pid, nil
}
