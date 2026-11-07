package systemhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMonitorSamplesCPUAndSeparatesCgroupLimits(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if e := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(root, p), []byte(s), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("proc/stat", "cpu 100 0 50 850 0 0 0 0 99 0\n")
	write("proc/meminfo", "MemTotal: 1000 kB\nMemAvailable: 500 kB\n")
	write("proc/loadavg", "0.50 0.10 0.05\n")
	write("sys/fs/cgroup/cpu.max", "200000 100000\n")
	write("sys/fs/cgroup/memory.max", "65536\n")
	write("sys/fs/cgroup/memory.current", "32768\n")
	write("etc/resolv.conf", "nameserver 192.0.2.53\n")
	m := &Monitor{root: root}
	a, e := m.Snapshot(context.Background())
	if e != nil || a.CPUPercent != nil || a.MemoryTotal != 1024000 || a.CgroupCPUQuota == nil || *a.CgroupCPUQuota != 2 {
		t.Fatal("incorrect scope or first sample", e)
	}
	m.sampled = time.Now().Add(-2 * time.Second)
	write("proc/stat", "cpu 150 0 50 900 0 0 0 0 199 0\n")
	a, e = m.Snapshot(context.Background())
	if e != nil || a.CPUPercent == nil || *a.CPUPercent != 50 {
		t.Fatal("CPU delta double-counted guest time")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := m.Snapshot(ctx); e == nil {
		t.Fatal("cancellation ignored")
	}
}
