package systemhost

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/netip"
	"reflect"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const nm = "org.freedesktop.NetworkManager"
const nmPath = dbus.ObjectPath("/org/freedesktop/NetworkManager")

type settings = map[string]map[string]dbus.Variant
type caller interface {
	Call(context.Context, dbus.ObjectPath, string, ...any) *dbus.Call
}
type systemBus struct{ conn *dbus.Conn }

func (b systemBus) Call(ctx context.Context, p dbus.ObjectPath, method string, args ...any) *dbus.Call {
	return b.conn.Object(nm, p).CallWithContext(ctx, method, 0, args...)
}

type deviceState struct {
	device, profile dbus.ObjectPath
	saved, applied  settings
	version         uint64
	view            Network
}
type pendingChange struct {
	result     Result
	checkpoint dbus.ObjectPath
	before     deviceState
	requested  IPv4
	desired    settings
	version    uint64
}

// NM owns one in-flight change. NetworkManager (not this process) owns the 90s
// rollback timer, so killing the agent does not disable rollback.
type NM struct {
	mu      sync.Mutex
	bus     caller
	allowed []string
	pending *pendingChange
	now     func() time.Time
}

func ConnectNM(interfaces []string) (*NM, func(), error) {
	if len(interfaces) == 0 || len(interfaces) > 16 {
		return nil, nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, s := range interfaces {
		if !ValidInterface(s) || seen[s] {
			return nil, nil, ErrInvalid
		}
		seen[s] = true
	}
	conn, e := dbus.ConnectSystemBus()
	if e != nil {
		return nil, nil, ErrUnavailable
	}
	return &NM{bus: systemBus{conn}, allowed: append([]string{}, interfaces...), now: time.Now}, func() { conn.Close() }, nil
}
func (n *NM) property(ctx context.Context, p dbus.ObjectPath, iface, key string) (dbus.Variant, error) {
	var v dbus.Variant
	e := n.bus.Call(ctx, p, "org.freedesktop.DBus.Properties.Get", iface, key).Store(&v)
	return v, e
}
func value(s settings, section, key string) any { return s[section][key].Value() }
func (n *NM) inspect(ctx context.Context, name string) (s deviceState, err error) {
	allowed := false
	for _, v := range n.allowed {
		if v == name {
			allowed = true
		}
	}
	if !allowed {
		return s, ErrInvalid
	}
	if e := n.bus.Call(ctx, nmPath, nm+".GetDeviceByIpIface", name).Store(&s.device); e != nil {
		return s, ErrUnavailable
	}
	state, e := n.property(ctx, s.device, nm+".Device", "State")
	if e != nil || state.Value() != uint32(100) {
		return s, ErrUnavailable
	}
	typ, e := n.property(ctx, s.device, nm+".Device", "DeviceType")
	if e != nil || typ.Value() != uint32(1) {
		return s, ErrInvalid
	}
	if e := n.bus.Call(ctx, s.device, nm+".Device.GetAppliedConnection", uint32(0)).Store(&s.applied, &s.version); e != nil {
		return s, ErrUnavailable
	}
	active, e := n.property(ctx, s.device, nm+".Device", "ActiveConnection")
	if e != nil {
		return s, ErrUnavailable
	}
	ap, ok := active.Value().(dbus.ObjectPath)
	if !ok || ap == "/" {
		return s, ErrInvalid
	}
	profile, e := n.property(ctx, ap, nm+".Connection.Active", "Connection")
	if e != nil {
		return s, ErrUnavailable
	}
	s.profile, ok = profile.Value().(dbus.ObjectPath)
	if !ok {
		return s, ErrInvalid
	}
	flags, e := n.property(ctx, s.profile, nm+".Settings.Connection", "Flags")
	if e != nil || flags.Value() != uint32(0) {
		return s, ErrInvalid
	}
	if e := n.bus.Call(ctx, s.profile, nm+".Settings.Connection.GetSettings").Store(&s.saved); e != nil {
		return s, ErrUnavailable
	}
	for _, cfg := range []settings{s.saved, s.applied} {
		if method := value(cfg, "ipv4", "method"); method != "auto" && method != "manual" {
			return s, ErrInvalid
		}
		if value(cfg, "connection", "type") != "802-3-ethernet" {
			return s, ErrInvalid
		}
		for _, k := range []string{"master", "controller", "slave-type", "port-type"} {
			if v := value(cfg, "connection", k); v != nil && v != "" {
				return s, ErrInvalid
			}
		}
		for k := range cfg {
			switch k {
			case "connection", "802-3-ethernet", "ipv4", "ipv6", "proxy":
			default:
				return s, ErrInvalid
			}
		}
	}
	s.view = Network{Interface: name, Revision: fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%#v/%#v/%d/%s", comparableSettings(s.saved), comparableSettings(s.applied), s.version, s.profile)))), IPv4: decodeIPv4(s.applied)}
	return s, nil
}
func decodeIPv4(s settings) (v IPv4) {
	v.Addresses = []string{}
	v.DNS = []string{}
	v.Method, _ = value(s, "ipv4", "method").(string)
	v.Gateway, _ = value(s, "ipv4", "gateway").(string)
	ignore, _ := value(s, "ipv4", "ignore-auto-dns").(bool)
	v.AutomaticDNS = !ignore
	if a, ok := value(s, "ipv4", "address-data").([]map[string]dbus.Variant); ok {
		for _, x := range a {
			address, _ := x["address"].Value().(string)
			prefix, _ := x["prefix"].Value().(uint32)
			v.Addresses = append(v.Addresses, fmt.Sprintf("%s/%d", address, prefix))
		}
	}
	if a, ok := value(s, "ipv4", "dns").([]uint32); ok {
		for _, x := range a {
			var b [4]byte
			binary.NativeEndian.PutUint32(b[:], x)
			v.DNS = append(v.DNS, netip.AddrFrom4(b).String())
		}
	}
	return
}
func patchIPv4(s settings, v IPv4) settings {
	out := settings{}
	for k, section := range s {
		out[k] = map[string]dbus.Variant{}
		for key, x := range section {
			out[k][key] = x
		}
	}
	if out["ipv4"] == nil {
		out["ipv4"] = map[string]dbus.Variant{}
	}
	ip := out["ipv4"]
	delete(ip, "addresses")
	delete(ip, "dns-data")
	ip["method"] = dbus.MakeVariant(v.Method)
	ip["ignore-auto-dns"] = dbus.MakeVariant(!v.AutomaticDNS)
	addresses := []map[string]dbus.Variant{}
	for _, text := range v.Addresses {
		p, _ := netip.ParsePrefix(text)
		addresses = append(addresses, map[string]dbus.Variant{"address": dbus.MakeVariant(p.Addr().String()), "prefix": dbus.MakeVariant(uint32(p.Bits()))})
	}
	ip["address-data"] = dbus.MakeVariant(addresses)
	delete(ip, "gateway")
	if v.Gateway != "" {
		ip["gateway"] = dbus.MakeVariant(v.Gateway)
	}
	dns := []uint32{}
	for _, text := range v.DNS {
		a, _ := netip.ParseAddr(text)
		b := a.As4()
		dns = append(dns, binary.NativeEndian.Uint32(b[:]))
	}
	ip["dns"] = dbus.MakeVariant(dns)
	return out
}

// Connection timestamps are NetworkManager observations, not configuration.
func comparableSettings(s settings) settings {
	out := settings{}
	for section, values := range s {
		out[section] = map[string]dbus.Variant{}
		for key, v := range values {
			if section == "connection" && key == "timestamp" {
				continue
			}
			out[section][key] = v
		}
	}
	return out
}
func (n *NM) verifyRestored(ctx context.Context, p *pendingChange) error {
	current, e := n.inspect(ctx, p.result.Interface)
	if e != nil {
		return ErrUnavailable
	}
	if current.profile != p.before.profile || !reflect.DeepEqual(comparableSettings(current.saved), comparableSettings(p.before.saved)) || !reflect.DeepEqual(decodeIPv4(current.applied), decodeIPv4(p.before.applied)) {
		return ErrConflict
	}
	n.pending = nil
	return nil
}
func (n *NM) rollback(ctx context.Context, p *pendingChange) error {
	var results map[string]uint32
	if e := n.bus.Call(ctx, nmPath, nm+".CheckpointRollback", p.checkpoint).Store(&results); e != nil {
		return ErrUnavailable
	}
	if len(results) == 0 {
		return ErrUnavailable
	}
	for _, v := range results {
		if v != 0 {
			return ErrUnavailable
		}
	}
	p.result.State = "rollback_pending"
	// NM may schedule reactivation asynchronously. A successful rollback request
	// alone is not evidence that the original active configuration is restored.
	return n.verifyRestored(ctx, p)
}
func (n *NM) expire(ctx context.Context) {
	if n.pending == nil || n.now().Before(n.pending.result.Deadline) {
		return
	}
	// Observation must not itself change host configuration. NM owns automatic
	// rollback; explicit rollback/acknowledgement goes through the audited API.
	v, e := n.property(ctx, nmPath, nm, "Checkpoints")
	if e != nil {
		n.pending.result.State = "expiry_unknown"
		return
	}
	checkpoints, ok := v.Value().([]dbus.ObjectPath)
	if !ok {
		n.pending.result.State = "expiry_unknown"
		return
	}
	for _, path := range checkpoints {
		if path == n.pending.checkpoint {
			n.pending.result.State = "expiry_pending"
			return
		}
	}
	n.pending.result.State = "expired_unverified"
}
func (n *NM) Snapshot(ctx context.Context) ([]Network, *Result, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.expire(ctx)
	var pending *Result
	if n.pending != nil {
		v := n.pending.result
		pending = &v
	}
	out := []Network{}
	for _, name := range n.allowed {
		s, e := n.inspect(ctx, name)
		if e != nil {
			return out, pending, e
		}
		out = append(out, s.view)
	}
	return out, pending, nil
}
func (n *NM) Apply(ctx context.Context, c Change) (Result, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if e := c.Validate(); e != nil {
		return Result{}, e
	}
	n.expire(ctx)
	if n.pending != nil {
		return Result{}, ErrConflict
	}
	s, e := n.inspect(ctx, c.Interface)
	if e != nil {
		return Result{}, e
	}
	if s.view.Revision != c.ExpectedRevision {
		return Result{}, ErrConflict
	}
	p := &pendingChange{before: s, requested: c.IPv4, desired: patchIPv4(s.applied, c.IPv4), result: Result{ID: c.ID, State: "pending", Interface: c.Interface, Deadline: n.now().Add(90 * time.Second)}}
	if e := n.bus.Call(ctx, nmPath, nm+".CheckpointCreate", []dbus.ObjectPath{s.device}, uint32(90), uint32(0)).Store(&p.checkpoint); e != nil {
		return Result{}, ErrUnavailable
	}
	n.pending = p
	if e := n.bus.Call(ctx, s.device, nm+".Device.Reapply", p.desired, s.version, uint32(0)).Err; e != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if n.rollback(cleanup, p) != nil {
			p.result.State = "rollback_failed"
		}
		return p.result, ErrUnavailable
	}
	var applied settings
	if e := n.bus.Call(ctx, s.device, nm+".Device.GetAppliedConnection", uint32(0)).Store(&applied, &p.version); e != nil {
		return p.result, ErrUnavailable
	}
	// Retain NetworkManager's canonical representation for concurrent-change checks.
	p.desired = applied
	return p.result, nil
}
func (n *NM) Finish(ctx context.Context, id string, confirm bool) (Result, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.pending
	if p == nil || p.result.ID != id {
		return Result{}, ErrConflict
	}
	if !confirm {
		v, e := n.property(ctx, nmPath, nm, "Checkpoints")
		if e != nil {
			return p.result, ErrUnavailable
		}
		paths, ok := v.Value().([]dbus.ObjectPath)
		if !ok {
			return p.result, ErrUnavailable
		}
		found := false
		for _, path := range paths {
			if path == p.checkpoint {
				found = true
			}
		}
		if found {
			e = n.rollback(ctx, p)
		} else {
			e = n.verifyRestored(ctx, p)
		}
		if e != nil {
			return p.result, e
		}
		p.result.State = "rolled_back"
		return p.result, nil
	}
	if p.result.State != "pending" || n.now().Add(15*time.Second).After(p.result.Deadline) {
		return p.result, ErrConflict
	}
	s, e := n.inspect(ctx, p.result.Interface)
	if e != nil {
		return p.result, e
	}
	if s.profile != p.before.profile || !reflect.DeepEqual(comparableSettings(s.saved), comparableSettings(p.before.saved)) || s.version != p.version || !reflect.DeepEqual(s.applied, p.desired) {
		return p.result, ErrConflict
	}
	// Extend the NM-owned timeout before bounded persistence calls. Failure keeps
	// the checkpoint active; there is never a success response without destroy.
	if e := n.bus.Call(ctx, nmPath, nm+".CheckpointAdjustRollbackTimeout", p.checkpoint, uint32(60)).Err; e != nil {
		return p.result, ErrUnavailable
	}
	p.result.Deadline = n.now().Add(60 * time.Second)
	desired := patchIPv4(s.saved, p.requested)
	if e := n.bus.Call(ctx, s.profile, nm+".Settings.Connection.Update2", desired, uint32(1), map[string]dbus.Variant{}).Err; e != nil {
		p.result.State = "commit_unknown"
		return p.result, ErrUnavailable
	}
	if e := n.bus.Call(ctx, nmPath, nm+".CheckpointDestroy", p.checkpoint).Err; e != nil {
		p.result.State = "commit_unknown"
		return p.result, ErrUnavailable
	}
	p.result.State = "confirmed"
	n.pending = nil
	return p.result, nil
}

type Host struct {
	ReadOnly
	Network *NM
}

func (h *Host) Snapshot(ctx context.Context) (Snapshot, error) {
	v, e := h.ReadOnly.Snapshot(ctx)
	if e != nil {
		return v, e
	}
	if h.Network != nil {
		v.Networks, v.Pending, e = h.Network.Snapshot(ctx)
		v.NetworkEnabled = e == nil
	}
	// Monitoring remains available when a configured NIC is unsupported or NM is down.
	return v, nil
}
func (h *Host) Apply(ctx context.Context, c Change) (Result, error) {
	if h.Network == nil {
		return Result{}, ErrUnavailable
	}
	return h.Network.Apply(ctx, c)
}
func (h *Host) Finish(ctx context.Context, id string, confirm bool) (Result, error) {
	if h.Network == nil {
		return Result{}, ErrUnavailable
	}
	return h.Network.Finish(ctx, id, confirm)
}
