package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/auth"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

type cloudFolderMoveHandlerStub struct {
	browse func(context.Context, uint, uint, string, string, int, int) (*service.CloudBrowseResult, error)
	move   func(context.Context, uint, uint, service.CloudFolderMoveRequest) (*service.CloudFolderMoveResult, error)
	calls  int
}

func (s *cloudFolderMoveHandlerStub) Browse(ctx context.Context, userID, storageID uint, method, cid string, offset, limit int) (*service.CloudBrowseResult, error) {
	s.calls++
	if s.browse != nil {
		return s.browse(ctx, userID, storageID, method, cid, offset, limit)
	}
	return &service.CloudBrowseResult{}, nil
}

func (s *cloudFolderMoveHandlerStub) Move(ctx context.Context, userID, storageID uint, request service.CloudFolderMoveRequest) (*service.CloudFolderMoveResult, error) {
	s.calls++
	if s.move != nil {
		return s.move(ctx, userID, storageID, request)
	}
	return &service.CloudFolderMoveResult{Status: "moved"}, nil
}

func newCloudFolderMoveTestRouter(stub *cloudFolderMoveHandlerStub, userID any) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if userID != nil {
			c.Set("user_id", userID)
		}
	})
	handler := NewCloudFolderMoveHandler(stub)
	router.GET("/api/cloud-storage/:id/files", handler.Browse)
	router.POST("/api/cloud-storage/:id/folder-move", handler.Move)
	return router
}

func cloudFolderMoveTestRequest(router *gin.Engine, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

const validCloudFolderMoveBody = `{"access_method":"cookie","source_parent_id":"10","file_id":"99","file_name":"Movie.mkv","target_parent_id":"20","folder_name":"Movie"}`

func TestCloudFolderMoveHandlerRejectsMissingOrInvalidIdentity(t *testing.T) {
	for _, userID := range []any{nil, uint(0), 1, "1", float64(1)} {
		t.Run(fmt.Sprintf("%T-%v", userID, userID), func(t *testing.T) {
			stub := &cloudFolderMoveHandlerStub{}
			router := newCloudFolderMoveTestRouter(stub, userID)
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				target := "/api/cloud-storage/1/files?access_method=cookie"
				if method == http.MethodPost {
					target = "/api/cloud-storage/1/folder-move"
				}
				if got := cloudFolderMoveTestRequest(router, method, target, validCloudFolderMoveBody).Code; got != http.StatusUnauthorized {
					t.Errorf("identity accepted: %d", got)
				}
			}
			if stub.calls != 0 {
				t.Fatal("unauthenticated request reached provider service")
			}
		})
	}
	stub := &cloudFolderMoveHandlerStub{}
	router := newCloudFolderMoveTestRouter(stub, uint(7))
	for _, id := range []string{"0", "-1", "abc", "18446744073709551616"} {
		for _, endpoint := range []string{"files?access_method=cookie", "folder-move"} {
			method := http.MethodGet
			if endpoint == "folder-move" {
				method = http.MethodPost
			}
			if got := cloudFolderMoveTestRequest(router, method, "/api/cloud-storage/"+id+"/"+endpoint, validCloudFolderMoveBody).Code; got != http.StatusBadRequest {
				t.Errorf("storage ID %q accepted: %d", id, got)
			}
		}
	}
	if stub.calls != 0 {
		t.Fatal("invalid storage ID reached provider service")
	}
}

func TestCloudFolderMoveBrowseValidationAndContext(t *testing.T) {
	stub := &cloudFolderMoveHandlerStub{}
	router := newCloudFolderMoveTestRouter(stub, uint(7))
	for _, query := range []string{"", "access_method=auto", "access_method=cookie&cid=-1", "access_method=cookie&cid=x", "access_method=cookie&cid=00", "access_method=cookie&offset=-1", "access_method=cookie&offset=x", "access_method=cookie&limit=0", "access_method=cookie&limit=201", "access_method=cookie&limit=x"} {
		if got := cloudFolderMoveTestRequest(router, http.MethodGet, "/api/cloud-storage/8/files?"+query, "").Code; got != http.StatusBadRequest {
			t.Errorf("query %q accepted: %d", query, got)
		}
	}
	if stub.calls != 0 {
		t.Fatal("invalid browse input reached provider service")
	}
	type contextKey struct{}
	stub.browse = func(ctx context.Context, userID, storageID uint, method, cid string, offset, limit int) (*service.CloudBrowseResult, error) {
		if userID != 7 || storageID != 8 || method != "openapi" || cid != "0" || offset != 0 || limit != 100 || ctx.Value(contextKey{}) != "forwarded" {
			t.Errorf("browse arguments lost: user=%d storage=%d method=%s cid=%s offset=%d limit=%d", userID, storageID, method, cid, offset, limit)
		}
		return &service.CloudBrowseResult{}, nil
	}
	request := httptest.NewRequest(http.MethodGet, "/api/cloud-storage/8/files?access_method=openapi", nil)
	request = request.WithContext(context.WithValue(request.Context(), contextKey{}, "forwarded"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || stub.calls != 1 {
		t.Fatalf("browse failed: %d %s", recorder.Code, recorder.Body.String())
	}
	stub.browse = func(context.Context, uint, uint, string, string, int, int) (*service.CloudBrowseResult, error) {
		return nil, &service.CloudFolderMoveError{Status: http.StatusForbidden, Message: "无权访问此存储账号"}
	}
	if got := cloudFolderMoveTestRequest(router, http.MethodGet, "/api/cloud-storage/8/files?access_method=cookie", "").Code; got != http.StatusForbidden {
		t.Fatalf("ownership rejection lost: %d", got)
	}
}

func TestCloudFolderMoveHandlerRejectsInvalidBodyBeforeService(t *testing.T) {
	stub := &cloudFolderMoveHandlerStub{}
	router := newCloudFolderMoveTestRouter(stub, uint(7))
	for _, body := range []string{
		`{}`, `null`, `[]`, validCloudFolderMoveBody + `{}`,
		strings.Replace(validCloudFolderMoveBody, `"cookie"`, `"auto"`, 1),
		strings.Replace(validCloudFolderMoveBody, `"99"`, `99`, 1),
		strings.Replace(validCloudFolderMoveBody, `"99"`, `"0"`, 1),
		strings.Replace(validCloudFolderMoveBody, `"10"`, `"-1"`, 1),
		strings.Replace(validCloudFolderMoveBody, `"20"`, `"18446744073709551616"`, 1),
		strings.Replace(validCloudFolderMoveBody, `"Movie.mkv"`, `" "`, 1),
		strings.Replace(validCloudFolderMoveBody, `"Movie"`, `null`, 1),
		strings.TrimSuffix(validCloudFolderMoveBody, "}") + `,"folder_id":"0"}`,
		strings.TrimSuffix(validCloudFolderMoveBody, "}") + `,"user_id":99}`,
	} {
		if recorder := cloudFolderMoveTestRequest(router, http.MethodPost, "/api/cloud-storage/8/folder-move", body); recorder.Code != http.StatusBadRequest {
			t.Errorf("invalid body accepted: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	oversized := strings.Replace(validCloudFolderMoveBody, "Movie.mkv", strings.Repeat("a", cloudFolderMoveMaxBodyBytes), 1)
	if got := cloudFolderMoveTestRequest(router, http.MethodPost, "/api/cloud-storage/8/folder-move", oversized).Code; got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body status=%d", got)
	}
	if got := cloudFolderMoveTestRequest(router, http.MethodPost, "/api/cloud-storage/8/folder-move", validCloudFolderMoveBody+strings.Repeat(" ", cloudFolderMoveMaxBodyBytes)).Code; got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized trailing whitespace status=%d", got)
	}
	if stub.calls != 0 {
		t.Fatal("invalid move input reached provider service")
	}
}

func TestCloudFolderMoveHandlerPreservesPartialResultAndSanitizesUnknownErrors(t *testing.T) {
	stub := &cloudFolderMoveHandlerStub{}
	router := newCloudFolderMoveTestRouter(stub, uint(7))
	stub.move = func(_ context.Context, userID, storageID uint, request service.CloudFolderMoveRequest) (*service.CloudFolderMoveResult, error) {
		if userID != 7 || storageID != 8 || request.FileID != "99" || request.SourceParentID != "10" || request.TargetParentID != "20" || request.FolderName != "Movie" {
			t.Errorf("move arguments lost: user=%d storage=%d %+v", userID, storageID, request)
		}
		return &service.CloudFolderMoveResult{Status: "pending", FolderID: "500", FolderName: "Movie", FileID: "99", FileName: "Movie.mkv"}, &service.CloudFolderMoveError{Status: http.StatusBadGateway, Message: "移动结果待确认，请重试核验"}
	}
	recorder := cloudFolderMoveTestRequest(router, http.MethodPost, "/api/cloud-storage/8/folder-move", validCloudFolderMoveBody)
	var response struct {
		Code    int                           `json:"code"`
		Message string                        `json:"message"`
		Data    service.CloudFolderMoveResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || recorder.Code != http.StatusBadGateway || response.Code != -1 || response.Data.FolderID != "500" || response.Data.Status != "pending" || response.Message != "移动结果待确认，请重试核验" {
		t.Fatalf("partial result lost: %d %s %v", recorder.Code, recorder.Body.String(), err)
	}
	stub.move = func(context.Context, uint, uint, service.CloudFolderMoveRequest) (*service.CloudFolderMoveResult, error) {
		return nil, errors.New("https://115.example/?access_token=private-token Cookie=secret")
	}
	recorder = cloudFolderMoveTestRequest(router, http.MethodPost, "/api/cloud-storage/8/folder-move", validCloudFolderMoveBody)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "private-token") || strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "115.example") {
		t.Fatalf("unknown error leaked: %d %s", recorder.Code, recorder.Body.String())
	}
	stub.move = func(context.Context, uint, uint, service.CloudFolderMoveRequest) (*service.CloudFolderMoveResult, error) {
		return &service.CloudFolderMoveResult{Status: "already_moved", FolderID: "500"}, nil
	}
	recorder = cloudFolderMoveTestRequest(router, http.MethodPost, "/api/cloud-storage/8/folder-move", validCloudFolderMoveBody)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"already_moved"`) {
		t.Fatalf("idempotent success lost: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCloudFolderMoveRoutesUseCloudStorageScope(t *testing.T) {
	for _, route := range []string{"/api/cloud-storage/:id/files", "/api/cloud-storage/:id/folder-move"} {
		if got := auth.APITokenScopeForRoute(route); got != "cloud-storage" {
			t.Errorf("route %s scope=%q", route, got)
		}
	}
}

func TestCloudFolderMoveHandlerReportsSTRMFailureWithoutLosingCloudSuccess(t *testing.T) {
	stub := &cloudFolderMoveHandlerStub{move: func(context.Context, uint, uint, service.CloudFolderMoveRequest) (*service.CloudFolderMoveResult, error) {
		return &service.CloudFolderMoveResult{
			Status: "moved", FolderID: "500", FileID: "99",
			STRM: &service.CloudFolderMoveStrmResult{Status: "failed", CloudPathID: 12, LocalPath: "/strm/Movie.strm", Message: "本地 STRM 写入失败，可重试"},
		}, nil
	}}
	recorder := cloudFolderMoveTestRequest(newCloudFolderMoveTestRouter(stub, uint(7)), http.MethodPost, "/api/cloud-storage/8/folder-move", validCloudFolderMoveBody)
	var response struct {
		Code int                           `json:"code"`
		Data service.CloudFolderMoveResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || response.Code != 0 || response.Data.Status != "moved" || response.Data.FolderID != "500" || response.Data.STRM == nil || response.Data.STRM.Status != "failed" || response.Data.STRM.CloudPathID != 12 || response.Data.STRM.LocalPath != "/strm/Movie.strm" {
		t.Fatalf("cloud and STRM outcomes conflated: %d %s", recorder.Code, recorder.Body.String())
	}
}
