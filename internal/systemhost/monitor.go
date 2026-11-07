package systemhost

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Monitor reads the current OS/network namespace. Cgroup limits are distinct
// from /proc host totals; the first CPU sample is unknown rather than zero.
type Monitor struct {
	mu          sync.Mutex
	root        string
	total, idle uint64
	sampled     time.Time
	cached      Metrics
}

func NewMonitor() *Monitor { return &Monitor{root: "/"} }
func (m *Monitor) read(p string) (string, error) {
	b, e := os.ReadFile(filepath.Join(m.root, p))
	return string(b), e
}
func (m *Monitor) Snapshot(ctx context.Context) (Metrics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		return Metrics{}, ctx.Err()
	}
	if time.Since(m.sampled) < time.Second {
		return m.cached, nil
	}
	now := time.Now()
	v := Metrics{Scope: "current_os_namespace", SampledAt: now.UTC(), CPUCount: runtime.NumCPU(), Interfaces: []Link{}, DNS: []string{}}
	stat, e := m.read("proc/stat")
	if e != nil {
		return v, ErrUnavailable
	}
	f := strings.Fields(strings.SplitN(stat, "\n", 2)[0])
	if len(f) < 9 || f[0] != "cpu" {
		return v, ErrUnavailable
	}
	var total, idle uint64
	for i := 1; i <= 8; i++ {
		n, e := strconv.ParseUint(f[i], 10, 64)
		if e != nil {
			return v, ErrUnavailable
		}
		if n > math.MaxUint64-total {
			return v, ErrUnavailable
		}
		total += n
		if i == 4 || i == 5 {
			idle += n
		}
	}
	if !m.sampled.IsZero() && total > m.total && idle >= m.idle && idle-m.idle <= total-m.total {
		p := 100 * (1 - float64(idle-m.idle)/float64(total-m.total))
		v.CPUPercent = &p
	}
	mem, e := m.read("proc/meminfo")
	if e != nil {
		return v, ErrUnavailable
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(mem, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[2] == "kB" {
			n, e := strconv.ParseUint(f[1], 10, 64)
			if e == nil && n <= math.MaxUint64/1024 {
				values[f[0]] = n * 1024
			}
		}
	}
	v.MemoryTotal = values["MemTotal:"]
	available, present := values["MemAvailable:"]
	if !present {
		return v, ErrUnavailable
	}
	v.MemoryAvailable = available
	if v.MemoryTotal == 0 || v.MemoryAvailable > v.MemoryTotal {
		return v, ErrUnavailable
	}
	load, e := m.read("proc/loadavg")
	f = strings.Fields(load)
	if e != nil || len(f) == 0 {
		return v, ErrUnavailable
	}
	v.Load1, e = strconv.ParseFloat(f[0], 64)
	if e != nil || v.Load1 < 0 || math.IsNaN(v.Load1) || math.IsInf(v.Load1, 0) {
		return v, ErrUnavailable
	}
	if text, e := m.read("sys/fs/cgroup/memory.max"); e == nil {
		if n, e := strconv.ParseUint(strings.TrimSpace(text), 10, 64); e == nil {
			v.CgroupMemoryLimit = &n
		}
	}
	if text, e := m.read("sys/fs/cgroup/memory.current"); e == nil {
		if n, e := strconv.ParseUint(strings.TrimSpace(text), 10, 64); e == nil {
			v.CgroupMemoryCurrent = &n
		}
	}
	if text, e := m.read("sys/fs/cgroup/cpu.max"); e == nil {
		var quota, period float64
		if _, e := fmt.Sscan(text, &quota, &period); e == nil && quota > 0 && period > 0 && !math.IsInf(quota, 0) && !math.IsInf(period, 0) {
			n := quota / period
			if !math.IsInf(n, 0) && n > 0 {
				v.CgroupCPUQuota = &n
			}
		}
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		return v, ErrUnavailable
	}
	for _, i := range interfaces {
		l := Link{Name: i.Name, Up: i.Flags&net.FlagUp != 0, MTU: i.MTU, Addresses: []string{}}
		addresses, e := i.Addrs()
		if e != nil {
			return v, ErrUnavailable
		}
		for _, a := range addresses {
			l.Addresses = append(l.Addresses, a.String())
		}
		if ValidInterface(i.Name) || i.Name == "lo" {
			rx, _ := m.read("sys/class/net/" + i.Name + "/statistics/rx_bytes")
			tx, _ := m.read("sys/class/net/" + i.Name + "/statistics/tx_bytes")
			l.RXBytes, _ = strconv.ParseUint(strings.TrimSpace(rx), 10, 64)
			l.TXBytes, _ = strconv.ParseUint(strings.TrimSpace(tx), 10, 64)
		}
		v.Interfaces = append(v.Interfaces, l)
	}
	if dns, e := m.read("etc/resolv.conf"); e == nil {
		for _, line := range strings.Split(dns, "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "nameserver" {
				v.DNS = append(v.DNS, f[1])
			}
		}
	}
	m.total, m.idle, m.sampled, m.cached = total, idle, now, v
	return v, nil
}

type ReadOnly struct {
	Monitor *Monitor
	Node    string
}

func (r *ReadOnly) Snapshot(ctx context.Context) (Snapshot, error) {
	m, e := r.Monitor.Snapshot(ctx)
	return Snapshot{Node: r.Node, Metrics: m, Networks: []Network{}}, e
}
func (*ReadOnly) Apply(context.Context, Change) (Result, error) { return Result{}, ErrUnavailable }
func (*ReadOnly) Finish(context.Context, string, bool) (Result, error) {
	return Result{}, ErrUnavailable
}
