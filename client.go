package emberplus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lukirs95/goemberplusclient/pkg/ber"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
	"github.com/lukirs95/goemberplusclient/pkg/s101"
)

var (
	ErrClosed           = errors.New("emberplus: client closed")
	ErrNotFound         = errors.New("emberplus: element not found")
	ErrKeepAliveTimeout = errors.New("emberplus: keep-alive timeout")
)

// glowVersion is sent in the S101 application bytes: Glow DTD 2.50.
var glowVersion = []byte{0x32, 0x02}

type config struct {
	keepAliveIdle    time.Duration
	keepAliveTimeout time.Duration
	requestTimeout   time.Duration
	logger           *slog.Logger
}

// Option configures a Client.
type Option func(*config)

// WithKeepAlive sets how long the connection may be idle before the client
// sends a keep-alive request, and how long it waits for any data afterwards
// before closing the connection. The defaults are 5 s each (spec: "Keep-Alive
// mechanism"). An idle
// duration of 0 disables keep-alive requests; requests from the provider
// are always answered.
func WithKeepAlive(idle, timeout time.Duration) Option {
	return func(c *config) {
		c.keepAliveIdle = idle
		c.keepAliveTimeout = timeout
	}
}

// WithRequestTimeout sets the timeout for requests whose context has no
// deadline. Ember+ does not define a response to requests on unknown
// elements, so without a timeout such requests could wait forever. The
// default is 10 s.
func WithRequestTimeout(d time.Duration) Option {
	return func(c *config) { c.requestTimeout = d }
}

// WithLogger sets the logger for protocol errors that do not close the
// connection, such as undecodable messages. By default nothing is logged.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// Client is an Ember+ consumer. Its methods are safe for concurrent use.
type Client struct {
	conn io.ReadWriteCloser
	cfg  config

	wmu      sync.Mutex // serializes writes
	enc      *s101.Encoder
	builder  ber.Builder
	template s101.Frame

	mu       sync.Mutex // guards the fields below
	tree     tree
	waiters  []*waiter
	dirs     map[string]*dirRequest
	watchers map[string][]*watcher
	subs     map[string]int
	err      error

	events       *dispatcher
	invocationID atomic.Int32
	lastRecv     atomic.Int64 // UnixNano of the last received frame
	done         chan struct{}
	closeOnce    sync.Once
}

// Dial connects to an Ember+ provider via TCP.
func Dial(ctx context.Context, addr string, opts ...Option) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("emberplus: %w", err)
	}
	return NewClient(conn, opts...), nil
}

// NewClient starts a client on an established connection. The client owns
// conn and closes it on Close.
func NewClient(conn io.ReadWriteCloser, opts ...Option) *Client {
	cfg := config{
		keepAliveIdle:    5 * time.Second,
		keepAliveTimeout: 5 * time.Second,
		requestTimeout:   10 * time.Second,
		logger:           slog.New(slog.DiscardHandler),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	c := &Client{
		conn: conn,
		cfg:  cfg,
		enc:  s101.NewEncoder(conn),
		template: s101.Frame{
			Slot:     s101.SlotDefault,
			Message:  s101.MessageEmBER,
			Version:  s101.VersionS101,
			DTD:      s101.DTDGlow,
			AppBytes: glowVersion,
		},
		tree:     newTree(),
		dirs:     map[string]*dirRequest{},
		watchers: map[string][]*watcher{},
		subs:     map[string]int{},
		events:   newDispatcher(),
		done:     make(chan struct{}),
	}
	c.lastRecv.Store(time.Now().UnixNano())
	go c.readLoop()
	go c.events.run(c.done)
	if cfg.keepAliveIdle > 0 {
		go c.keepAlive()
	}
	return c
}

// Close closes the connection. Pending requests return ErrClosed.
func (c *Client) Close() error {
	c.shutdown(ErrClosed)
	return nil
}

// Done is closed when the connection is closed, by Close or because of an
// error.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Err returns nil while the connection is open. Afterwards it returns
// ErrClosed after Close, or the error that closed the connection.
func (c *Client) Err() error {
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.err
	default:
		return nil
	}
}

func (c *Client) shutdown(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
		c.conn.Close()
	})
}

func (c *Client) readLoop() {
	dec := s101.NewDecoder(c.conn)
	var a s101.Assembler
	var f s101.Frame
	for {
		err := dec.ReadFrame(&f)
		switch {
		case errors.Is(err, s101.ErrInvalidChecksum), errors.Is(err, s101.ErrInvalidFrame), errors.Is(err, s101.ErrFrameTooLarge):
			c.cfg.logger.Warn("emberplus: discarding frame", "err", err)
			continue
		case err != nil:
			c.shutdown(fmt.Errorf("emberplus: connection lost: %w", err))
			return
		}
		c.lastRecv.Store(time.Now().UnixNano())

		switch f.Command {
		case s101.CommandKeepAliveRequest:
			// Answering is mandatory (spec: "Keep-Alive mechanism"). The slot of the request is kept.
			resp := s101.NewKeepAliveResponse()
			resp.Slot = f.Slot
			if err := c.writeFrame(&resp); err != nil {
				return
			}
			continue
		case s101.CommandEmBER:
		default:
			continue
		}

		payload, complete, err := a.Add(&f)
		if err != nil {
			c.cfg.logger.Warn("emberplus: discarding packet", "err", err)
			continue
		}
		if !complete || len(payload) == 0 {
			continue
		}
		msg, err := glow.Decode(payload)
		if err != nil {
			c.cfg.logger.Warn("emberplus: discarding message", "err", err)
			continue
		}
		c.handle(msg)
	}
}

// handle applies a message to the mirror and wakes up matching requests.
func (c *Client) handle(msg glow.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch m := msg.(type) {
	case glow.Elements:
		for _, el := range m {
			if e := c.tree.apply(el); e != nil {
				c.notify(e, el)
			}
		}
	case glow.Streams:
		for _, u := range c.tree.applyStreams(m) {
			c.notify(u.entry, u.update)
		}
	}
	c.wakeWaiters(msg)
}

func (c *Client) keepAlive() {
	idle, timeout := c.cfg.keepAliveIdle, c.cfg.keepAliveTimeout
	t := time.NewTimer(idle)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		since := time.Since(time.Unix(0, c.lastRecv.Load()))
		if since < idle {
			t.Reset(idle - since)
			continue
		}
		sent := time.Now()
		req := s101.NewKeepAliveRequest()
		if err := c.writeFrame(&req); err != nil {
			return
		}
		t.Reset(timeout)
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		if c.lastRecv.Load() < sent.UnixNano() {
			c.shutdown(ErrKeepAliveTimeout)
			return
		}
		t.Reset(idle)
	}
}

type writeDeadliner interface {
	SetWriteDeadline(time.Time) error
}

func (c *Client) setWriteDeadline(t time.Time) {
	if d, ok := c.conn.(writeDeadliner); ok {
		d.SetWriteDeadline(t)
	}
}

// writeFrame writes a single frame such as a keep-alive.
func (c *Client) writeFrame(f *s101.Frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.Err(); err != nil {
		return err
	}
	var deadline time.Time
	if c.cfg.keepAliveTimeout > 0 {
		deadline = time.Now().Add(c.cfg.keepAliveTimeout)
	}
	c.setWriteDeadline(deadline)
	if err := c.enc.WriteFrame(f); err != nil {
		c.shutdown(fmt.Errorf("emberplus: write: %w", err))
		return c.Err()
	}
	return nil
}

// send encodes elems as one message. A write error closes the connection,
// since a partially written message cannot be recovered.
func (c *Client) send(ctx context.Context, elems ...glow.Element) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.Err(); err != nil {
		return err
	}
	c.builder.Reset()
	if err := glow.Encode(&c.builder, elems...); err != nil {
		return err
	}
	payload, _ := c.builder.Bytes()
	deadline, _ := ctx.Deadline()
	c.setWriteDeadline(deadline)
	if err := s101.Split(c.template, payload, c.enc.WriteFrame); err != nil {
		c.shutdown(fmt.Errorf("emberplus: write: %w", err))
		return c.Err()
	}
	return nil
}

// withTimeout applies the request timeout if ctx has no deadline.
func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok || c.cfg.requestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.cfg.requestTimeout)
}

// waiter is woken up by the first message for which match returns true.
// match and fire are called with c.mu held.
type waiter struct {
	match func(glow.Message) bool
	fire  func()
}

// wakeWaiters fires and removes all waiters matching msg. Callers must hold
// c.mu.
func (c *Client) wakeWaiters(msg glow.Message) {
	c.waiters = slices.DeleteFunc(c.waiters, func(w *waiter) bool {
		if w.match(msg) {
			w.fire()
			return true
		}
		return false
	})
}

func (c *Client) removeWaiter(w *waiter) {
	for i, x := range c.waiters {
		if x == w {
			c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
			return
		}
	}
}
