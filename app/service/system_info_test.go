package service

import (
	"context"
	"math"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

func TestSystemInfoCollectorCollectsCoreMetrics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info := NewSystemInfoCollector().Collect(ctx)

	if info.CollectedAt.IsZero() {
		t.Fatal("collected_at should be set")
	}
	if info.CPU.LogicalCores < 1 {
		t.Fatalf("logical_cores = %d, want at least 1", info.CPU.LogicalCores)
	}
	if info.CPU.UsagePercent < 0 || info.CPU.UsagePercent > 100 {
		t.Fatalf("cpu usage = %f, want 0..100", info.CPU.UsagePercent)
	}
	if info.Memory.Total == 0 {
		t.Fatal("memory total should be available")
	}
	if info.Disk.Total == 0 {
		t.Fatal("disk total should be available")
	}
	if info.Process.PID != int32(os.Getpid()) {
		t.Fatalf("pid = %d, want %d", info.Process.PID, os.Getpid())
	}
	if info.Process.GoVersion != runtime.Version() {
		t.Fatalf("go_version = %q, want %q", info.Process.GoVersion, runtime.Version())
	}
	if info.Process.Goroutines < 1 {
		t.Fatal("goroutine count should be available")
	}
}

func TestSystemInfoValueHelpers(t *testing.T) {
	if got := firstCPUModel([]cpu.InfoStat{{}, {ModelName: "  Example CPU  "}}); got != "Example CPU" {
		t.Fatalf("firstCPUModel() = %q", got)
	}
	for _, input := range []float64{-1, math.NaN(), math.Inf(1)} {
		if got := clampPercent(input); got != 0 {
			t.Fatalf("clampPercent(%v) = %f, want 0", input, got)
		}
	}
	if got := clampPercent(145); got != 100 {
		t.Fatalf("clampPercent(145) = %f, want 100", got)
	}
}
