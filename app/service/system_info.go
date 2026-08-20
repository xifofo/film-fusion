package service

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

const systemInfoCPUSampleInterval = 200 * time.Millisecond

// SystemInfo is a read-only snapshot of the machine and the FilmFusion process.
// Byte counters intentionally stay as raw bytes so every client can choose its
// own display unit without losing precision.
type SystemInfo struct {
	CollectedAt time.Time         `json:"collected_at"`
	Host        SystemHostInfo    `json:"host"`
	CPU         SystemCPUInfo     `json:"cpu"`
	Memory      SystemMemoryInfo  `json:"memory"`
	Disk        SystemDiskInfo    `json:"disk"`
	Process     SystemProcessInfo `json:"process"`
	Warnings    []string          `json:"warnings,omitempty"`
}

type SystemHostInfo struct {
	Hostname             string `json:"hostname"`
	OS                   string `json:"os"`
	Platform             string `json:"platform"`
	PlatformVersion      string `json:"platform_version"`
	KernelVersion        string `json:"kernel_version"`
	Architecture         string `json:"architecture"`
	VirtualizationSystem string `json:"virtualization_system,omitempty"`
	VirtualizationRole   string `json:"virtualization_role,omitempty"`
	UptimeSeconds        uint64 `json:"uptime_seconds"`
	BootTime             uint64 `json:"boot_time"`
	ProcessCount         uint64 `json:"process_count"`
}

type SystemCPUInfo struct {
	ModelName     string  `json:"model_name"`
	LogicalCores  int     `json:"logical_cores"`
	PhysicalCores int     `json:"physical_cores"`
	UsagePercent  float64 `json:"usage_percent"`
	Load1         float64 `json:"load_1"`
	Load5         float64 `json:"load_5"`
	Load15        float64 `json:"load_15"`
}

type SystemMemoryInfo struct {
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Available    uint64  `json:"available"`
	UsagePercent float64 `json:"usage_percent"`
}

type SystemDiskInfo struct {
	Path         string  `json:"path"`
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Available    uint64  `json:"available"`
	UsagePercent float64 `json:"usage_percent"`
}

type SystemProcessInfo struct {
	PID                int32   `json:"pid"`
	CPUUsagePercent    float64 `json:"cpu_usage_percent"`
	MemoryRSS          uint64  `json:"memory_rss"`
	MemoryUsagePercent float64 `json:"memory_usage_percent"`
	GoHeapAlloc        uint64  `json:"go_heap_alloc"`
	Goroutines         int     `json:"goroutines"`
	Threads            int32   `json:"threads"`
	UptimeSeconds      uint64  `json:"uptime_seconds"`
	GoVersion          string  `json:"go_version"`
}

// SystemInfoCollector keeps the process sampler between requests so process
// CPU usage is calculated over the interval since the previous snapshot.
type SystemInfoCollector struct {
	mu        sync.Mutex
	process   *process.Process
	diskPath  string
	startedAt time.Time
}

func NewSystemInfoCollector() *SystemInfoCollector {
	diskPath, err := os.Getwd()
	if err != nil || strings.TrimSpace(diskPath) == "" {
		diskPath = string(os.PathSeparator)
	}

	currentProcess, _ := process.NewProcess(int32(os.Getpid()))
	if currentProcess != nil {
		// Prime the non-blocking process CPU sampler. The first API request can
		// then report the interval since collector initialization instead of a
		// synthetic zero.
		_, _ = currentProcess.Percent(0)
	}

	return &SystemInfoCollector{
		process:   currentProcess,
		diskPath:  diskPath,
		startedAt: time.Now(),
	}
}

func (c *SystemInfoCollector) Collect(ctx context.Context) SystemInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	result := SystemInfo{
		CollectedAt: now.UTC(),
		Host: SystemHostInfo{
			OS:           runtime.GOOS,
			Architecture: runtime.GOARCH,
		},
		CPU: SystemCPUInfo{
			LogicalCores: runtime.NumCPU(),
		},
		Disk: SystemDiskInfo{Path: c.diskPath},
		Process: SystemProcessInfo{
			PID:           int32(os.Getpid()),
			Goroutines:    runtime.NumGoroutine(),
			UptimeSeconds: durationSeconds(now.Sub(c.startedAt)),
			GoVersion:     runtime.Version(),
		},
	}

	if hostInfo, err := host.InfoWithContext(ctx); err != nil {
		result.addWarning("主机信息", err)
	} else {
		result.Host = SystemHostInfo{
			Hostname:             hostInfo.Hostname,
			OS:                   hostInfo.OS,
			Platform:             hostInfo.Platform,
			PlatformVersion:      hostInfo.PlatformVersion,
			KernelVersion:        hostInfo.KernelVersion,
			Architecture:         firstSystemInfoValue(hostInfo.KernelArch, runtime.GOARCH),
			VirtualizationSystem: hostInfo.VirtualizationSystem,
			VirtualizationRole:   hostInfo.VirtualizationRole,
			UptimeSeconds:        hostInfo.Uptime,
			BootTime:             hostInfo.BootTime,
			ProcessCount:         hostInfo.Procs,
		}
	}

	if logicalCores, err := cpu.CountsWithContext(ctx, true); err != nil {
		result.addWarning("CPU 逻辑核心", err)
	} else if logicalCores > 0 {
		result.CPU.LogicalCores = logicalCores
	}
	if physicalCores, err := cpu.CountsWithContext(ctx, false); err != nil {
		result.addWarning("CPU 物理核心", err)
	} else {
		result.CPU.PhysicalCores = physicalCores
	}
	if cpuInfo, err := cpu.InfoWithContext(ctx); err != nil {
		result.addWarning("CPU 型号", err)
	} else {
		result.CPU.ModelName = firstCPUModel(cpuInfo)
	}
	if usage, err := cpu.PercentWithContext(ctx, systemInfoCPUSampleInterval, false); err != nil {
		result.addWarning("CPU 占用", err)
	} else if len(usage) > 0 {
		result.CPU.UsagePercent = clampPercent(usage[0])
	}
	if loadAverage, err := load.AvgWithContext(ctx); err != nil {
		result.addWarning("系统负载", err)
	} else {
		result.CPU.Load1 = finiteNumber(loadAverage.Load1)
		result.CPU.Load5 = finiteNumber(loadAverage.Load5)
		result.CPU.Load15 = finiteNumber(loadAverage.Load15)
	}

	if memoryInfo, err := mem.VirtualMemoryWithContext(ctx); err != nil {
		result.addWarning("内存占用", err)
	} else {
		result.Memory = SystemMemoryInfo{
			Total:        memoryInfo.Total,
			Used:         memoryInfo.Used,
			Available:    memoryInfo.Available,
			UsagePercent: clampPercent(memoryInfo.UsedPercent),
		}
	}

	if diskInfo, err := disk.UsageWithContext(ctx, c.diskPath); err != nil {
		result.addWarning("磁盘占用", err)
	} else {
		result.Disk = SystemDiskInfo{
			Path:         c.diskPath,
			Total:        diskInfo.Total,
			Used:         diskInfo.Used,
			Available:    diskInfo.Free,
			UsagePercent: clampPercent(diskInfo.UsedPercent),
		}
	}

	var runtimeMemory runtime.MemStats
	runtime.ReadMemStats(&runtimeMemory)
	result.Process.GoHeapAlloc = runtimeMemory.HeapAlloc
	c.collectProcessInfo(ctx, &result)

	return result
}

func (c *SystemInfoCollector) collectProcessInfo(ctx context.Context, result *SystemInfo) {
	if c.process == nil {
		result.addWarning("FilmFusion 进程", fmt.Errorf("无法读取当前进程"))
		return
	}

	if processMemory, err := c.process.MemoryInfoWithContext(ctx); err != nil {
		result.addWarning("进程内存", err)
	} else {
		result.Process.MemoryRSS = processMemory.RSS
	}
	if memoryPercent, err := c.process.MemoryPercentWithContext(ctx); err != nil {
		result.addWarning("进程内存占比", err)
	} else {
		result.Process.MemoryUsagePercent = clampPercent(float64(memoryPercent))
	}
	if processCPU, err := c.process.PercentWithContext(ctx, 0); err != nil {
		result.addWarning("进程 CPU", err)
	} else {
		// gopsutil reports process CPU in single-core units and can therefore
		// exceed 100%. Normalize it to total-machine capacity for this page.
		logicalCores := max(result.CPU.LogicalCores, 1)
		result.Process.CPUUsagePercent = clampPercent(processCPU / float64(logicalCores))
	}
	if threads, err := c.process.NumThreadsWithContext(ctx); err != nil {
		result.addWarning("进程线程", err)
	} else {
		result.Process.Threads = threads
	}
}

func (s *SystemInfo) addWarning(component string, err error) {
	if err == nil {
		return
	}
	s.Warnings = append(s.Warnings, fmt.Sprintf("%s：%v", component, err))
}

func firstCPUModel(info []cpu.InfoStat) string {
	for _, item := range info {
		if model := strings.TrimSpace(item.ModelName); model != "" {
			return model
		}
	}
	return ""
}

func firstSystemInfoValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return min(value, 100)
}

func finiteNumber(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func durationSeconds(duration time.Duration) uint64 {
	if duration <= 0 {
		return 0
	}
	return uint64(duration / time.Second)
}
