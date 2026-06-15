package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// logDir 运行日志目录（与 logger 写入目录一致）。
const logDir = "data/logs"

// 单次读取的最大尾部字节数，限制大文件下的内存与耗时（约最近 5MB 日志）。
const logMaxScanBytes int64 = 5 << 20

// 返回的最大行数上限。
const logMaxLines = 5000

// LogHandler 运行日志查看处理器
type LogHandler struct{}

// NewLogHandler 创建运行日志处理器
func NewLogHandler() *LogHandler {
	return &LogHandler{}
}

func (h *LogHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *LogHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{Code: errorCode, Message: message, Data: nil})
}

// LogFileInfo 日志文件元信息
type LogFileInfo struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

// LogEntry 解析后的单条日志
type LogEntry struct {
	Timestamp  string `json:"timestamp"`
	Level      string `json:"level"`
	Caller     string `json:"caller,omitempty"`
	Msg        string `json:"msg"`
	Stacktrace string `json:"stacktrace,omitempty"`
	Raw        string `json:"raw"`
}

// ListFiles GET /api/logs/files 列出日志目录下的 .log 文件（最新优先）。
func (h *LogHandler) ListFiles(c *gin.Context) {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			h.success(c, []LogFileInfo{}, "暂无日志文件")
			return
		}
		h.error(c, http.StatusInternalServerError, 500, "读取日志目录失败: "+err.Error())
		return
	}

	files := make([]LogFileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, LogFileInfo{
			Name:     e.Name(),
			Size:     info.Size(),
			Modified: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name > files[j].Name
	})

	h.success(c, files, "获取日志文件列表成功")
}

// validLogFileName 校验文件名安全：无路径分隔/穿越，且以 .log 结尾。
func validLogFileName(name string) bool {
	if name == "" {
		return false
	}
	if name != filepath.Base(name) {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	return strings.HasSuffix(name, ".log")
}

// GetLogs GET /api/logs?file=&lines=&level=&keyword=
// 读取指定日志文件最近 N 行（按字节尾部窗口扫描），可按级别/关键字过滤，返回最新在前。
func (h *LogHandler) GetLogs(c *gin.Context) {
	file := strings.TrimSpace(c.Query("file"))
	if file == "" {
		// 默认取最新一个日志文件
		latest, err := h.latestLogFile()
		if err != nil {
			h.error(c, http.StatusInternalServerError, 500, "读取日志目录失败: "+err.Error())
			return
		}
		if latest == "" {
			h.success(c, gin.H{"file": "", "total": 0, "entries": []LogEntry{}}, "暂无日志")
			return
		}
		file = latest
	}

	if !validLogFileName(file) {
		h.error(c, http.StatusBadRequest, 400, "非法的日志文件名")
		return
	}

	lines, _ := strconv.Atoi(c.DefaultQuery("lines", "500"))
	if lines <= 0 {
		lines = 500
	}
	if lines > logMaxLines {
		lines = logMaxLines
	}
	level := strings.ToLower(strings.TrimSpace(c.Query("level")))
	keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword")))

	fullPath := filepath.Join(logDir, file)
	// 二次防御：确保最终路径仍在 logDir 内
	absDir, _ := filepath.Abs(logDir)
	absPath, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absPath, absDir+string(os.PathSeparator)) {
		h.error(c, http.StatusBadRequest, 400, "非法的日志文件路径")
		return
	}

	rawLines, err := readTailLines(fullPath, logMaxScanBytes)
	if err != nil {
		if os.IsNotExist(err) {
			h.error(c, http.StatusNotFound, 404, "日志文件不存在")
			return
		}
		h.error(c, http.StatusInternalServerError, 500, "读取日志失败: "+err.Error())
		return
	}

	entries := make([]LogEntry, 0, len(rawLines))
	for _, line := range rawLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entry := parseLogLine(line)
		if level != "" && strings.ToLower(entry.Level) != level {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(line), keyword) {
			continue
		}
		entries = append(entries, entry)
	}

	// 取最后 N 条
	if len(entries) > lines {
		entries = entries[len(entries)-lines:]
	}

	// 最新在前
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	h.success(c, gin.H{
		"file":    file,
		"total":   len(entries),
		"entries": entries,
	}, "获取日志成功")
}

func (h *LogHandler) latestLogFile() (string, error) {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	latest := ""
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if e.Name() > latest {
			latest = e.Name()
		}
	}
	return latest, nil
}

// readTailLines 读取文件尾部最多 maxBytes 字节并按行切分（丢弃首个可能不完整的行）。
func readTailLines(path string, maxBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := fi.Size()
	start := int64(0)
	if size > maxBytes {
		start = size - maxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	content := string(data)
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if start > 0 {
		// 丢弃首个可能被截断的行
		if idx := strings.IndexByte(content, '\n'); idx >= 0 {
			content = content[idx+1:]
		}
	}
	return strings.Split(content, "\n"), nil
}

// parseLogLine 解析单行：优先按 JSON 解析，失败则原样作为 msg。
func parseLogLine(line string) LogEntry {
	entry := LogEntry{Raw: line}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "{") {
		var parsed struct {
			Level      string `json:"level"`
			Timestamp  string `json:"timestamp"`
			Caller     string `json:"caller"`
			Msg        string `json:"msg"`
			Stacktrace string `json:"stacktrace"`
		}
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			entry.Level = parsed.Level
			entry.Timestamp = parsed.Timestamp
			entry.Caller = parsed.Caller
			entry.Msg = parsed.Msg
			entry.Stacktrace = parsed.Stacktrace
			return entry
		}
	}
	entry.Msg = line
	return entry
}
