package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
)

type cloudBrowserHandlerStub struct {
	calls, browseCalls, generateCalls int
	userID, directoryID               uint
	method, cid, jobID                string
	offset, limit                     int
	err                               error
}

func (s *cloudBrowserHandlerStub) Browse(_ context.Context, userID, directoryID uint, method, cid string, offset, limit int) (*service.CloudBrowserPage, error) {
	s.calls++
	s.browseCalls++
	s.userID, s.directoryID, s.method, s.cid, s.offset, s.limit = userID, directoryID, method, cid, offset, limit
	return &service.CloudBrowserPage{CID: cid, Offset: offset, Limit: limit}, s.err
}
func (s *cloudBrowserHandlerStub) GenerateSTRM(_ context.Context, userID, directoryID uint, _ service.CloudBrowserGenerateRequest) (*service.CloudBrowserOperationResult, error) {
	s.calls++
	s.generateCalls++
	s.userID, s.directoryID = userID, directoryID
	return &service.CloudBrowserOperationResult{Status: "completed", Finished: true}, s.err
}
func (s *cloudBrowserHandlerStub) JobStatus(_ context.Context, userID, directoryID uint, jobID string) (*service.CloudBrowserOperationResult, error) {
	s.calls++
	s.userID, s.directoryID, s.jobID = userID, directoryID, jobID
	return &service.CloudBrowserOperationResult{JobID: jobID, Status: "running"}, s.err
}
func (s *cloudBrowserHandlerStub) CancelJob(ctx context.Context, userID, directoryID uint, jobID string) (*service.CloudBrowserOperationResult, error) {
	return s.JobStatus(ctx, userID, directoryID, jobID)
}

func (s *cloudBrowserHandlerStub) ActiveJob(_ context.Context, userID, directoryID uint) (*service.CloudBrowserOperationResult, error) {
	s.calls++
	s.userID, s.directoryID = userID, directoryID
	return nil, s.err
}

func cloudBrowserHandlerRouter(stub *cloudBrowserHandlerStub, user any) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if user != nil {
		router.Use(func(c *gin.Context) { c.Set("user_id", user); c.Next() })
	}
	h := NewCloudBrowserHandler(stub)
	router.GET("/api/directories/:id/browser", h.Browse)
	router.POST("/api/directories/:id/browser/strm", h.GenerateSTRM)
	router.GET("/api/directories/:id/browser/jobs", h.ActiveJob)
	router.GET("/api/directories/:id/browser/jobs/:job_id", h.JobStatus)
	router.DELETE("/api/directories/:id/browser/jobs/:job_id", h.CancelJob)
	return router
}

func cloudBrowserHandlerRequest(router http.Handler, method, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestCloudBrowserHandlerRejectsBeforeService(t *testing.T) {
	for _, user := range []any{nil, "7", uint(0)} {
		s := &cloudBrowserHandlerStub{}
		r := cloudBrowserHandlerRouter(s, user)
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			target := "/api/directories/2/browser?access_method=cookie"
			if method == http.MethodPost {
				target = "/api/directories/2/browser/strm"
			}
			if method == http.MethodDelete {
				target = "/api/directories/2/browser/jobs/" + strings.Repeat("a", 32)
			}
			if w := cloudBrowserHandlerRequest(r, method, target, `{"selection_token":"signed"}`); w.Code != http.StatusUnauthorized {
				t.Fatalf("invalid identity accepted: %d", w.Code)
			}
		}
		if s.calls != 0 {
			t.Fatal("unauthenticated request reached service")
		}
	}
	s := &cloudBrowserHandlerStub{}
	r := cloudBrowserHandlerRouter(s, uint(7))
	for _, query := range []string{
		"", "access_method=auto", "access_method=cookie&cid=01", "access_method=cookie&cid=-1", "access_method=cookie&cid=18446744073709551616",
		"access_method=cookie&offset=-1", "access_method=cookie&offset=10000001", "access_method=cookie&offset=x",
		"access_method=openapi&limit=0", "access_method=openapi&limit=1151", "access_method=openapi&limit=x",
	} {
		if w := cloudBrowserHandlerRequest(r, http.MethodGet, "/api/directories/2/browser?"+query, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("query %q accepted: %d", query, w.Code)
		}
	}
	for _, id := range []string{"0", "-1", "abc", "18446744073709551616"} {
		if w := cloudBrowserHandlerRequest(r, http.MethodGet, "/api/directories/"+id+"/browser?access_method=cookie", ""); w.Code != http.StatusBadRequest {
			t.Fatalf("directory ID %q accepted", id)
		}
	}
	for _, body := range []string{`{}`, `null`, `{"selection_token":1}`, `{"selection_token":""}`, `{"selection_token":"signed","path":"/outside"}`, `{"selection_token":"signed"}{}`, `{"selection_token":"` + strings.Repeat("x", 32769) + `"}`} {
		if w := cloudBrowserHandlerRequest(r, http.MethodPost, "/api/directories/2/browser/strm", body); w.Code != http.StatusBadRequest {
			t.Fatalf("body validation: %d %s", w.Code, w.Body.String())
		}
	}
	if w := cloudBrowserHandlerRequest(r, http.MethodPost, "/api/directories/2/browser/strm", `{"selection_token":"`+strings.Repeat("x", 65536)+`"}`); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit: %d", w.Code)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		for _, id := range []string{"bad", strings.Repeat("g", 32)} {
			if w := cloudBrowserHandlerRequest(r, method, "/api/directories/2/browser/jobs/"+id, ""); w.Code != http.StatusBadRequest {
				t.Fatalf("invalid job accepted: %d", w.Code)
			}
		}
	}
	if s.calls != 0 {
		t.Fatalf("invalid input invoked service %d times", s.calls)
	}
}

func TestCloudBrowserHandlerDefaultAndMaximumPageSize(t *testing.T) {
	for _, method := range []string{"cookie", "openapi"} {
		for _, limit := range []string{"", "&limit=1150"} {
			s := &cloudBrowserHandlerStub{}
			r := cloudBrowserHandlerRouter(s, uint(7))
			w := cloudBrowserHandlerRequest(r, http.MethodGet, "/api/directories/2/browser?access_method="+method+limit, "")
			if w.Code != http.StatusOK || s.limit != 1150 || s.browseCalls != 1 {
				t.Fatalf("method=%s limit=%s status=%d forwarded=%d calls=%d", method, limit, w.Code, s.limit, s.browseCalls)
			}
		}
	}
}

func TestCloudBrowserHandlerForwardsExplicitPageAndLocalJobActions(t *testing.T) {
	s := &cloudBrowserHandlerStub{}
	r := cloudBrowserHandlerRouter(s, uint(7))
	w := cloudBrowserHandlerRequest(r, http.MethodGet, "/api/directories/2/browser?access_method=cookie&cid=10&offset=50&limit=50", "")
	if w.Code != http.StatusOK || s.userID != 7 || s.directoryID != 2 || s.method != "cookie" || s.cid != "10" || s.offset != 50 || s.limit != 50 || s.browseCalls != 1 {
		t.Fatalf("page forwarding mismatch: %+v / %s", s, w.Body.String())
	}
	jobID := strings.Repeat("a", 32)
	if w := cloudBrowserHandlerRequest(r, http.MethodGet, "/api/directories/2/browser/jobs", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data":null`) {
		t.Fatalf("empty active job response: %d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if w := cloudBrowserHandlerRequest(r, method, "/api/directories/2/browser/jobs/"+jobID, ""); w.Code != http.StatusOK {
			t.Fatalf("job action: %d", w.Code)
		}
	}
	if s.browseCalls != 1 || s.generateCalls != 0 || s.jobID != jobID {
		t.Fatal("job status/cancel unexpectedly invoked browsing or generation")
	}
	w = cloudBrowserHandlerRequest(r, http.MethodPost, "/api/directories/2/browser/strm", `{"selection_token":"signed"}`)
	if w.Code != http.StatusOK || s.generateCalls != 1 || s.userID != 7 || s.directoryID != 2 {
		t.Fatalf("generate forwarding: %d", w.Code)
	}
}

func TestCloudBrowserHandlerSanitizesUnexpectedErrors(t *testing.T) {
	s := &cloudBrowserHandlerStub{err: errors.New("Cookie=secret provider response")}
	r := cloudBrowserHandlerRouter(s, uint(7))
	w := cloudBrowserHandlerRequest(r, http.MethodGet, "/api/directories/2/browser?access_method=cookie", "")
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("unsafe error: %d %s", w.Code, w.Body.String())
	}
	s.err = &service.CloudFolderMoveError{Status: http.StatusForbidden, Message: "无权限"}
	w = cloudBrowserHandlerRequest(r, http.MethodGet, "/api/directories/2/browser?access_method=cookie", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("service status not preserved: %d", w.Code)
	}
}
