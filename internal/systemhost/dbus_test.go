package systemhost

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// Exercise actual D-Bus marshalling over an isolated session bus. The exported
// NM service is synthetic; this is not a real NetworkManager/hardware test.
func TestNetworkManagerPrivateDBusRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "dbus-daemon", "--session", "--nofork", "--print-address=1", "--address=unix:path="+filepath.Join(t.TempDir(), "bus"))
	stdout, e := command.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = command.Start(); e != nil {
		t.Fatal("dbus-daemon required for wire contract validation", e)
	}
	defer func() { command.Process.Kill(); command.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("private D-Bus unavailable")
	}
	service, e := dbus.Connect(scanner.Text())
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	client, e := dbus.Connect(scanner.Text())
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	if _, e := service.RequestName(nm, dbus.NameFlagDoNotQueue); e != nil {
		t.Fatal(e)
	}
	_, fake := fixtureNM()
	var mu sync.Mutex
	call := func(path dbus.ObjectPath, method string, args ...any) *dbus.Call {
		mu.Lock()
		defer mu.Unlock()
		return fake.Call(ctx, path, method, args...)
	}
	convert := func(e error) *dbus.Error {
		if e != nil {
			return dbus.MakeFailedError(e)
		}
		return nil
	}
	export := func(path dbus.ObjectPath, iface string, methods map[string]interface{}) {
		t.Helper()
		if e := service.ExportMethodTable(methods, path, iface); e != nil {
			t.Fatal(e)
		}
	}
	for _, path := range []dbus.ObjectPath{nmPath, "/device/1", "/active/1", "/profile/1"} {
		export(path, "org.freedesktop.DBus.Properties", map[string]interface{}{"Get": func(iface, key string) (dbus.Variant, *dbus.Error) {
			var v dbus.Variant
			e := call(path, "org.freedesktop.DBus.Properties.Get", iface, key).Store(&v)
			return v, convert(e)
		}})
	}
	export(nmPath, nm, map[string]interface{}{
		"GetDeviceByIpIface": func(name string) (dbus.ObjectPath, *dbus.Error) {
			var p dbus.ObjectPath
			e := call(nmPath, nm+".GetDeviceByIpIface", name).Store(&p)
			return p, convert(e)
		},
		"CheckpointCreate": func(devices []dbus.ObjectPath, timeout, flags uint32) (dbus.ObjectPath, *dbus.Error) {
			var p dbus.ObjectPath
			e := call(nmPath, nm+".CheckpointCreate", devices, timeout, flags).Store(&p)
			return p, convert(e)
		},
		"CheckpointAdjustRollbackTimeout": func(p dbus.ObjectPath, timeout uint32) *dbus.Error {
			return convert(call(nmPath, nm+".CheckpointAdjustRollbackTimeout", p, timeout).Err)
		},
		"CheckpointDestroy": func(p dbus.ObjectPath) *dbus.Error { return convert(call(nmPath, nm+".CheckpointDestroy", p).Err) },
		"CheckpointRollback": func(p dbus.ObjectPath) (map[string]uint32, *dbus.Error) {
			var v map[string]uint32
			e := call(nmPath, nm+".CheckpointRollback", p).Store(&v)
			return v, convert(e)
		},
	})
	export("/device/1", nm+".Device", map[string]interface{}{
		"GetAppliedConnection": func(flags uint32) (settings, uint64, *dbus.Error) {
			var s settings
			var version uint64
			e := call("/device/1", nm+".Device.GetAppliedConnection", flags).Store(&s, &version)
			return s, version, convert(e)
		},
		"Reapply": func(s settings, version uint64, flags uint32) *dbus.Error {
			return convert(call("/device/1", nm+".Device.Reapply", s, version, flags).Err)
		},
	})
	export("/profile/1", nm+".Settings.Connection", map[string]interface{}{
		"GetSettings": func() (settings, *dbus.Error) {
			var s settings
			e := call("/profile/1", nm+".Settings.Connection.GetSettings").Store(&s)
			return s, convert(e)
		},
		"Update2": func(s settings, flags uint32, args map[string]dbus.Variant) (map[string]dbus.Variant, *dbus.Error) {
			return map[string]dbus.Variant{}, convert(call("/profile/1", nm+".Settings.Connection.Update2", s, flags, args).Err)
		},
	})
	network := &NM{bus: systemBus{client}, allowed: []string{"eth0"}, now: time.Now}
	change := changeFor(t, network)
	if _, e := network.Apply(ctx, change); e != nil {
		t.Fatal("wire apply failed", e)
	}
	state, _, e := network.Snapshot(ctx)
	if e != nil || len(state) != 1 || state[0].IPv4.DNS[0] != "192.0.2.53" {
		t.Fatal("wire DNS roundtrip failed", e)
	}
	if v, e := network.Finish(ctx, change.ID, true); e != nil || v.State != "confirmed" {
		t.Fatal("wire confirmation failed", e)
	}
}
