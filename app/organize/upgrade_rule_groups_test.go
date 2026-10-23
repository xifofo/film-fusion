package organize

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"film-fusion/app/model"
	"film-fusion/app/service"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/gin-gonic/gin"
)

func TestUpgradeRuleGroupHTTPRejectsInvalidRulesAndForeignReferences(t *testing.T) {
	e := newUpgradeTestEnv(t, false, false)
	e.must(e.db.AutoMigrate(&model.MediaUpgradeRuleGroup{}))
	handler := &Handler{Service: e.h}
	request := func(userID uint, method, target, body string) *httptest.ResponseRecorder {
		router := gin.New()
		router.Use(func(c *gin.Context) { c.Set("user_id", userID) })
		router.GET("/groups", handler.ListUpgradeRuleGroups)
		router.POST("/groups", handler.SaveUpgradeRuleGroup)
		router.PUT("/groups/:id", handler.SaveUpgradeRuleGroup)
		router.DELETE("/groups/:id", handler.DeleteUpgradeRuleGroup)
		router.POST("/test", handler.TestUpgradePolicy)
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	valid := planJSON(service.MediaUpgradeRuleGroupInput{Name: "家庭", Policy: service.MediaUpgradePresets()["quality"]})
	if got := request(0, "POST", "/groups", valid); got.Code != 401 {
		t.Fatalf("anonymous accepted: %s", got.Body)
	}
	for _, body := range []string{
		`{"name":"typo","policy":{"rules":[{"name":"unsafe","resolutions":["2160p"],"exlcude":"DV"}]}}`,
		valid + `{}`,
		`{"name":"invalid","policy":{"rules":[{"name":"unsafe","include_regex":"["}]}}`,
	} {
		if got := request(1, "POST", "/groups", body); got.Code != 400 {
			t.Fatalf("bad rule accepted: %s", got.Body)
		}
	}
	created := request(1, "POST", "/groups", valid)
	if created.Code != 200 {
		t.Fatal(created.Body)
	}
	var response struct {
		Data service.MediaUpgradeRuleGroupView `json:"data"`
	}
	e.must(json.Unmarshal(created.Body.Bytes(), &response))
	id := strconv.Itoa(int(response.Data.ID))
	if got := request(2, "PUT", "/groups/"+id, strings.Replace(valid, `"expected_revision":0`, `"expected_revision":1`, 1)); got.Code != 404 {
		t.Fatalf("cross-user update: %s", got.Body)
	}
	ref := planJSON(map[string]any{"policy": map[string]any{"enabled": true, "group_id": response.Data.ID}, "incoming": "Example.2160p.REMUX.mkv", "existing": "Example.1080p.WEB-DL.mkv"})
	if got := request(2, "POST", "/test", ref); got.Code != 400 {
		t.Fatalf("cross-user comparison: %s", got.Body)
	}
	if got := request(1, "POST", "/test", ref); got.Code != 200 || !strings.Contains(got.Body.String(), `"action":"replace"`) {
		t.Fatalf("reference comparison failed: %s", got.Body)
	}
	if got := request(1, "DELETE", "/groups/"+id+"?expected_revision=99", ""); got.Code != 409 {
		t.Fatalf("stale delete: %s", got.Body)
	}
}

func attachUpgradeRuleGroup(t *testing.T, e *upgradeTestEnv) service.MediaUpgradeRuleGroupView {
	t.Helper()
	e.must(e.db.AutoMigrate(&model.MediaUpgradeRuleGroup{}))
	group, err := service.SaveMediaUpgradeRuleGroup(e.db, 1, 0, service.MediaUpgradeRuleGroupInput{Name: "共享画质", Policy: service.MediaUpgradePresets()["quality"]})
	e.must(err)
	e.dir.UpgradePolicy = planJSON(service.MediaUpgradePolicy{Enabled: true, GroupID: group.ID})
	e.must(e.db.Model(&e.dir).Update("upgrade_policy", e.dir.UpgradePolicy).Error)
	return group
}

func TestUpgradeRuleGroupChangeInvalidatesReadyPlanBeforeFileAccess(t *testing.T) {
	e := newUpgradeTestEnv(t, false, false)
	group := attachUpgradeRuleGroup(t, e)
	e.preview()
	if e.result.UpgradePolicy == nil || e.result.UpgradePolicy.GroupRevision != 1 {
		t.Fatal("preview omitted group snapshot")
	}
	_, err := service.SaveMediaUpgradeRuleGroup(e.db, 1, group.ID, service.MediaUpgradeRuleGroupInput{Name: group.Name, ExpectedRevision: 1, Policy: service.MediaUpgradePresets()["compatible"]})
	e.must(err)
	if _, err := e.apply(); err == nil || !strings.Contains(err.Error(), "规则组已改变") {
		t.Fatalf("stale group accepted: %v", err)
	}
	if e.mutations != 0 || e.downloads != 0 {
		t.Fatal("stale plan touched media")
	}
}

func TestUpgradeRuleGroupChangeDuringSourceValidationCannotStartOldPlan(t *testing.T) {
	e := newUpgradeTestEnv(t, false, false)
	group := attachUpgradeRuleGroup(t, e)
	e.preview()
	// Inject a settings edit after the early revision check, while execution
	// is preparing its client and validating source files using the fake transport.
	e.h.newOrganizeClient = func(string) (*driver.Pan115Client, error) {
		_, err := service.SaveMediaUpgradeRuleGroup(e.db, 1, group.ID, service.MediaUpgradeRuleGroupInput{Name: group.Name, ExpectedRevision: 1, Policy: service.MediaUpgradePresets()["compatible"]})
		return e.client, err
	}
	if _, err := e.apply(); err == nil || !strings.Contains(err.Error(), "规则组已改变") {
		t.Fatalf("concurrent edit did not fence plan: %v", err)
	}
	if e.mutations != 0 || e.downloads != 0 {
		t.Fatal("stale plan changed files")
	}
	var row model.OrganizePlan
	e.must(e.db.First(&row, "id = ?", e.result.PlanID).Error)
	if row.Status != "ready" {
		t.Fatalf("stale plan was claimed: %s", row.Status)
	}
}

func TestUpgradeRuleGroupEditsDoNotBreakStartedTaskRecovery(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "restore"}[restore], func(t *testing.T) {
			e := newUpgradeTestEnv(t, true, true)
			group := attachUpgradeRuleGroup(t, e)
			e.preview()
			e.failMoveID = "101"
			result, err := e.apply()
			e.must(err)
			if result.UpgradeJob == nil || result.UpgradeJob.Status != "failed" {
				t.Fatalf("expected recovery job: %+v", result.UpgradeJob)
			}
			_, err = service.SaveMediaUpgradeRuleGroup(e.db, 1, group.ID, service.MediaUpgradeRuleGroupInput{Name: group.Name, ExpectedRevision: 1, Policy: service.MediaUpgradePresets()["compatible"]})
			e.must(err)
			e.failMoveID = ""
			job := e.resume(restore)
			if restore {
				if job.Status != "rolled_back" || e.files["201"].ParentID != "900" || e.files["101"].ParentID != "100" {
					t.Fatalf("restore failed: %+v", job)
				}
				data, err := os.ReadFile(e.oldStrm)
				e.must(err)
				if string(data) != e.oldContent {
					t.Fatal("old STRM not restored")
				}
			} else {
				if job.Status != "completed" {
					t.Fatalf("continue failed: %+v", job)
				}
				data, err := os.ReadFile(e.result.Groups[0].Items[0].StrmPath)
				e.must(err)
				if string(data) != e.result.Groups[0].Items[0].StrmContent {
					t.Fatal("STRM not synchronized")
				}
			}
		})
	}
}
