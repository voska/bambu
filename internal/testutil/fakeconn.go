package testutil

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
)

// FakeConn is an in-memory printer.Conn. OnCommand may mutate State (e.g. start a job) and returns the ack.
type FakeConn struct {
	mu        sync.Mutex
	state     map[string]any
	Commands  []map[string]any // {section: body}
	OnCommand func(f *FakeConn, section string, body map[string]any) map[string]any
	updates   chan struct{}
	Closed    bool
}

// NewFakeConn loads a push_status fixture and applies mut.
func NewFakeConn(t *testing.T, fixture string, mut func(m map[string]any)) *FakeConn {
	t.Helper()
	b, err := os.ReadFile(fixture) //nolint:gosec // test fixture
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if mut != nil {
		mut(m)
	}
	return &FakeConn{state: m, updates: make(chan struct{}, 1)}
}

// Set updates a top-level state field.
func (f *FakeConn) Set(k string, v any) {
	f.mu.Lock()
	f.state[k] = v
	f.mu.Unlock()
	select {
	case f.updates <- struct{}{}:
	default:
	}
}

// Pushall implements printer.Conn.
func (f *FakeConn) Pushall(context.Context) (map[string]any, error) { return f.State(), nil }

// State implements printer.Conn.
func (f *FakeConn) State() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(f.state)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// Updates implements printer.Conn.
func (f *FakeConn) Updates() <-chan struct{} { return f.updates }

// Command implements printer.Conn.
func (f *FakeConn) Command(_ context.Context, section string, body map[string]any) (map[string]any, error) {
	f.mu.Lock()
	f.Commands = append(f.Commands, map[string]any{section: body})
	f.mu.Unlock()
	if f.OnCommand != nil {
		return f.OnCommand(f, section, body), nil
	}
	return nil, nil //nolint:nilnil // no ack
}

// Close implements printer.Conn.
func (f *FakeConn) Close() { f.Closed = true }
