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

// outBuf is a byte log that keeps only its newest bytes: a ring buffer of
// at most limit bytes (maxBuffered when zero), addressed by absolute stream
// offset. Offsets count every byte ever written, so a reader's cursor stays
// valid as old bytes are dropped.
//
// A write costs O(len(p)). The previous version appended and then copied
// the whole kept window into a new slice on every write once it was full,
// which is 4 MiB of allocation and copying per write: a chatty process kept
// a core busy just buffering its output.
type outBuf struct {
	limit int   // capacity; 0 means maxBuffered
	base  int64 // stream offset of the oldest byte kept
	head  int   // index in data of the oldest byte kept
	n     int   // bytes kept
	// data grows by append until it reaches the capacity, and from then on
	// is a ring of exactly that size. While it grows, head is 0 and
	// len(data) == n.
	data []byte
}

func (b *outBuf) capacity() int {
	if b.limit > 0 {
		return b.limit
	}
	return maxBuffered
}

func (b *outBuf) write(p []byte) {
	size := b.capacity()
	if len(p) >= size {
		// Only the last size bytes of p survive; everything before goes.
		if len(b.data) != size {
			b.data = make([]byte, size)
		}
		copy(b.data, p[len(p)-size:])
		b.base += int64(b.n + len(p) - size)
		b.head, b.n = 0, size
		return
	}
	if len(b.data) < size {
		if b.n+len(p) <= size {
			b.data = append(b.data, p...)
			b.n += len(p)
			return
		}
		// The window is about to fill: become a ring of exactly size bytes.
		ring := make([]byte, size)
		copy(ring, b.data[:b.n])
		b.data = ring
	}
	tail := (b.head + b.n) % size
	k := copy(b.data[tail:], p)
	copy(b.data, p[k:])
	b.n += len(p)
	if over := b.n - size; over > 0 {
		b.head = (b.head + over) % size
		b.n = size
		b.base += int64(over)
	}
}

func (b *outBuf) end() int64 { return b.base + int64(b.n) }

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
	stop := b.n
	if stop-start > max {
		stop = start + max
	}
	chunk = make([]byte, stop-start)
	if len(chunk) > 0 {
		i := (b.head + start) % len(b.data)
		k := copy(chunk, b.data[i:])
		copy(chunk[k:], b.data)
	}
	return chunk, b.base + int64(stop), dropped
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
