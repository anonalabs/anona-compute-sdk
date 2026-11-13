// Package relay is the CLI's client for the stream host's framed relay:
// it opens /v1/tcp (ssh) or /v1/ws (desktop) with a viewer token and pipes
// bytes between a local reader/writer and the sandbox service.
package relay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Frame types and header size of the stream host's relay protocol.
const (
	headerLen   = 12
	typeHello   = 0x01
	typeWelcome = 0x02
	typeData    = 0x10
	typeInput   = 0x11
	typeAck     = 0x20
	typePing    = 0x21
	typePong    = 0x22
	typeClose   = 0x7F
	flagZstd    = 0x01
	chunk       = 32 << 10
	ackWindow   = 4 << 20
)

// Hello is the HELLO payload the CLI sends.
type Hello struct {
	Token   string `json:"token"`
	Channel string `json:"channel"`
	Sandbox string `json:"sandbox,omitempty"`
}

// Welcome is the relay's answer.
type Welcome struct {
	Session string `json:"session"`
	Scope   string `json:"scope"`
	Window  int    `json:"window"`
}

// CloseError is a relay CLOSE with its code (4401, 4403, 4404, 4409, 4503...).
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string { return fmt.Sprintf("stream closed %d: %s", e.Code, e.Reason) }

func frame(typ byte, payload []byte) []byte {
	b := make([]byte, headerLen+len(payload))
	b[0], b[1] = 1, typ
	copy(b[headerLen:], payload)
	return b
}

type decoded struct {
	typ     byte
	flags   byte
	seq     uint32
	payload []byte
}

func decode(b []byte) (decoded, error) {
	if len(b) < headerLen || b[0] != 1 {
		return decoded{}, errors.New("bad frame from relay")
	}
	return decoded{typ: b[1], flags: b[2], seq: binary.BigEndian.Uint32(b[8:12]), payload: b[headerLen:]}, nil
}

// Conn is an open stream socket after WELCOME.
type Conn struct {
	c       *websocket.Conn
	Welcome Welcome
	wmu     sync.Mutex
}

// Dial opens url (ws://…/v1/tcp or /v1/ws), sends HELLO and waits for WELCOME.
func Dial(ctx context.Context, url string, hello Hello, hc *http.Client) (*Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(dctx, url, &websocket.DialOptions{HTTPClient: hc, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", url, err)
	}
	c.SetReadLimit(2<<20 + headerLen)
	p, _ := json.Marshal(hello)
	if err := c.Write(dctx, websocket.MessageBinary, frame(typeHello, p)); err != nil {
		c.CloseNow()
		return nil, err
	}
	conn := &Conn{c: c}
	for {
		f, err := conn.read(dctx)
		if err != nil {
			c.CloseNow()
			return nil, err
		}
		if f.typ == typeWelcome {
			_ = json.Unmarshal(f.payload, &conn.Welcome)
			return conn, nil
		}
	}
}

// read returns the next frame; CLOSE frames and socket closes become *CloseError.
func (c *Conn) read(ctx context.Context) (decoded, error) {
	_, b, err := c.c.Read(ctx)
	if err != nil {
		if code := websocket.CloseStatus(err); code != -1 {
			return decoded{}, &CloseError{Code: int(code)}
		}
		return decoded{}, err
	}
	f, err := decode(b)
	if err != nil {
		return f, err
	}
	if f.typ == typeClose {
		var m struct {
			Code   int    `json:"code"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(f.payload, &m)
		return f, &CloseError{Code: m.Code, Reason: m.Reason}
	}
	return f, nil
}

func (c *Conn) write(ctx context.Context, b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.c.Write(ctx, websocket.MessageBinary, b)
}

// Pipe copies in to the sandbox as INPUT frames and DATA frames to out,
// until either side ends. With ack set (the /v1/ws credit window) every DATA
// frame is acknowledged once written. A normal close (1000) returns nil.
func (c *Conn) Pipe(ctx context.Context, in io.Reader, out io.Writer, ack bool) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// Reads use a context that is not cancelled early, so a CLOSE can still be read.
	go func() {
		buf := make([]byte, chunk)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if werr := c.write(ctx, frame(typeInput, buf[:n])); werr != nil {
					cancel(werr)
					return
				}
			}
			if err != nil {
				// Local side finished (stdin EOF or the TCP client hung up).
				cancel(io.EOF)
				return
			}
		}
	}()
	errc := make(chan error, 1)
	go func() {
		for {
			f, err := c.read(context.Background())
			if err != nil {
				errc <- err
				return
			}
			switch f.typ {
			case typeData:
				if f.flags&flagZstd != 0 {
					errc <- errors.New("relay sent a zstd frame on a byte pipe")
					return
				}
				if _, err := out.Write(f.payload); err != nil {
					errc <- err
					return
				}
				if ack {
					p, _ := json.Marshal(map[string]any{"seq": f.seq, "window": ackWindow})
					if err := c.write(ctx, frame(typeAck, p)); err != nil {
						errc <- err
						return
					}
				}
			case typePing:
				_ = c.write(ctx, frame(typePong, nil))
			}
		}
	}()
	var err error
	select {
	case err = <-errc:
	case <-ctx.Done():
		err = context.Cause(ctx)
	}
	_ = c.c.Close(websocket.StatusNormalClosure, "")
	var ce *CloseError
	if errors.As(err, &ce) && ce.Code == int(websocket.StatusNormalClosure) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// Close closes the socket.
func (c *Conn) Close() error { return c.c.Close(websocket.StatusNormalClosure, "") }
