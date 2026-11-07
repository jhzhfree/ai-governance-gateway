package systemhost

import (
	"context"
	"errors"
	"github.com/godbus/dbus/v5"
	"strings"
	"testing"
	"time"
)

type fakeBus struct {
	deferRollback            bool
	saved, applied, original settings
	version                  uint64
	checkpoint               bool
	calls                    []string
	fail                     string
}

func wired() settings {
	return settings{"connection": {"type": dbus.MakeVariant("802-3-ethernet"), "id": dbus.MakeVariant("fixture"), "uuid": dbus.MakeVariant("synthetic")}, "ipv4": {"method": dbus.MakeVariant("auto"), "address-data": dbus.MakeVariant([]map[string]dbus.Variant{}), "dns": dbus.MakeVariant([]uint32{}), "ignore-auto-dns": dbus.MakeVariant(false)}, "ipv6": {"method": dbus.MakeVariant("auto")}}
}
func (f *fakeBus) Call(ctx context.Context, p dbus.ObjectPath, method string, args ...any) *dbus.Call {
	f.calls = append(f.calls, method)
	c := &dbus.Call{}
	if ctx.Err() != nil {
		c.Err = ctx.Err()
		return c
	}
	if method == f.fail {
		c.Err = errors.New("synthetic private failure")
		return c
	}
	switch method {
	case nm + ".GetDeviceByIpIface":
		c.Body = []any{dbus.ObjectPath("/device/1")}
	case "org.freedesktop.DBus.Properties.Get":
		var v any
		switch args[1] {
		case "State":
			v = uint32(100)
		case "DeviceType":
			v = uint32(1)
		case "ActiveConnection":
			v = dbus.ObjectPath("/active/1")
		case "Connection":
			v = dbus.ObjectPath("/profile/1")
		case "Flags":
			v = uint32(0)
		case "Checkpoints":
			a := []dbus.ObjectPath{}
			if f.checkpoint {
				a = append(a, "/checkpoint/1")
			}
			v = a
		}
		c.Body = []any{dbus.MakeVariant(v)}
	case nm + ".Device.GetAppliedConnection":
		c.Body = []any{f.applied, f.version}
	case nm + ".Settings.Connection.GetSettings":
		c.Body = []any{f.saved}
	case nm + ".CheckpointCreate":
		if f.checkpoint || args[1] != uint32(90) || args[2] != uint32(0) {
			c.Err = ErrConflict
			break
		}
		f.checkpoint = true
		f.original = f.applied
		c.Body = []any{dbus.ObjectPath("/checkpoint/1")}
	case nm + ".Device.Reapply":
		if args[1] != f.version {
			c.Err = ErrConflict
			break
		}
		f.applied = args[0].(settings)
		f.version++
	case nm + ".CheckpointRollback":
		if !f.checkpoint {
			c.Err = ErrConflict
			break
		}
		if !f.deferRollback {
			f.applied = f.original
		}
		f.checkpoint = false
		c.Body = []any{map[string]uint32{"/device/1": 0}}
	case nm + ".CheckpointAdjustRollbackTimeout":
		if !f.checkpoint {
			c.Err = ErrConflict
		}
	case nm + ".Settings.Connection.Update2":
		if args[1] != uint32(1) {
			c.Err = ErrInvalid
		}
		f.saved = args[0].(settings)
	case nm + ".CheckpointDestroy":
		if !f.checkpoint {
			c.Err = ErrConflict
		}
		f.checkpoint = false
	default:
		c.Err = errors.New("unexpected method")
	}
	return c
}
func fixtureNM() (*NM, *fakeBus) {
	b := &fakeBus{saved: wired(), applied: wired(), version: 1}
	return &NM{bus: b, allowed: []string{"eth0"}, now: time.Now}, b
}
func changeFor(t *testing.T, n *NM) Change {
	t.Helper()
	s, e := n.inspect(context.Background(), "eth0")
	if e != nil {
		t.Fatal(e)
	}
	return Change{ID: "synthetic_change_01", Interface: "eth0", ExpectedRevision: s.view.Revision, IPv4: IPv4{Method: "manual", Addresses: []string{"192.0.2.20/24"}, Gateway: "192.0.2.1", DNS: []string{"192.0.2.53"}}}
}
func TestNetworkCheckpointConfirmAndRollback(t *testing.T) {
	for _, confirm := range []bool{true, false} {
		n, b := fixtureNM()
		c := changeFor(t, n)
		v, e := n.Apply(context.Background(), c)
		if e != nil || v.State != "pending" {
			t.Fatal(e)
		}
		if value(b.saved, "ipv4", "method") != "auto" {
			t.Fatal("temporary apply persisted before confirmation")
		}
		if _, e := n.Apply(context.Background(), c); !errors.Is(e, ErrConflict) {
			t.Fatal("overlapping change accepted")
		}
		v, e = n.Finish(context.Background(), c.ID, confirm)
		if e != nil || b.checkpoint || n.pending != nil {
			t.Fatal("checkpoint not resolved", e)
		}
		if confirm {
			if v.State != "confirmed" || value(b.saved, "ipv4", "method") != "manual" {
				t.Fatal("confirmation not saved")
			}
		} else if v.State != "rolled_back" || value(b.applied, "ipv4", "method") != "auto" {
			t.Fatal("rollback failed")
		}
	}
}
func TestNetworkRejectsStaleUnsafeAndConcurrentChange(t *testing.T) {
	n, b := fixtureNM()
	c := changeFor(t, n)
	c.ExpectedRevision = strings.Repeat("0", 64)
	if _, e := n.Apply(context.Background(), c); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	c = changeFor(t, n)
	c.Interface = "eth0;reboot"
	if _, e := n.Apply(context.Background(), c); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	c = changeFor(t, n)
	if _, e := n.Apply(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	b.version++
	if _, e := n.Finish(context.Background(), c.ID, true); !errors.Is(e, ErrConflict) {
		t.Fatal("external applied change overwritten")
	}
	if !b.checkpoint {
		t.Fatal("unsafe confirm destroyed rollback checkpoint")
	}
}
func TestNetworkFailuresRemainUnknownAndRollbackIndependent(t *testing.T) {
	for _, method := range []string{nm + ".CheckpointCreate", nm + ".Device.Reapply", nm + ".Settings.Connection.Update2", nm + ".CheckpointDestroy"} {
		n, b := fixtureNM()
		c := changeFor(t, n)
		if strings.Contains(method, "Update2") || strings.Contains(method, "Destroy") {
			if _, e := n.Apply(context.Background(), c); e != nil {
				t.Fatal(e)
			}
			b.fail = method
			v, e := n.Finish(context.Background(), c.ID, true)
			if e == nil || v.State != "commit_unknown" || !b.checkpoint {
				t.Fatal("failed commit reported success or removed checkpoint")
			}
		} else {
			b.fail = method
			if _, e := n.Apply(context.Background(), c); e == nil {
				t.Fatal("failure accepted")
			}
			if value(b.saved, "ipv4", "method") != "auto" {
				t.Fatal("failure modified disk")
			}
		}
	}
}
func TestNetworkTimeoutAcknowledgementVerifiesRollback(t *testing.T) {
	n, b := fixtureNM()
	c := changeFor(t, n)
	if _, e := n.Apply(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	n.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	_, pending, e := n.Snapshot(context.Background())
	if e != nil || pending == nil || pending.State != "expiry_pending" || !b.checkpoint {
		t.Fatal("read-only observation performed rollback or claimed completion", e)
	}
	b.checkpoint = false
	if _, e := n.Finish(context.Background(), c.ID, true); !errors.Is(e, ErrConflict) {
		t.Fatal("late confirmation accepted")
	}
	if _, e := n.Finish(context.Background(), c.ID, false); !errors.Is(e, ErrConflict) {
		t.Fatal("unobserved rollback claimed")
	}
	b.applied = b.original
	v, e := n.Finish(context.Background(), c.ID, false)
	if e != nil || v.State != "rolled_back" || n.pending != nil {
		t.Fatal("verified timeout rollback not acknowledged", e)
	}
}
func TestNetworkValidationAndComplexProfileRejected(t *testing.T) {
	n, b := fixtureNM()
	valid := changeFor(t, n)
	for _, mutate := range []func(*Change){func(c *Change) { c.IPv4.DNS = []string{""} }, func(c *Change) { c.IPv4.DNS = []string{"192.0.2.53", "192.0.2.53"} }, func(c *Change) { c.IPv4.DNS = []string{"8.8.8.8;reboot"} }, func(c *Change) { c.IPv4.Addresses = []string{"127.0.0.1/8"} }, func(c *Change) { c.IPv4.Method = "disabled" }, func(c *Change) { c.IPv4.Method = "auto" }, func(c *Change) { c.ExpectedRevision = strings.Repeat("z", 64) }} {
		c := valid
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("unsafe input accepted")
		}
	}
	b.saved["802-1x"] = map[string]dbus.Variant{"identity": dbus.MakeVariant("private")}
	if _, e := n.inspect(context.Background(), "eth0"); !errors.Is(e, ErrInvalid) {
		t.Fatal("secret-bearing profile accepted")
	}
}

func TestNetworkRollbackMustObserveRestoredConfiguration(t *testing.T) {
	n, b := fixtureNM()
	c := changeFor(t, n)
	if _, e := n.Apply(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	b.deferRollback = true
	v, e := n.Finish(context.Background(), c.ID, false)
	if e == nil || v.State == "rolled_back" || n.pending == nil {
		t.Fatal("asynchronous rollback falsely reported completion")
	}
	b.applied = b.original
	// A new timestamp on reactivation must not prevent configuration comparison.
	saved := comparableSettings(b.saved)
	saved["connection"]["timestamp"] = dbus.MakeVariant(uint64(123))
	b.saved = saved
	v, e = n.Finish(context.Background(), c.ID, false)
	if e != nil || v.State != "rolled_back" || n.pending != nil {
		t.Fatal("observed recovery not acknowledged", e)
	}
}
