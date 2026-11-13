package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/envd"
)

// maxBuffered is how much output of each stream of a background process the
// server keeps; older bytes are dropped (and reported as dropped).
const maxBuffered = 4 << 20

// outBuf is an append-only byte log with a moving start.
type outBuf struct {
	base int64 // offset of data[0]
	data []byte
}

func (b *outBuf) write(p []byte) {
	b.data = append(b.data, p...)
	if over := len(b.data) - maxBuffered; over > 0 {
		b.data = append([]byte(nil), b.data[over:]...)
		b.base += int64(over)
	}
}

func (b *outBuf) end() int64 { return b.base + int64(len(b.data)) }

// from returns up to max bytes from offset off, the offset after them, and
// how many bytes before off had been dropped already.
func (b *outBuf) from(off int64, max int) (chunk []byte, next int64, dropped int64) {
	if off < b.base {
		dropped = b.base - off
		off = b.base
	}
	if off > b.end() {
		off = b.end()
	}
	start := int(off - b.base)
	stop := len(b.data)
	if stop-start > max {
		stop = start + max
	}
	return append([]byte(nil), b.data[start:stop]...), b.base + int64(stop), dropped
}

// proc is a background process started through this server.
type proc struct {
	sandbox string
	pid     uint32
	command string
	started time.Time
	client  *envd.Client

	mu       sync.Mutex
	stdout   outBuf
	stderr   outBuf
	running  bool
	exitCode *int
	status   string
	err      string
	changed  chan struct{} // closed and replaced on every update
}

func (p *proc) notify() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// procs tracks background processes by sandbox and PID.
type procs struct {
	mu sync.Mutex
	m  map[string]*proc
}

func procKey(sandbox string, pid uint32) string { return fmt.Sprintf("%s/%d", sandbox, pid) }

func (ps *procs) get(sandbox string, pid uint32) *proc {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.m[procKey(sandbox, pid)]
}

func (ps *procs) forget(sandbox string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for k, p := range ps.m {
		if p.sandbox == sandbox {
			delete(ps.m, k)
		}
	}
}

func (ps *procs) list(sandbox string) []*proc {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var out []*proc
	for _, p := range ps.m {
		if sandbox == "" || p.sandbox == sandbox {
			out = append(out, p)
		}
	}
	return out
}

// start launches command and keeps reading its output in the background
// until it exits. It returns once envd reports the PID.
func (ps *procs) start(ctx context.Context, ec *envd.Client, sandbox string, req envd.StartRequest, command string) (*proc, error) {
	// The stream outlives the tool call: it must not use the call's context.
	sctx, cancel := context.WithCancel(context.Background())
	stream, err := ec.Start(sctx, req, 0)
	if err != nil {
		cancel()
		return nil, err
	}
	first := make(chan error, 1)
	p := &proc{sandbox: sandbox, command: command, started: time.Now(), client: ec, running: true, changed: make(chan struct{})}
	go func() {
		defer cancel()
		defer stream.Close()
		gotPID := false
		for {
			ev, err := stream.Next()
			if err != nil {
				p.mu.Lock()
				if p.running {
					p.running = false
					if !errors.Is(err, io.EOF) {
						p.err = err.Error()
					}
				}
				p.notify()
				p.mu.Unlock()
				if !gotPID {
					first <- err
				}
				return
			}
			switch {
			case ev.Start != nil:
				p.pid = ev.Start.PID
				ps.mu.Lock()
				ps.m[procKey(sandbox, p.pid)] = p
				ps.mu.Unlock()
				gotPID = true
				first <- nil
			case ev.Data != nil:
				p.mu.Lock()
				p.stdout.write(ev.Data.Stdout)
				p.stdout.write(ev.Data.Pty)
				p.stderr.write(ev.Data.Stderr)
				p.notify()
				p.mu.Unlock()
			case ev.End != nil:
				p.mu.Lock()
				p.running = false
				code := int(ev.End.ExitCode)
				p.exitCode = &code
				p.status = ev.End.Status
				if ev.End.Error != nil {
					p.err = *ev.End.Error
				}
				p.notify()
				p.mu.Unlock()
			}
		}
	}()
	select {
	case err := <-first:
		if err != nil {
			return nil, fmt.Errorf("start process: %w", err)
		}
		return p, nil
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
}
