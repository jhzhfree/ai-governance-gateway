// Package observe keeps bounded, instance-local HTTP counters without payloads.
package observe

import (
	"crypto/rand"
	"sync"
	"time"
)

const MaxScopes = 4096

// Buckets are cumulative, in milliseconds; requests include streamed HTTP 200s.
var Bounds = []int64{100, 500, 1000, 5000, 30000, 120000}

type Counters struct {
	Requests     uint64   `json:"requests"`
	Rejected     uint64   `json:"rejected"`
	ServerErrors uint64   `json:"server_errors"`
	DurationMS   uint64   `json:"duration_ms"`
	Buckets      []uint64 `json:"buckets"`
}
type Snapshot struct {
	InstanceID string    `json:"instance_id"`
	StartedAt  time.Time `json:"started_at"`
	Tracked    bool      `json:"tracked"`
	Counters   Counters  `json:"counters"`
}
type Registry struct {
	mu      sync.Mutex
	id      string
	started time.Time
	scopes  map[[2]string]Counters
	ingress map[ingressKey]ingressCounters
}

func New() *Registry {
	return &Registry{id: rand.Text(), started: time.Now().UTC(), scopes: map[[2]string]Counters{}}
}
func (r *Registry) Record(org, project string, status int, duration time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := [2]string{org, project}
	v, exists := r.scopes[key]
	if !exists && len(r.scopes) >= MaxScopes {
		return
	}
	if v.Buckets == nil {
		v.Buckets = make([]uint64, len(Bounds))
	}
	ms := uint64(max(0, duration.Milliseconds()))
	v.Requests++
	if status == 429 || status == 503 {
		v.Rejected++
	}
	if status >= 500 {
		v.ServerErrors++
	}
	v.DurationMS += ms
	for i, bound := range Bounds {
		if ms <= uint64(bound) {
			v.Buckets[i]++
		}
	}
	r.scopes[key] = v
}
func (r *Registry) Snapshot(org, project string) *Snapshot {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	v, exists := r.scopes[[2]string{org, project}]
	v.Buckets = append([]uint64{}, v.Buckets...)
	if !exists {
		v.Buckets = make([]uint64, len(Bounds))
	}
	return &Snapshot{r.id, r.started, exists, v}
}
