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

func TestMonitorRejectsUnavailableOrInvalidRequiredMetrics(t *testing.T) {
	for _, tc := range []struct{ name, stat, mem, load string }{
		{"missing_available", "cpu 1 0 0 9 0 0 0 0", "MemTotal: 1000 kB\n", "0.1"},
		{"overflow_memory", "cpu 1 0 0 9 0 0 0 0", "MemTotal: 1000 kB\nMemAvailable: 18446744073709551615 kB", "0.1"},
		{"overflow_cpu", "cpu 18446744073709551615 1 0 0 0 0 0 0", "MemTotal: 1000 kB\nMemAvailable: 500 kB", "0.1"},
		{"nan_load", "cpu 1 0 0 9 0 0 0 0", "MemTotal: 1000 kB\nMemAvailable: 500 kB", "NaN"},
		{"infinite_load", "cpu 1 0 0 9 0 0 0 0", "MemTotal: 1000 kB\nMemAvailable: 500 kB", "+Inf"},
		{"missing_load", "cpu 1 0 0 9 0 0 0 0", "MemTotal: 1000 kB\nMemAvailable: 500 kB", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "proc"), 0700); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]string{"stat": tc.stat, "meminfo": tc.mem, "loadavg": tc.load} {
				if err := os.WriteFile(filepath.Join(root, "proc", name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			m := &Monitor{root: root}
			if _, err := m.Snapshot(context.Background()); err != ErrUnavailable {
				t.Fatalf("invalid metric accepted: %v", err)
			}
			// Failed samples must not poison the CPU baseline or become cached success.
			if !m.sampled.IsZero() {
				t.Fatal("invalid sample cached")
			}
		})
	}
}
