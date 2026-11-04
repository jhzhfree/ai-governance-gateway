package observe

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCountersIsolationBoundsAndConcurrentReads(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status := 200
			if i%2 == 0 {
				status = 503
			}
			r.Record("org", "project", status, 500*time.Millisecond)
			r.Snapshot("org", "project")
		}(i)
	}
	wg.Wait()
	s := r.Snapshot("org", "project")
	if !s.Tracked || s.Counters.Requests != 100 || s.Counters.Rejected != 50 || s.Counters.ServerErrors != 50 || s.Counters.DurationMS != 50000 || s.Counters.Buckets[0] != 0 || s.Counters.Buckets[1] != 100 {
		t.Fatal("counters mismatch")
	}
	s.Counters.Buckets[1] = 0
	if r.Snapshot("org", "project").Counters.Buckets[1] != 100 {
		t.Fatal("snapshot leaked mutable state")
	}
	if r.Snapshot("other", "project").Tracked {
		t.Fatal("cross tenant counters exposed")
	}
	for i := 1; i < MaxScopes; i++ {
		r.Record("org", fmt.Sprint(i), 200, time.Second)
	}
	r.Record("new", "overflow", 200, time.Second)
	if r.Snapshot("new", "overflow").Tracked || len(r.scopes) != MaxScopes {
		t.Fatal("registry limit not enforced")
	}
}
