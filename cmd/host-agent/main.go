// host-agent is a Linux-only local privileged boundary; never an HTTP listener.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/systemhost"
	"golang.org/x/sys/unix"
)

type peerListener struct {
	*net.UnixListener
	uid uint32
}

func (l peerListener) Accept() (net.Conn, error) {
	for {
		c, e := l.AcceptUnix()
		if e != nil {
			return nil, e
		}
		raw, e := c.SyscallConn()
		var cred *unix.Ucred
		if e == nil {
			e = raw.Control(func(fd uintptr) { cred, _ = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
		}
		if e == nil && cred != nil && cred.Uid == l.uid {
			return c, nil
		}
		c.Close()
	}
}
func main() {
	socket := flag.String("socket", "/run/aigw-host/agent.sock", "root-owned runtime directory; socket uses service group")
	uidText := flag.String("gateway-uid", "", "numeric UID allowed to access the agent")
	interfaces := flag.String("interfaces", "", "comma-separated allowlist of active plain Ethernet interfaces")
	enabled := flag.Bool("enable-network-changes", false, "explicitly permit NetworkManager checkpointed IPv4/DNS changes")
	node := flag.String("node", "", "stable installation node name")
	flag.Parse()
	uid, e := strconv.ParseUint(*uidText, 10, 32)
	if e != nil || uid == 0 || *node == "" || len(*node) > 128 || os.Geteuid() != 0 {
		log.Fatal("root agent, non-root gateway UID and node name required")
	}
	monitor := systemhost.ReadOnly{Monitor: systemhost.NewMonitor(), Node: *node}
	agent := &systemhost.Host{ReadOnly: monitor}
	if *enabled {
		network, close, e := systemhost.ConnectNM(strings.Split(*interfaces, ","))
		if e != nil {
			log.Fatal("NetworkManager setup invalid or unavailable")
		}
		defer close()
		agent.Network = network
	}
	if !safeRuntimeDirectory(*socket) {
		log.Fatal("socket directory and ancestors must be root-owned, non-writable and free of symlinks")
	}
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: *socket, Net: "unix"})
	if e != nil {
		log.Fatal("agent socket unavailable; use a private root-owned runtime directory")
	}
	defer listener.Close()
	if e := os.Chmod(*socket, 0660); e != nil {
		log.Fatal("agent socket permissions unavailable")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	server := &http.Server{Handler: systemhost.Handler(agent), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() { <-ctx.Done(); server.Close() }()
	if e := server.Serve(peerListener{listener, uint32(uid)}); e != nil && e != http.ErrServerClosed {
		log.Fatal("host agent stopped unexpectedly")
	}
}

func safeRuntimeDirectory(socket string) bool {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return false
	}
	for dir := filepath.Dir(socket); ; dir = filepath.Dir(dir) {
		info, e := os.Lstat(dir)
		if e != nil || !info.IsDir() || info.Mode()&0022 != 0 {
			return false
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 {
			return false
		}
		if dir == "/" {
			return true
		}
	}
}
