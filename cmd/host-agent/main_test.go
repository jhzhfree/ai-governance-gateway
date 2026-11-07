package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnixPeerUIDBoundary(t *testing.T) {
	for _, allow := range []bool{true, false} {
		dir, e := os.MkdirTemp("", "aigw-peer-")
		if e != nil {
			t.Fatal(e)
		}
		defer os.RemoveAll(dir)
		listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "socket"), Net: "unix"})
		if e != nil {
			t.Fatal(e)
		}
		uid := uint32(os.Getuid())
		if !allow {
			uid++
		}
		ended := make(chan struct{})
		go func() {
			defer close(ended)
			c, e := (peerListener{listener, uid}).Accept()
			if e == nil {
				c.Write([]byte("ok"))
				c.Close()
			}
		}()
		client, e := net.Dial("unix", listener.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		client.SetReadDeadline(time.Now().Add(time.Second))
		b, e := io.ReadAll(client)
		client.Close()
		listener.Close()
		<-ended
		if allow && (e != nil || string(b) != "ok") {
			t.Fatal("authorized UID rejected")
		}
		if !allow && len(b) != 0 {
			t.Fatal("foreign UID accepted")
		}
	}
}
