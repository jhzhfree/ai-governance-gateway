//go:build linux

package terminal

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"
	"golang.org/x/term"
)

type screenCapture struct {
	mu       sync.Mutex
	data     bytes.Buffer
	entered  chan struct{}
	exited   chan struct{}
	once     sync.Once
	exitOnce sync.Once
}

func (s *screenCapture) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := s.data.Write(data)
	if strings.Contains(s.data.String(), "\x1b[?1049h") {
		s.once.Do(func() { close(s.entered) })
	}
	if strings.Contains(s.data.String(), "\x1b[?1049l") {
		s.exitOnce.Do(func() { close(s.exited) })
	}
	return n, nil
}
func (s *screenCapture) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.data.String() }
func TestTUIRestoresRealPseudoTerminal(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	capture := &screenCapture{entered: make(chan struct{}), exited: make(chan struct{})}
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(capture, master); close(copied) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := NewModel(ctx, fakeAPI{}, testConfig(), nil, true)
	defer m.stop()
	program := tea.NewProgram(m, tea.WithInput(slave), tea.WithOutput(slave), tea.WithContext(ctx), tea.WithEnvironment([]string{"TERM=xterm-256color", "NO_COLOR=1"}), tea.WithoutSignalHandler(), tea.WithWindowSize(80, 24))
	finished := make(chan error, 1)
	go func() { _, err := program.Run(); finished <- err }()
	select {
	case <-capture.entered:
	case <-ctx.Done():
		t.Fatal("TUI did not enter alternate screen")
	}
	if _, err := master.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("TUI did not quit")
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("terminal raw mode was not restored")
	}
	// Run returning proves the writer finished, not that io.Copy has drained
	// the PTY. Closing the master here can discard the restoration sequence.
	select {
	case <-capture.exited:
	case <-ctx.Done():
		t.Fatal("alternate screen was not restored")
	}
	slave.Close()
	master.Close()
	select {
	case <-copied:
	case <-time.After(time.Second):
		t.Fatal("PTY capture did not exit")
	}
	if !strings.Contains(capture.String(), "\x1b[?1049l") {
		t.Fatal("alternate screen was not restored")
	}
}
