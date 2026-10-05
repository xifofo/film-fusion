package service

import (
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newPlaybackQueueTest(t *testing.T, callback PlaybackInfoCallback) *PersistentTaskQueue {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "queue.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(&model.MediaTask{}); err != nil {
		t.Fatal(err)
	}
	log := logger.New(config.LogConfig{Output: "stdout", Level: "error"})
	t.Cleanup(func() { _ = log.Close() })
	return &PersistentTaskQueue{db: db, log: log, cfg: &config.Config{}, playbackCallback: callback}
}

func TestPlaybackQueueStatsUseOneQueryAndKeepZeroStatuses(t *testing.T) {
	q := newPlaybackQueueTest(t, nil)
	for _, status := range []model.TaskStatus{model.TaskStatusPending, model.TaskStatusPending, model.TaskStatusCompleted, "unknown"} {
		if err := q.db.Create(&model.MediaTask{ItemID: "stats", Status: status}).Error; err != nil {
			t.Fatal(err)
		}
	}
	queries := 0
	count := func(*gorm.DB) { queries++ }
	if err := q.db.Callback().Query().After("gorm:query").Register("stats:query", count); err != nil {
		t.Fatal(err)
	}
	if err := q.db.Callback().Row().After("gorm:row").Register("stats:row", count); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetQueueStatus()
	want := map[string]int64{"pending": 2, "processing": 0, "completed": 1, "failed": 0}
	if err != nil || !reflect.DeepEqual(got, want) || queries != 1 {
		t.Fatalf("stats=%v err=%v queries=%d", got, err, queries)
	}
	if err := q.db.Where("1 = 1").Delete(&model.MediaTask{}).Error; err != nil {
		t.Fatal(err)
	}
	got, err = q.GetQueueStatus()
	if err != nil || !reflect.DeepEqual(got, map[string]int64{"pending": 0, "processing": 0, "completed": 0, "failed": 0}) {
		t.Fatalf("empty queue changed shape: %v, %v", got, err)
	}
}

func TestPlaybackQueueStopDrainsExecutionAndCanRestart(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	q := newPlaybackQueueTest(t, func(string, *config.Config) error { close(entered); <-release; return nil })
	if err := q.AddTask("item"); err != nil {
		t.Fatal(err)
	}
	q.Start()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	stopped := make(chan struct{})
	go func() { q.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Error("Stop returned before task finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not drain task")
	}
	var task model.MediaTask
	if err := q.db.First(&task).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != model.TaskStatusCompleted || q.executing.Load() {
		t.Fatalf("task not completed: %#v", task)
	}
	q.Start()
	q.Stop()
}

func TestPlaybackQueueKeepsRetryLimitAndConcurrentDedup(t *testing.T) {
	calls := 0
	q := newPlaybackQueueTest(t, func(string, *config.Config) error { calls++; return errors.New("temporary failure") })
	var adds sync.WaitGroup
	for range 16 {
		adds.Add(1)
		go func() {
			defer adds.Done()
			if err := q.AddTask("same-item"); err != nil {
				t.Error(err)
			}
		}()
	}
	adds.Wait()
	var count int64
	if err := q.db.Model(&model.MediaTask{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("dedup count=%d err=%v", count, err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if !q.processNextTask() {
			t.Fatal("pending task not claimed")
		}
		// Observe the execution flag concurrently; go test -race covers this path.
		for q.executing.Load() {
			time.Sleep(time.Millisecond)
		}
		q.taskWg.Wait()
		var task model.MediaTask
		if err := q.db.First(&task).Error; err != nil {
			t.Fatal(err)
		}
		want := model.TaskStatusPending
		if attempt == 3 {
			want = model.TaskStatusFailed
		}
		if task.Status != want {
			t.Fatalf("attempt=%d status=%s", attempt, task.Status)
		}
	}
	if calls != 3 || q.processNextTask() {
		t.Fatalf("unexpected retry count: %d", calls)
	}
}
