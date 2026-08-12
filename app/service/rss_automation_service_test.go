package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
)

func TestSampleRSSAutomationSourceReturnsUpToTwentyItems(t *testing.T) {
	var feed strings.Builder
	feed.WriteString(`<rss><channel><title>动画更新</title>`)
	for index := 1; index <= 25; index++ {
		_, _ = fmt.Fprintf(
			&feed,
			`<item><guid>episode-%d</guid><title>第%d集</title></item>`,
			index,
			index,
		)
	}
	feed.WriteString(`</channel></rss>`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(feed.String()))
	}))
	defer server.Close()

	automation := &RSSAutomationService{
		httpClient: &http.Client{Timeout: time.Second},
	}
	parsed, err := automation.SampleSource(context.Background(), RSSAutomationSourceInput{
		Name:            "动画更新",
		FeedURL:         server.URL,
		IntervalMinutes: 5,
		Mapping:         DefaultRSSAutomationMapping(),
	})
	if err != nil {
		t.Fatalf("SampleSource() error = %v", err)
	}
	if len(parsed.Items) != 20 {
		t.Fatalf("SampleSource() items = %d, want 20", len(parsed.Items))
	}
	if parsed.Items[19].Fields["guid"] != "episode-20" {
		t.Fatalf("unexpected last sample: %#v", parsed.Items[19])
	}
}

func TestSampleRSSAutomationSourceUsesDatabaseUserAgent(t *testing.T) {
	const customUserAgent = "Mozilla/5.0 FilmFusion-RSS-Custom"
	var receivedUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		receivedUserAgent = request.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`<rss><channel><item><guid>1</guid><title>示例</title></item></channel></rss>`))
	}))
	defer server.Close()

	db := newRSSAutomationTestDB(t)
	if err := database.SaveRSSAutomationSettings(db, customUserAgent); err != nil {
		t.Fatalf("save RSS automation settings: %v", err)
	}
	automation := &RSSAutomationService{
		db:         db,
		httpClient: &http.Client{Timeout: time.Second},
	}
	_, err := automation.SampleSource(context.Background(), RSSAutomationSourceInput{
		Name:            "动画更新",
		FeedURL:         server.URL,
		IntervalMinutes: 5,
		Mapping:         DefaultRSSAutomationMapping(),
	})
	if err != nil {
		t.Fatalf("SampleSource() error = %v", err)
	}
	if receivedUserAgent != customUserAgent {
		t.Fatalf("User-Agent = %q, want %q", receivedUserAgent, customUserAgent)
	}
}

func TestRefreshRSSAutomationSourceRetriesWithoutBrokenConditionalHeaders(t *testing.T) {
	const lastModified = "Wed, 12 Aug 2026 06:28:59 GMT"
	requestCount := 0
	plainRequestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestCount++
		if request.Header.Get("If-Modified-Since") != "" {
			http.Error(w, "upstream failed", http.StatusInternalServerError)
			return
		}
		plainRequestCount++
		w.Header().Set("Last-Modified", lastModified)
		_, _ = w.Write([]byte(`<rss><channel><title>示例</title><item><guid>1</guid><title>示例条目</title></item></channel></rss>`))
	}))
	defer server.Close()

	db := newRSSAutomationTestDB(t)
	source := model.RSSAutomationSource{
		Name: "条件请求不兼容源", Enabled: true, Initialized: true,
		FeedURL: server.URL, IntervalMinutes: 5, MappingJSON: DefaultRSSAutomationMappingJSON(),
		LastModified: lastModified,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	automation := &RSSAutomationService{
		db:         db,
		httpClient: &http.Client{Timeout: time.Second},
	}
	if _, err := automation.refreshAutomationSource(context.Background(), source); err != nil {
		t.Fatalf("refreshAutomationSource() error = %v", err)
	}
	if requestCount != 2 || plainRequestCount != 1 {
		t.Fatalf("requests = %d, plain requests = %d; want 2 and 1", requestCount, plainRequestCount)
	}
	if err := db.First(&source, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if source.LastModified != "" || source.ETag != "" {
		t.Fatalf("broken cache validators were retained: etag=%q last_modified=%q", source.ETag, source.LastModified)
	}
	if source.LastError != "" || source.LastSuccessAt == nil {
		t.Fatalf("fallback success was not recorded: %#v", source)
	}
}

func TestRefreshRSSAutomationSourceDoesNotQueueMalformedItems(t *testing.T) {
	feed := `<rss><channel>
  <item><guid>valid</guid><title>有效条目</title></item>
  <item><guid>invalid</guid></item>
</channel></rss>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(feed))
	}))
	defer server.Close()

	db := newRSSAutomationTestDB(t)
	source := model.RSSAutomationSource{
		Name: "测试源", Enabled: true, Initialized: true,
		FeedURL: server.URL, IntervalMinutes: 5, MappingJSON: DefaultRSSAutomationMappingJSON(),
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	automation := &RSSAutomationService{
		db: db, ctx: ctx, cancel: cancel,
		httpClient:    &http.Client{Timeout: time.Second},
		executionWake: make(chan struct{}, 1),
	}

	result, err := automation.refreshAutomationSource(ctx, source)
	if err != nil {
		t.Fatalf("refreshAutomationSource() error = %v", err)
	}
	if result.Fetched != 2 || result.NewEntries != 1 || result.ParseWarnings != 1 {
		t.Fatalf("unexpected refresh result: %#v", result)
	}
	var entries []model.RSSAutomationEntry
	if err := db.Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].GUID != "valid" {
		t.Fatalf("malformed item was persisted: %#v", entries)
	}
}

func TestCreateRSSAutomationCreatesSourceAndWorkflowAtomically(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	automation := &RSSAutomationService{
		db: db, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
	}
	input := RSSAutomationCreateInput{
		Source: RSSAutomationSourceInput{
			Name: "动画更新", Enabled: true, FeedURL: "https://example.com/feed.xml",
			IntervalMinutes: 5, Mapping: DefaultRSSAutomationMapping(),
		},
		Workflow: RSSAutomationCreateWorkflowInput{
			Name: "大于 1000 集", Enabled: true, Definition: DefaultRSSAutomationDefinition(),
		},
	}
	created, err := automation.CreateAutomation(input)
	if err != nil {
		t.Fatalf("CreateAutomation() error = %v", err)
	}
	if created.Source.ID == 0 || created.Workflow.ID == 0 || created.Workflow.SourceID != created.Source.ID {
		t.Fatalf("unexpected create result: %#v", created)
	}

	input.Workflow.Definition.Nodes = nil
	if _, err := automation.CreateAutomation(input); err == nil {
		t.Fatal("invalid workflow unexpectedly created")
	}
	var sourceCount int64
	if err := db.Model(&model.RSSAutomationSource{}).Count(&sourceCount).Error; err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Fatalf("failed transaction left a source behind: %d", sourceCount)
	}
}

func TestRSSAutomationWorkflowIsUniqueAndRequiresSource(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	source := model.RSSAutomationSource{
		Name: "动画更新", Enabled: true, FeedURL: "https://example.com/feed.xml",
		IntervalMinutes: 5, MappingJSON: DefaultRSSAutomationMappingJSON(),
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	definitionJSON, err := MarshalRSSAutomationDefinition(DefaultRSSAutomationDefinition())
	if err != nil {
		t.Fatal(err)
	}
	workflow := model.RSSAutomationWorkflow{
		SourceID: source.ID, Name: "流程一", Enabled: true, Version: 1, DefinitionJSON: definitionJSON,
	}
	if err := db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := model.RSSAutomationWorkflow{
		SourceID: source.ID, Name: "流程二", Enabled: true, Version: 1, DefinitionJSON: definitionJSON,
	}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("同一个 RSS 源意外创建了第二个流程")
	}
	unbound := model.RSSAutomationWorkflow{
		SourceID: 0, Name: "全局流程", Enabled: true, Version: 1, DefinitionJSON: definitionJSON,
	}
	if err := db.Create(&unbound).Error; err == nil {
		t.Fatal("source_id=0 的全局流程意外创建成功")
	}
}

func TestRSSAutomationOneToOneLifecycle(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	automation := &RSSAutomationService{
		db: db, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
	}
	create := func(name, feedURL string) RSSAutomationCreateResult {
		result, err := automation.CreateAutomation(RSSAutomationCreateInput{
			Source: RSSAutomationSourceInput{
				Name: name, Enabled: true, FeedURL: feedURL,
				IntervalMinutes: 5, Mapping: DefaultRSSAutomationMapping(),
			},
			Workflow: RSSAutomationCreateWorkflowInput{
				Name: name + "流程", Enabled: true, Definition: DefaultRSSAutomationDefinition(),
			},
		})
		if err != nil {
			t.Fatalf("CreateAutomation() error = %v", err)
		}
		return result
	}

	first := create("动画一", "https://example.com/one.xml")
	second := create("动画二", "https://example.com/two.xml")

	if _, _, err := automation.UpdateWorkflow(first.Workflow.ID, RSSAutomationWorkflowInput{
		SourceID: second.Source.ID,
		Name:     first.Workflow.Name, Enabled: true, Definition: DefaultRSSAutomationDefinition(),
	}); err == nil {
		t.Fatal("流程意外改绑到了另一个 RSS 源")
	}

	updatedSource, err := automation.UpdateSource(first.Source.ID, RSSAutomationSourceInput{
		Name: first.Source.Name, Enabled: false, FeedURL: first.Source.FeedURL,
		IntervalMinutes: first.Source.IntervalMinutes, Mapping: DefaultRSSAutomationMapping(),
	})
	if err != nil {
		t.Fatalf("UpdateSource() error = %v", err)
	}
	if updatedSource.Enabled {
		t.Fatal("RSS 自动化源没有被停用")
	}
	var syncedWorkflow model.RSSAutomationWorkflow
	if err := db.First(&syncedWorkflow, first.Workflow.ID).Error; err != nil {
		t.Fatal(err)
	}
	if syncedWorkflow.Enabled {
		t.Fatal("RSS 源停用后，唯一流程没有同步停用")
	}

	if _, _, err := automation.UpdateWorkflow(first.Workflow.ID, RSSAutomationWorkflowInput{
		SourceID: first.Source.ID,
		Name:     first.Workflow.Name, Enabled: true, Definition: DefaultRSSAutomationDefinition(),
	}); err != nil {
		t.Fatalf("UpdateWorkflow() error = %v", err)
	}
	if err := db.First(&updatedSource, first.Source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !updatedSource.Enabled {
		t.Fatal("流程启用后，RSS 自动化源没有同步启用")
	}

	if err := automation.DeleteAutomation(first.Source.ID); err != nil {
		t.Fatalf("DeleteAutomation() error = %v", err)
	}
	var sourceCount, workflowCount int64
	if err := db.Model(&model.RSSAutomationSource{}).Where("id = ?", first.Source.ID).Count(&sourceCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RSSAutomationWorkflow{}).Where("source_id = ?", first.Source.ID).Count(&workflowCount).Error; err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 || workflowCount != 0 {
		t.Fatalf("删除后仍残留一对一配置: sources=%d workflows=%d", sourceCount, workflowCount)
	}
}

func TestSetRSSAutomationEnabledSynchronizesSourceAndWorkflow(t *testing.T) {
	db := newRSSAutomationTestDB(t)
	automation := &RSSAutomationService{
		db: db, sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
	}
	created, err := automation.CreateAutomation(RSSAutomationCreateInput{
		Source: RSSAutomationSourceInput{
			Name: "动画更新", Enabled: true, FeedURL: "https://example.com/feed.xml",
			IntervalMinutes: 5, Mapping: DefaultRSSAutomationMapping(),
		},
		Workflow: RSSAutomationCreateWorkflowInput{
			Name: "下载新条目", Enabled: true, Definition: DefaultRSSAutomationDefinition(),
		},
	})
	if err != nil {
		t.Fatalf("CreateAutomation() error = %v", err)
	}

	disabled, err := automation.SetAutomationEnabled(created.Source.ID, false)
	if err != nil {
		t.Fatalf("SetAutomationEnabled(false) error = %v", err)
	}
	if disabled.Source.Enabled || disabled.Workflow.Enabled {
		t.Fatalf("automation was not disabled: %#v", disabled)
	}

	enabled, err := automation.SetAutomationEnabled(created.Source.ID, true)
	if err != nil {
		t.Fatalf("SetAutomationEnabled(true) error = %v", err)
	}
	if !enabled.Source.Enabled || !enabled.Workflow.Enabled {
		t.Fatalf("automation was not enabled: %#v", enabled)
	}
}
