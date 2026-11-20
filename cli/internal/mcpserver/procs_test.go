package mcpserver

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// model is the obvious buffer outBuf must behave exactly like: every byte
// ever written, read through the same keep-the-last-limit window.
type model struct {
	all   []byte
	limit int
}

func (m *model) base() int64 { return int64(max(0, len(m.all)-m.limit)) }

func (m *model) from(off int64, n int) ([]byte, int64, int64) {
	var dropped int64
	if off < m.base() {
		dropped, off = m.base()-off, m.base()
	}
	if off > int64(len(m.all)) {
		off = int64(len(m.all))
	}
	stop := min(int64(len(m.all)), off+int64(n))
	return m.all[off:stop], stop, dropped
}

// Random writes of every size (empty, small, wrapping the ring, larger than
// the whole window) and random reads (before the kept window, inside it,
// past its end) agree with the model, so the cursor semantics the
// process_output tool exposes are unchanged.
func TestOutBufMatchesModel(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, limit := range []int{1, 7, 64} {
		b := &outBuf{limit: limit}
		m := &model{limit: limit}
		next := byte(0)
		for i := 0; i < 5000; i++ {
			p := make([]byte, r.IntN(limit*2+2))
			for j := range p {
				p[j], next = next, next+1
			}
			b.write(p)
			m.all = append(m.all, p...)
			if b.end() != int64(len(m.all)) || b.base != m.base() {
				t.Fatalf("limit %d write %d: end %d base %d, want %d %d", limit, i, b.end(), b.base, len(m.all), m.base())
			}
			off := int64(r.IntN(len(m.all)+limit+2)) - int64(limit)
			n := r.IntN(limit + 3)
			got, gn, gd := b.from(off, n)
			want, wn, wd := m.from(off, n)
			if !bytes.Equal(got, want) || gn != wn || gd != wd {
				t.Fatalf("limit %d write %d from(%d, %d) = %v %d %d, want %v %d %d", limit, i, off, n, got, gn, gd, want, wn, wd)
			}
		}
	}
}

// Once the 4 MiB window is full, a write must cost the bytes written, not
// the window: the old buffer allocated and copied all 4 MiB on every write.
func TestOutBufWriteDoesNotCopyTheWindow(t *testing.T) {
	var b outBuf
	b.write(bytes.Repeat([]byte("x"), maxBuffered))
	line := []byte("a line of process output\n")
	if allocs := testing.AllocsPerRun(1000, func() { b.write(line) }); allocs != 0 {
		t.Fatalf("%.0f allocations per write into a full buffer, want 0", allocs)
	}
	got, _, _ := b.from(b.end()-int64(len(line)), len(line))
	if !bytes.Equal(got, line) {
		t.Fatalf("newest bytes %q", got)
	}
}

// The run_command tail keeps the newest maxCommandOutput bytes and says when
// it dropped some.
func TestTailKeepsTheNewestOutput(t *testing.T) {
	tl := newTail()
	tl.write([]byte("short"))
	if tl.String() != "short" || tl.truncated() {
		t.Fatalf("%q %v", tl.String(), tl.truncated())
	}
	tl.write(bytes.Repeat([]byte("y"), maxCommandOutput))
	tl.write([]byte("end"))
	s := tl.String()
	if len(s) != maxCommandOutput || s[len(s)-3:] != "end" || !tl.truncated() {
		t.Fatalf("len %d suffix %q truncated %v", len(s), s[len(s)-3:], tl.truncated())
	}
}

// BenchmarkOutBufWriteFull is a small write into a full window; it was about
// a 4 MiB copy per op before the ring buffer.
func BenchmarkOutBufWriteFull(b *testing.B) {
	var buf outBuf
	buf.write(make([]byte, maxBuffered))
	line := []byte("a line of process output\n")
	b.SetBytes(int64(len(line)))
	b.ReportAllocs()
	for b.Loop() {
		buf.write(line)
	}
}
