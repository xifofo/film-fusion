package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"film-fusion/app/model"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
)

const (
	defaultAutomation115PollSeconds    = 30
	defaultAutomation115MaxWaitMinutes = 7 * 24 * 60
	minAutomation115PollSeconds        = 5
	maxAutomation115PollSeconds        = 300
	maxAutomation115MaxWaitMinutes     = 30 * 24 * 60
	maxAutomation115TaskPages          = 200
	maxAutomation115MediaFiles         = 1000
	maxAutomation115Folders            = 500
	maxAutomation115FolderDepth        = 32
)

var (
	automationTMDBIDPattern     = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	automationTMDBMarkerPattern = regexp.MustCompile(`[\s._-]*` + localTMDBMarkerPattern)
	automationMediaExtensions   = map[string]struct{}{
		".3gp": {}, ".asf": {}, ".avi": {}, ".divx": {}, ".flv": {},
		".iso": {}, ".m2ts": {}, ".m4v": {}, ".mkv": {}, ".mov": {},
		".mp4": {}, ".mpeg": {}, ".mpg": {}, ".mts": {}, ".rm": {},
		".rmvb": {}, ".ts": {}, ".vob": {}, ".webm": {}, ".wmv": {},
	}
)

type automationMoviePilotRecognizer interface {
	AutomationMediaRecognizer
	RecognizeFile(filePath string) (MoviePilotMediaInfo, map[string]any, error)
}

type automation115Task struct {
	InfoHash    string  `json:"info_hash"`
	Name        string  `json:"name,omitempty"`
	FileID      string  `json:"file_id,omitempty"`
	DirectoryID string  `json:"directory_id,omitempty"`
	Status      int     `json:"status"`
	Percent     float64 `json:"percent"`
	Size        int64   `json:"size,omitempty"`
}

type automation115MediaFile struct {
	FileID string `json:"file_id,omitempty"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size,omitempty"`
}

type automation115Gateway interface {
	ListOfflineTasks(ctx context.Context, storage model.CloudStorage, accessMethod string) ([]automation115Task, error)
	ListMediaFiles(ctx context.Context, storage model.CloudStorage, accessMethod string, task automation115Task) ([]automation115MediaFile, error)
}

type defaultAutomation115Gateway struct {
	web115     *Web115Service
	httpClient *http.Client
}

func (g *defaultAutomation115Gateway) ListOfflineTasks(ctx context.Context, storage model.CloudStorage, accessMethod string) ([]automation115Task, error) {
	switch strings.ToLower(strings.TrimSpace(accessMethod)) {
	case "openapi":
		accessToken := strings.TrimSpace(storage.AccessToken)
		if accessToken == "" {
			return nil, errors.New("115 OpenAPI 账号没有可用 AccessToken")
		}
		client := sdk115.New(sdk115.WithAccessToken(accessToken))
		if g.httpClient != nil {
			client.SetHttpClient(g.httpClient)
		}
		tasks := make([]automation115Task, 0)
		for pageNumber := int64(1); pageNumber <= maxAutomation115TaskPages; pageNumber++ {
			page, err := client.OfflineTaskList(ctx, pageNumber)
			if err != nil {
				return nil, fmt.Errorf("查询 115 OpenAPI 云下载任务失败: %w", err)
			}
			for _, task := range page.Tasks {
				tasks = append(tasks, automation115Task{
					InfoHash: strings.TrimSpace(task.InfoHash), Name: strings.TrimSpace(task.Name),
					FileID: strings.TrimSpace(task.FileID), DirectoryID: strings.TrimSpace(task.WpPathID),
					Status: task.Status, Percent: float64(task.PercentDone), Size: task.Size,
				})
			}
			if page.PageCount <= 0 || int(pageNumber) >= page.PageCount {
				break
			}
		}
		return tasks, nil
	case "cookie":
		if g.web115 == nil {
			return nil, errors.New("115 Cookie 服务未初始化")
		}
		if strings.TrimSpace(storage.Cookie) == "" {
			return nil, errors.New("115 账号没有可用 Cookie")
		}
		timeout := 30 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			timeout = time.Until(deadline)
		}
		if timeout <= 0 {
			return nil, context.DeadlineExceeded
		}
		client, err := g.web115.NewClientWithContext(ctx, storage.Cookie, timeout)
		if err != nil {
			return nil, err
		}
		tasks := make([]automation115Task, 0)
		for pageNumber := int64(1); pageNumber <= maxAutomation115TaskPages; pageNumber++ {
			page, err := client.ListOfflineTask(pageNumber)
			if err != nil {
				return nil, fmt.Errorf("查询 115 Cookie 云下载任务失败: %w", err)
			}
			for _, task := range page.Tasks {
				if task == nil {
					continue
				}
				tasks = append(tasks, automation115Task{
					InfoHash: strings.TrimSpace(task.InfoHash), Name: strings.TrimSpace(task.Name),
					FileID: strings.TrimSpace(task.FileId), DirectoryID: strings.TrimSpace(task.DirId),
					Status: task.Status, Percent: task.Percent, Size: task.Size,
				})
			}
			if page.PageCount <= 0 || pageNumber >= page.PageCount {
				break
			}
		}
		return tasks, nil
	default:
		return nil, fmt.Errorf("不支持的 115 访问方式 %q", accessMethod)
	}
}

func (g *defaultAutomation115Gateway) ListMediaFiles(ctx context.Context, storage model.CloudStorage, accessMethod string, task automation115Task) ([]automation115MediaFile, error) {
	if automationIsMediaFile(task.Name) {
		return []automation115MediaFile{{
			FileID: task.FileID, Name: task.Name, Path: task.Name, Size: task.Size,
		}}, nil
	}
	rootID := strings.TrimSpace(task.FileID)
	if rootID == "" {
		return nil, errors.New("115 云下载任务完成后没有返回文件或文件夹 ID")
	}
	rootName := strings.Trim(strings.TrimSpace(task.Name), "/")
	if rootName == "" {
		rootName = rootID
	}

	type folder struct {
		id    string
		path  string
		depth int
	}
	queue := []folder{{id: rootID, path: rootName}}
	visited := map[string]struct{}{}
	files := make([]automation115MediaFile, 0)
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[0]
		queue = queue[1:]
		if _, exists := visited[current.id]; exists {
			continue
		}
		visited[current.id] = struct{}{}
		if len(visited) > maxAutomation115Folders {
			return nil, fmt.Errorf("115 下载目录超过 %d 个文件夹，已停止媒体识别", maxAutomation115Folders)
		}
		if current.depth > maxAutomation115FolderDepth {
			return nil, fmt.Errorf("115 下载目录深度超过 %d 层，已停止媒体识别", maxAutomation115FolderDepth)
		}

		children, err := g.listFolderChildren(ctx, storage, accessMethod, current.id)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			childPath := path.Join(current.path, child.Name)
			if child.IsFile {
				if !automationIsMediaFile(child.Name) {
					continue
				}
				files = append(files, automation115MediaFile{
					FileID: child.FileID, Name: child.Name, Path: childPath, Size: child.Size,
				})
				if len(files) > maxAutomation115MediaFiles {
					return nil, fmt.Errorf("115 下载目录超过 %d 个媒体文件，已停止识别", maxAutomation115MediaFiles)
				}
				continue
			}
			if strings.TrimSpace(child.FileID) != "" {
				queue = append(queue, folder{id: child.FileID, path: childPath, depth: current.depth + 1})
			}
		}
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}

func (g *defaultAutomation115Gateway) listFolderChildren(ctx context.Context, storage model.CloudStorage, accessMethod, folderID string) ([]Web115File, error) {
	switch strings.ToLower(strings.TrimSpace(accessMethod)) {
	case "openapi":
		return g.listOpenAPIFolderChildren(ctx, storage.AccessToken, folderID)
	case "cookie":
		return g.listCookieFolderChildren(ctx, storage.Cookie, folderID)
	default:
		return nil, fmt.Errorf("不支持的 115 访问方式 %q", accessMethod)
	}
}

func (g *defaultAutomation115Gateway) listOpenAPIFolderChildren(ctx context.Context, accessToken, folderID string) ([]Web115File, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, errors.New("115 OpenAPI 账号没有可用 AccessToken")
	}
	client := sdk115.New(sdk115.WithAccessToken(accessToken))
	if g.httpClient != nil {
		client.SetHttpClient(g.httpClient)
	}
	const limit int64 = 1150
	items := make([]Web115File, 0)
	for offset := int64(0); ; offset += limit {
		result, err := client.GetFiles(ctx, &sdk115.GetFilesReq{
			CID: strings.TrimSpace(folderID), Limit: limit, Offset: offset,
			Stdir: 1, ShowDir: true, Cur: 1,
		})
		if err != nil {
			return nil, fmt.Errorf("查询 115 OpenAPI 下载目录失败 folder_id=%s: %w", folderID, err)
		}
		for _, file := range result.Data {
			items = append(items, Web115File{
				FileID: strings.TrimSpace(file.Fid), Name: strings.TrimSpace(file.Fn),
				PickCode: strings.TrimSpace(file.Pc), SHA1: strings.TrimSpace(file.Sha1),
				IsFile: file.Fc == "1", Size: file.FS,
			})
		}
		if len(result.Data) == 0 || int64(len(result.Data)) < limit || (result.Count > 0 && offset+limit >= result.Count) {
			break
		}
	}
	return items, nil
}

func (g *defaultAutomation115Gateway) listCookieFolderChildren(ctx context.Context, cookie, folderID string) ([]Web115File, error) {
	if g.web115 == nil {
		return nil, errors.New("115 Cookie 服务未初始化")
	}
	timeout := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	client, err := g.web115.NewClientWithContext(ctx, cookie, timeout)
	if err != nil {
		return nil, err
	}
	const limit = 1150
	items := make([]Web115File, 0)
	for offset := 0; ; offset += limit {
		page, err := g.web115.GetFilesWithClient(client, folderID, offset, limit)
		if err != nil {
			return nil, fmt.Errorf("查询 115 Cookie 下载目录文件失败 folder_id=%s: %w", folderID, err)
		}
		items = append(items, page.Items...)
		if len(page.Items) == 0 || len(page.Items) < limit || (page.Total > 0 && int64(offset+limit) >= page.Total) {
			break
		}
	}
	for offset := 0; ; offset += limit {
		page, err := g.web115.GetDirectoriesWithClient(client, folderID, offset, limit)
		if err != nil {
			return nil, fmt.Errorf("查询 115 Cookie 下载目录文件夹失败 folder_id=%s: %w", folderID, err)
		}
		items = append(items, page.Items...)
		if len(page.Items) == 0 || len(page.Items) < limit || (page.Total > 0 && int64(offset+limit) >= page.Total) {
			break
		}
	}
	return items, nil
}

func (s *AutomationService) executeAutomationWait115(ctx context.Context, nodeRun model.AutomationNodeRun, node AutomationNode, definition AutomationDefinition, runContext map[string]any) (map[string]any, error) {
	_, sourceOutput, err := automationPredecessorOutput(node.ID, definition, runContext)
	if err != nil {
		return nil, err
	}
	storageID := automationAnyUint(sourceOutput["cloud_storage_id"])
	accessMethod := strings.ToLower(automationAnyString(sourceOutput["access_method"]))
	hashes := automationAnyStringSlice(sourceOutput["hashes"])
	if storageID == 0 || accessMethod == "" || len(hashes) == 0 {
		return nil, errors.New("上游 115 节点没有返回账号、访问方式或任务 hash")
	}

	var storage model.CloudStorage
	if err := s.db.First(&storage, storageID).Error; err != nil {
		return nil, fmt.Errorf("115 账号不存在: %w", err)
	}
	if storage.StorageType != model.StorageType115Open {
		return nil, errors.New("所选云存储不是 115 账号")
	}
	if storage.Status != model.StatusActive {
		return nil, fmt.Errorf("115 账号当前状态为 %s", storage.Status)
	}
	gateway := s.automation115Gateway()
	if gateway == nil {
		return nil, errors.New("115 云下载查询服务未初始化")
	}

	output := automationDecodeNodeOutput(nodeRun.OutputJSON)
	delete(output, "selected_port")
	delete(output, "selected_ports")
	delete(output, "reason")
	waitingSince := automationOutputTime(output, "waiting_since")
	if waitingSince.IsZero() {
		waitingSince = time.Now().UTC()
	}
	output["waiting_since"] = waitingSince.Format(time.RFC3339)
	output["cloud_storage_id"] = storageID
	output["access_method"] = accessMethod
	output["directory_id"] = automationAnyString(sourceOutput["directory_id"])
	output["hashes"] = hashes

	listedTasks, listErr := gateway.ListOfflineTasks(ctx, storage, accessMethod)
	if listErr != nil {
		output["last_checked_at"] = time.Now().UTC().Format(time.RFC3339)
		return output, listErr
	}
	byHash := make(map[string]automation115Task, len(listedTasks))
	for _, task := range listedTasks {
		if task.InfoHash != "" {
			byHash[strings.ToLower(task.InfoHash)] = task
		}
	}

	tasks := make([]automation115Task, 0, len(hashes))
	pendingHashes := make([]string, 0)
	failedNames := make([]string, 0)
	fileIDs := make([]string, 0)
	fileNames := make([]string, 0)
	percentTotal := 0.0
	for _, hash := range hashes {
		task, found := byHash[strings.ToLower(strings.TrimSpace(hash))]
		if !found {
			task = automation115Task{InfoHash: hash, Status: 0}
			pendingHashes = append(pendingHashes, hash)
		} else {
			if task.DirectoryID == "" {
				task.DirectoryID = automationAnyString(sourceOutput["directory_id"])
			}
			percentTotal += task.Percent
			switch {
			case task.Status == -1:
				failedName := task.Name
				if failedName == "" {
					failedName = task.InfoHash
				}
				failedNames = append(failedNames, failedName)
			case task.Status == 2 && strings.TrimSpace(task.FileID) != "":
				fileIDs = append(fileIDs, task.FileID)
				fileNames = append(fileNames, task.Name)
			default:
				pendingHashes = append(pendingHashes, hash)
			}
		}
		tasks = append(tasks, task)
	}
	output["tasks"] = tasks
	output["pending_hashes"] = pendingHashes
	output["file_ids"] = fileIDs
	output["file_names"] = fileNames
	if len(fileIDs) == 1 {
		output["file_id"] = fileIDs[0]
		output["file_name"] = fileNames[0]
	}
	if len(hashes) > 0 {
		output["percent"] = percentTotal / float64(len(hashes))
	}
	output["last_checked_at"] = time.Now().UTC().Format(time.RFC3339)

	if len(failedNames) > 0 {
		output["waiting"] = false
		output["completed"] = false
		output["failed"] = true
		output["selected_port"] = "failure"
		output["reason"] = "115 云下载失败: " + strings.Join(failedNames, "、")
		return output, nil
	}
	if len(pendingHashes) == 0 && len(fileIDs) == len(hashes) {
		output["waiting"] = false
		output["completed"] = true
		output["failed"] = false
		output["percent"] = 100
		output["selected_port"] = "success"
		return output, nil
	}

	maxWaitMinutes := automationConfigUint(node.Config, "max_wait_minutes")
	if maxWaitMinutes == 0 {
		maxWaitMinutes = defaultAutomation115MaxWaitMinutes
	}
	if time.Since(waitingSince) >= time.Duration(maxWaitMinutes)*time.Minute {
		output["waiting"] = false
		output["completed"] = false
		output["timed_out"] = true
		output["selected_port"] = "failure"
		output["reason"] = fmt.Sprintf("等待 115 云下载超过 %d 分钟", maxWaitMinutes)
		return output, nil
	}

	pollSeconds := automationConfigUint(node.Config, "poll_interval_seconds")
	if pollSeconds == 0 {
		pollSeconds = defaultAutomation115PollSeconds
	}
	output["waiting"] = true
	output["completed"] = false
	output["failed"] = false
	output["selected_ports"] = []string{}
	return output, &automationNodeDeferred{
		delay:   time.Duration(pollSeconds) * time.Second,
		message: fmt.Sprintf("115 云下载中，当前约 %.0f%%", output["percent"]),
	}
}

func (s *AutomationService) executeAutomationMoviePilotRecognize(ctx context.Context, node AutomationNode, definition AutomationDefinition, runContext map[string]any) (map[string]any, error) {
	if s.moviePilot == nil {
		return nil, errors.New("MoviePilot 媒体识别服务未初始化")
	}
	_, sourceOutput, err := automationPredecessorOutput(node.ID, definition, runContext)
	if err != nil {
		return nil, err
	}
	if completed, _ := sourceOutput["completed"].(bool); !completed {
		return nil, errors.New("上游 115 下载尚未完成")
	}
	storageID := automationAnyUint(sourceOutput["cloud_storage_id"])
	accessMethod := strings.ToLower(automationAnyString(sourceOutput["access_method"]))
	if storageID == 0 || accessMethod == "" {
		return nil, errors.New("上游等待节点没有返回 115 账号或访问方式")
	}
	var storage model.CloudStorage
	if err := s.db.First(&storage, storageID).Error; err != nil {
		return nil, fmt.Errorf("115 账号不存在: %w", err)
	}
	if storage.StorageType != model.StorageType115Open {
		return nil, errors.New("所选云存储不是 115 账号")
	}
	if storage.Status != model.StatusActive {
		return nil, fmt.Errorf("115 账号当前状态为 %s", storage.Status)
	}
	tmdbID, err := resolveAutomationOptionalTMDBID(runContext, automationConfigString(node.Config, "tmdb_id"))
	if err != nil {
		return nil, err
	}
	tasks := automationTasksFromOutput(sourceOutput["tasks"])
	if len(tasks) == 0 {
		return nil, errors.New("上游等待节点没有返回已完成任务")
	}
	gateway := s.automation115Gateway()
	if gateway == nil {
		return nil, errors.New("115 文件查询服务未初始化")
	}

	files := make([]automation115MediaFile, 0)
	fileKeys := map[string]struct{}{}
	for _, task := range tasks {
		if task.Status != 2 || strings.TrimSpace(task.FileID) == "" {
			continue
		}
		taskFiles, err := gateway.ListMediaFiles(ctx, storage, accessMethod, task)
		if err != nil {
			return nil, err
		}
		for _, file := range taskFiles {
			key := strings.TrimSpace(file.FileID)
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(file.Path))
			}
			if key == "" {
				continue
			}
			if _, exists := fileKeys[key]; exists {
				continue
			}
			fileKeys[key] = struct{}{}
			files = append(files, file)
		}
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	output := map[string]any{
		"cloud_storage_id": storageID, "access_method": accessMethod,
		"requested_tmdb_id": tmdbID, "total_files": len(files),
		"directory_id": sourceOutput["directory_id"], "file_ids": sourceOutput["file_ids"],
		"file_names": sourceOutput["file_names"], "tasks": sourceOutput["tasks"],
	}
	if len(files) == 0 {
		output["selected_port"] = "failure"
		output["reason"] = "115 下载结果中没有找到可识别的视频文件"
		output["items"] = []any{}
		return output, nil
	}

	items := make([]map[string]any, 0, len(files))
	failed := make([]map[string]any, 0)
	technicalErrors := make([]error, 0)
	recognizer := automationRecognitionSourceAdapter{
		base: s.moviePilot, source: automationConfigString(node.Config, "recognition_source"),
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return output, err
		}
		info, recognizeInput, responded, recognizeErr := recognizeAutomationMediaFile(recognizer, file, tmdbID)
		if recognizeErr != nil {
			technicalErrors = append(technicalErrors, fmt.Errorf("%s: %w", file.Path, recognizeErr))
		}
		if strings.TrimSpace(info.TmdbID) == "" {
			reason := "MoviePilot 未识别到 TMDB ID"
			if tmdbID != "" && responded {
				reason = "MoviePilot 没有识别为指定的 TMDB ID " + tmdbID
			}
			if recognizeErr != nil {
				reason = recognizeErr.Error()
			}
			failed = append(failed, map[string]any{
				"file_id": file.FileID, "file_name": file.Name, "file_path": file.Path,
				"recognize_input": recognizeInput, "error": reason,
			})
			continue
		}
		items = append(items, automationMoviePilotOutput(file, recognizeInput, info))
	}
	output["items"] = items
	output["failed_items"] = failed
	output["recognized_count"] = len(items)
	output["failed_count"] = len(failed)
	if len(items) > 0 {
		for key, value := range items[0] {
			if key != "file_id" && key != "file_name" && key != "file_path" {
				output[key] = value
			}
		}
	}
	if len(technicalErrors) > 0 {
		output["partial"] = len(items) > 0
		return output, fmt.Errorf("MoviePilot 媒体识别请求失败: %w", errors.Join(technicalErrors...))
	}
	if len(failed) > 0 {
		output["partial"] = len(items) > 0
		output["selected_port"] = "failure"
		output["reason"] = fmt.Sprintf("%d 个媒体文件未能识别", len(failed))
		return output, nil
	}
	output["partial"] = false
	output["selected_port"] = "success"
	return output, nil
}

func (s *AutomationService) automation115Gateway() automation115Gateway {
	if s == nil {
		return nil
	}
	if s.cloud115 != nil {
		return s.cloud115
	}
	if s.web115 == nil {
		s.web115 = serviceWeb115ForAutomation(s.log)
	}
	s.cloud115 = &defaultAutomation115Gateway{web115: s.web115, httpClient: s.httpClient}
	return s.cloud115
}

func automationPredecessorOutput(nodeID string, definition AutomationDefinition, runContext map[string]any) (string, map[string]any, error) {
	nodesByID := make(map[string]AutomationNode, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodesByID[node.ID] = node
	}
	incoming := make(map[string][]AutomationEdge, len(definition.Nodes))
	for _, edge := range definition.Edges {
		incoming[edge.Target] = append(incoming[edge.Target], edge)
	}
	if len(incoming[nodeID]) == 0 {
		return "", nil, fmt.Errorf("节点 %s 没有上游节点", nodeID)
	}
	if len(incoming[nodeID]) > 1 {
		return "", nil, fmt.Errorf("节点 %s 有多个上游节点", nodeID)
	}
	edge := incoming[nodeID][0]
	predecessorID := edge.Source
	port := strings.ToLower(strings.TrimSpace(edge.SourcePort))
	visited := map[string]struct{}{}
	for port == "success" {
		predecessor, exists := nodesByID[predecessorID]
		if !exists || predecessor.Type != AutomationNodeDelay {
			break
		}
		if _, exists := visited[predecessorID]; exists {
			break
		}
		visited[predecessorID] = struct{}{}
		delayIncoming := incoming[predecessorID]
		if len(delayIncoming) != 1 {
			break
		}
		edge = delayIncoming[0]
		predecessorID = edge.Source
		port = strings.ToLower(strings.TrimSpace(edge.SourcePort))
	}
	nodes, _ := runContext["nodes"].(map[string]any)
	nodeValue, _ := nodes[predecessorID].(map[string]any)
	output, _ := nodeValue["output"].(map[string]any)
	if output == nil {
		return predecessorID, nil, fmt.Errorf("上游节点 %s 没有可用输出", predecessorID)
	}
	return predecessorID, output, nil
}

func automationDecodeNodeOutput(raw string) map[string]any {
	output := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return output
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&output) != nil {
		return map[string]any{}
	}
	return output
}

func automationOutputTime(output map[string]any, key string) time.Time {
	value := automationAnyString(output[key])
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

func automationAnyString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func automationAnyStringSlice(value any) []string {
	result := make([]string, 0)
	seen := map[string]struct{}{}
	appendValue := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	switch typed := value.(type) {
	case []string:
		for _, item := range typed {
			appendValue(item)
		}
	case []any:
		for _, item := range typed {
			appendValue(fmt.Sprint(item))
		}
	case string:
		appendValue(typed)
	}
	return result
}

func automationAnyUint(value any) uint {
	if number, ok := automationNumber(value); ok && number > 0 {
		return uint(number)
	}
	return 0
}

func automationTasksFromOutput(value any) []automation115Task {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var tasks []automation115Task
	if json.Unmarshal(encoded, &tasks) != nil {
		return nil
	}
	return tasks
}

func automationIsMediaFile(name string) bool {
	_, ok := automationMediaExtensions[strings.ToLower(path.Ext(strings.TrimSpace(name)))]
	return ok
}

func resolveAutomationOptionalTMDBID(runContext map[string]any, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", nil
	}
	resolved := configured
	if strings.HasPrefix(configured, "$") {
		value, exists := resolveAutomationReference(runContext, configured)
		if !exists {
			return "", fmt.Errorf("TMDB ID 变量 %q 不存在", configured)
		}
		resolved = strings.TrimSpace(fmt.Sprint(value))
	} else if strings.Contains(configured, "{{") {
		resolved = strings.TrimSpace(renderAutomationTemplate(configured, runContext))
	}
	if !automationTMDBIDPattern.MatchString(resolved) {
		return "", errors.New("TMDB ID 必须是正整数")
	}
	return resolved, nil
}

func recognizeAutomationMediaFile(recognizer automationMoviePilotRecognizer, file automation115MediaFile, tmdbID string) (MoviePilotMediaInfo, string, bool, error) {
	candidates := automationRecognitionCandidates(file, tmdbID)
	lastInput := ""
	responded := false
	requestErrors := make([]error, 0)
	for _, candidate := range candidates {
		lastInput = candidate
		info, _, err := recognizer.RecognizeFile(candidate)
		if err != nil {
			requestErrors = append(requestErrors, err)
			continue
		}
		responded = true
		recognizedID := strings.TrimSpace(info.TmdbID)
		if recognizedID == "" {
			continue
		}
		if tmdbID != "" && recognizedID != tmdbID {
			continue
		}
		return info, candidate, true, nil
	}
	if !responded && len(requestErrors) > 0 {
		return MoviePilotMediaInfo{}, lastInput, false, errors.Join(requestErrors...)
	}
	return MoviePilotMediaInfo{}, lastInput, responded, nil
}

func automationRecognitionCandidates(file automation115MediaFile, tmdbID string) []string {
	fileName := strings.ReplaceAll(strings.TrimSpace(file.Name), " - ", ".")
	filePath := strings.ReplaceAll(strings.TrimSpace(file.Path), " - ", ".")
	if filePath == "" {
		filePath = fileName
	}
	candidates := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.Trim(strings.TrimSpace(value), "/")
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		candidates = append(candidates, value)
	}
	if tmdbID != "" {
		markedName := automationFileNameWithTMDB(fileName, tmdbID)
		add(path.Join(path.Dir(filePath), markedName))
		add(markedName)
	}
	add(filePath)
	add(fileName)
	return candidates
}

func automationFileNameWithTMDB(fileName, tmdbID string) string {
	fileName = strings.TrimSpace(fileName)
	marker := "{tmdb-" + strings.TrimSpace(tmdbID) + "}"
	cleaned := strings.TrimSpace(automationTMDBMarkerPattern.ReplaceAllString(fileName, ""))
	extension := path.Ext(cleaned)
	stem := strings.TrimRight(strings.TrimSpace(strings.TrimSuffix(cleaned, extension)), " ._-")
	if stem == "" {
		return marker + extension
	}
	return stem + "." + marker + extension
}

func automationMoviePilotOutput(file automation115MediaFile, recognizeInput string, info MoviePilotMediaInfo) map[string]any {
	posterURL := automationTMDBImageURL(firstAutomationNonEmpty(info.BackdropPath, info.PosterPath))
	quality := extractAutomationQuality(strings.TrimSpace(info.ResourceType + " " + info.ResourcePix + " " + file.Name))
	return map[string]any{
		"file_id": file.FileID, "file_name": file.Name, "file_path": file.Path,
		"recognize_input": recognizeInput,
		"tmdb_id":         strings.TrimSpace(info.TmdbID), "media_type": strings.TrimSpace(info.MediaType),
		"title": strings.TrimSpace(info.Title), "year": strings.TrimSpace(info.Year),
		"title_year": strings.TrimSpace(info.TitleYear), "category": strings.TrimSpace(info.Category),
		"season_episode": strings.TrimSpace(info.SeasonEpisode), "rating": info.Rating,
		"poster_path": strings.TrimSpace(info.PosterPath), "backdrop_path": strings.TrimSpace(info.BackdropPath),
		"poster_url": posterURL, "quality": quality, "genres": info.Genres,
	}
}
