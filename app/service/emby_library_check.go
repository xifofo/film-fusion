package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"film-fusion/app/utils/embyhelper"

	"golang.org/x/text/unicode/norm"
)

const (
	EmbyLibraryCheckMaxNames      = 200
	embyLibraryCheckMaxNameBytes  = 1024
	embyLibraryCheckWorkers       = 4
	embyLibraryCheckPageSize      = 100
	embyLibraryCheckMaxCandidates = 500
	embyLibraryCheckMaxMatches    = 20
)

type EmbyLibrarySearcher interface {
	SearchLibraryPage(context.Context, string, int, int) (embyhelper.LibrarySearchPage, error)
}

type EmbyLibraryCheckService struct {
	emby EmbyLibrarySearcher
}

func NewEmbyLibraryCheckService(emby EmbyLibrarySearcher) *EmbyLibraryCheckService {
	return &EmbyLibraryCheckService{emby: emby}
}

type EmbyLibraryMatch struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Year    int    `json:"year,omitempty"`
	Path    string `json:"path,omitempty"`
	EmbyURL string `json:"emby_url,omitempty"`
}

type EmbyLibraryCheckResult struct {
	OriginalName string             `json:"original_name"`
	Name         string             `json:"name"`
	Status       string             `json:"status"`
	Matches      []EmbyLibraryMatch `json:"matches"`
	Error        string             `json:"error,omitempty"`
	Truncated    bool               `json:"truncated,omitempty"`
}

type EmbyLibraryCheckResponse struct {
	Results           []EmbyLibraryCheckResult `json:"results"`
	Total             int                      `json:"total"`
	DuplicatesRemoved int                      `json:"duplicates_removed"`
}

// Check keeps input order while at most four name searches run at once. All
// work remains attached to the HTTP request, with both batch and name deadlines.
func (s *EmbyLibraryCheckService) Check(ctx context.Context, names []string) (*EmbyLibraryCheckResponse, error) {
	response, err := prepareEmbyLibraryCheck(names)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if source, ok := s.emby.(interface{ LibrarySearchSnapshot() *embyhelper.EmbyClient }); ok {
		client := source.LibrarySearchSnapshot()
		defer client.Close()
		s = NewEmbyLibraryCheckService(client)
	}
	jobs := make(chan int, response.Total)
	for i := range response.Results {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for range min(embyLibraryCheckWorkers, response.Total) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				response.Results[i] = s.checkName(ctx, response.Results[i])
			}
		}()
	}
	wg.Wait()
	return response, nil
}

func prepareEmbyLibraryCheck(names []string) (*EmbyLibraryCheckResponse, error) {
	response := &EmbyLibraryCheckResponse{Results: make([]EmbyLibraryCheckResult, 0)}
	seen := make(map[string]struct{})
	rows := 0
	for _, value := range names {
		// Accept either one name per array entry or a pasted multiline entry.
		for _, line := range strings.Split(strings.ReplaceAll(value, "\r", "\n"), "\n") {
			original := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "\ufeff"))
			if original == "" {
				continue
			}
			rows++
			if rows > EmbyLibraryCheckMaxNames {
				return nil, fmt.Errorf("每次最多检查 %d 个非空名称", EmbyLibraryCheckMaxNames)
			}
			if len(original) > embyLibraryCheckMaxNameBytes {
				return nil, fmt.Errorf("第 %d 行名称过长（最多 %d 字节）", rows, embyLibraryCheckMaxNameBytes)
			}
			name := cleanEmbyLibraryName(original)
			if name == "" {
				continue
			}
			key := embyLibraryNameKey(name)
			if key == "" {
				return nil, fmt.Errorf("第 %d 行不包含有效名称", rows)
			}
			if _, exists := seen[key]; exists {
				response.DuplicatesRemoved++
				continue
			}
			seen[key] = struct{}{}
			response.Results = append(response.Results, EmbyLibraryCheckResult{
				OriginalName: original, Name: name, Matches: make([]EmbyLibraryMatch, 0),
			})
		}
	}
	response.Total = len(response.Results)
	if response.Total == 0 {
		return nil, errors.New("请至少输入一个有效名称")
	}
	return response, nil
}

var embyLibraryExtensions = map[string]bool{
	"mkv": true, "mp4": true, "avi": true, "mov": true, "wmv": true,
	"flv": true, "m4v": true, "ts": true, "m2ts": true, "mts": true,
	"mpg": true, "mpeg": true, "webm": true, "rm": true, "rmvb": true,
	"vob": true, "iso": true, "strm": true, "srt": true, "ass": true,
	"ssa": true, "sub": true, "idx": true, "vtt": true, "sup": true,
	"torrent": true,
}

func cleanEmbyLibraryName(value string) string {
	name := strings.TrimSpace(value)
	for {
		before := name
		for _, quotes := range [][2]string{{`"`, `"`}, {"'", "'"}, {"“", "”"}, {"‘", "’"}} {
			if len(name) >= len(quotes[0])+len(quotes[1]) && strings.HasPrefix(name, quotes[0]) && strings.HasSuffix(name, quotes[1]) {
				name = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(name, quotes[0]), quotes[1]))
			}
		}
		if dot := strings.LastIndexByte(name, '.'); dot >= 0 && embyLibraryExtensions[strings.ToLower(name[dot+1:])] {
			name = strings.TrimSpace(name[:dot])
		}
		if name == before {
			return name
		}
	}
}

// Separators are ignored only for equality. SearchTerm retains the cleaned
// user's spelling and punctuation, including meaningful dotted title segments.
func embyLibraryNameKey(name string) string {
	var key strings.Builder
	for _, r := range norm.NFKC.String(strings.ToLower(name)) {
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) {
			key.WriteRune(r)
		}
	}
	return key.String()
}

func (s *EmbyLibraryCheckService) checkName(ctx context.Context, result EmbyLibraryCheckResult) EmbyLibraryCheckResult {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	fail := func(err error) EmbyLibraryCheckResult {
		result.Status = "error"
		// The searcher must return safe errors; unknown errors are not reflected.
		result.Error = "Emby 查询失败，请检查连接或稍后重试"
		if errors.Is(err, context.DeadlineExceeded) {
			result.Error = "Emby 查询超时，请稍后重试"
		} else if errors.Is(err, context.Canceled) {
			result.Error = "查询已取消"
		} else if errors.Is(err, embyhelper.ErrLibrarySearchUnconfigured) {
			result.Error = embyhelper.ErrLibrarySearchUnconfigured.Error()
		}
		return result
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if s.emby == nil {
		return fail(errors.New("missing Emby client"))
	}
	key := embyLibraryNameKey(result.Name)
	seen := make(map[string]struct{})
	for start := 0; start < embyLibraryCheckMaxCandidates; {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		page, err := s.emby.SearchLibraryPage(ctx, result.Name, start, embyLibraryCheckPageSize)
		if err != nil {
			return fail(err)
		}
		exact := make([]EmbyLibraryMatch, 0)
		for _, item := range page.Items {
			if item.ID == "" || strings.TrimSpace(item.Name) == "" || (item.Type != "Movie" && item.Type != "Series") || item.IsVirtualItem || item.IsPlaceHolder || strings.EqualFold(item.LocationType, "Virtual") {
				continue
			}
			if _, exists := seen[item.ID]; exists {
				continue
			}
			seen[item.ID] = struct{}{}
			match := EmbyLibraryMatch{ID: item.ID, Name: item.Name, Type: item.Type, Year: item.ProductionYear, Path: item.Path}
			if links, ok := s.emby.(interface{ LibrarySearchItemWebURL(string) string }); ok {
				match.EmbyURL = links.LibrarySearchItemWebURL(item.ID)
			}
			if key == embyLibraryNameKey(item.Name) || (item.OriginalTitle != "" && key == embyLibraryNameKey(item.OriginalTitle)) {
				if len(exact) < embyLibraryCheckMaxMatches {
					exact = append(exact, match)
				}
			} else if len(result.Matches) < embyLibraryCheckMaxMatches {
				result.Matches = append(result.Matches, match)
			}
		}
		if len(exact) > 0 {
			result.Status, result.Matches = "found", exact
			return result
		}
		start += len(page.Items)
		if page.Total >= 0 && start >= page.Total {
			break
		}
		if len(page.Items) == 0 {
			if page.Total > start {
				return fail(errors.New("incomplete Emby pagination"))
			}
			break
		}
		if page.Total < 0 && len(page.Items) < embyLibraryCheckPageSize {
			break
		}
		if start >= embyLibraryCheckMaxCandidates {
			result.Truncated = true
		}
	}
	result.Status = "missing"
	if len(result.Matches) > 0 || result.Truncated {
		result.Status = "possible"
	}
	return result
}
