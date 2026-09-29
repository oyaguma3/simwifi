package mbim

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"uuid"

	"github.com/oyaguma3/simwifi/internal/logging"
)

const (
	// DefaultProxyAddr は mbim-proxy の抽象 unix ソケット名。
	DefaultProxyAddr = "@mbim-proxy"
	// DefaultTimeout は 1 コマンドの応答待ちの既定値（proxy 内部の 300 秒より十分短く）。
	DefaultTimeout = 10 * time.Second
	// defaultDeviceOpenTimeout は proxy がデバイスを開くときの待ち時間（秒）。
	defaultDeviceOpenTimeout = 30
	maxControlTransfer       = 4096
)

// ErrTimeout はコマンドの応答が時間内に来なかったことを示す。
var ErrTimeout = errors.New("mbim command timed out")

// Options は Dial の設定。
type Options struct {
	ProxyAddr         string        // 既定は DefaultProxyAddr
	DevicePath        string        // /dev/cdc-wdmN（必須）
	Timeout           time.Duration // 1 コマンドの応答待ち。既定は DefaultTimeout
	DeviceOpenTimeout uint32        // proxy に渡すデバイスオープンの待ち時間（秒）
	Logger            *slog.Logger
}

// Client は mbim-proxy 経由の MBIM クライアント。複数の goroutine から使える。
type Client struct {
	conn    net.Conn
	log     *slog.Logger
	timeout time.Duration

	txid    atomic.Uint32
	writeMu sync.Mutex
	gotAny  atomic.Bool // 1 通でも受信したか（拒否判定用）

	mu      sync.Mutex
	pending map[uint32]chan *Message
	err     error // 受信ループが止まった理由
	done    chan struct{}
}

// Dial は mbim-proxy に接続し、デバイスを指定して OPEN する。
func Dial(ctx context.Context, opts Options) (*Client, error) {
	if opts.DevicePath == "" {
		return nil, errors.New("mbim: device path is required")
	}
	addr := cmp.Or(opts.ProxyAddr, DefaultProxyAddr)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to mbim-proxy (%s): %w", addr, err)
	}
	c := newClient(conn, opts)
	go c.readLoop()

	cfg := Encode(String(opts.DevicePath), U32(cmp.Or(opts.DeviceOpenTimeout, defaultDeviceOpenTimeout)))
	if _, err := c.Command(ctx, ServiceProxyControl, CIDProxyControlConfiguration, Set, cfg); err != nil {
		c.Close()
		return nil, fmt.Errorf("configure mbim-proxy for %s: %w", opts.DevicePath, err)
	}
	resp, err := c.request(ctx, &Message{Type: TypeOpen, MaxControlTransfer: maxControlTransfer})
	if err == nil && resp.Type != TypeOpenDone {
		err = unexpected(resp)
	}
	if err == nil && resp.Status != 0 {
		err = fmt.Errorf("mbim open: %s", Status(resp.Status))
	}
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("open %s: %w", opts.DevicePath, err)
	}
	c.log.Debug("mbim device opened", "device", opts.DevicePath)
	return c, nil
}

func newClient(conn net.Conn, opts Options) *Client {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Client{
		conn:    conn,
		log:     logger.With("component", "mbim"),
		timeout: cmp.Or(opts.Timeout, DefaultTimeout),
		pending: make(map[uint32]chan *Message),
		done:    make(chan struct{}),
	}
}

// Close は接続を閉じる。CLOSE_MSG は送らない（proxy が処理する）。
func (c *Client) Close() error {
	c.mu.Lock()
	if c.err == nil {
		c.err = ErrClosed
	}
	c.mu.Unlock()
	return c.conn.Close()
}

// Query は Query コマンドを送る。
func (c *Client) Query(ctx context.Context, service uuid.UUID, cid uint32, buf []byte) ([]byte, error) {
	return c.Command(ctx, service, cid, Query, buf)
}

// SetCommand は Set コマンドを送る。
func (c *Client) SetCommand(ctx context.Context, service uuid.UUID, cid uint32, buf []byte) ([]byte, error) {
	return c.Command(ctx, service, cid, Set, buf)
}

// Command はコマンドを送り、COMMAND_DONE の情報バッファを返す。
// Status が成功でない場合は *StatusError を返すが、情報バッファも一緒に返す
// （AUTH_SYNC_FAILURE の AUTS や、UICC の SW など、失敗時にも意味のあるデータがあるため）。
func (c *Client) Command(ctx context.Context, service uuid.UUID, cid uint32, typ CommandType, buf []byte) ([]byte, error) {
	resp, err := c.request(ctx, &Message{Type: TypeCommand, Service: service, CID: cid, CommandType: typ, Buffer: buf})
	if err != nil {
		return nil, fmt.Errorf("mbim %s cid %d: %w", ServiceName(service), cid, err)
	}
	switch {
	case resp.Type == TypeFunctionError:
		return nil, fmt.Errorf("mbim %s cid %d: %w", ServiceName(service), cid, &FunctionError{Code: ProtocolError(resp.Status)})
	case resp.Type != TypeCommandDone:
		return nil, unexpected(resp)
	case resp.Service != service || resp.CID != cid:
		return nil, fmt.Errorf("mbim: response for %s cid %d does not match request %s cid %d",
			ServiceName(resp.Service), resp.CID, ServiceName(service), cid)
	case resp.Status != 0:
		return resp.Buffer, &StatusError{Service: service, CID: cid, Status: Status(resp.Status)}
	}
	return resp.Buffer, nil
}

func unexpected(m *Message) error {
	return fmt.Errorf("mbim: unexpected %s (txid %d)", m.Type, m.TxID)
}

// request はメッセージを送り、同じ TransactionId の応答を待つ。
func (c *Client) request(ctx context.Context, m *Message) (*Message, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, c.timeout, ErrTimeout)
	defer cancel()

	m.TxID = c.nextTxID()
	ch := make(chan *Message, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	c.pending[m.TxID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, m.TxID)
		c.mu.Unlock()
	}()

	c.trace(ctx, "mbim send", m)
	c.writeMu.Lock()
	_, err := c.conn.Write(m.Marshal())
	c.writeMu.Unlock()
	if err != nil {
		if !c.gotAny.Load() && isDisconnect(err) {
			return nil, ErrProxyRejected
		}
		return nil, fmt.Errorf("write: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return nil, c.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (c *Client) nextTxID() uint32 {
	for {
		if id := c.txid.Add(1); id != 0 {
			return id
		}
	}
}

func (c *Client) readLoop() {
	var r reassembler
	for {
		raw, err := ReadRaw(c.conn)
		if err != nil {
			if !c.gotAny.Load() && isDisconnect(err) {
				err = ErrProxyRejected
			}
			c.stop(err)
			return
		}
		c.gotAny.Store(true)
		full, err := r.add(raw)
		if err != nil {
			c.log.Warn("dropping malformed mbim fragment", "error", err)
			continue
		}
		if full == nil {
			continue
		}
		m, err := ParseMessage(full)
		if err != nil {
			c.log.Warn("dropping malformed mbim message", "error", err)
			continue
		}
		c.trace(context.Background(), "mbim recv", m)
		switch m.Type {
		case TypeIndicateStatus:
			// proxy の MBIMEx バージョン通知などは読み捨てる
		case TypeOpenDone, TypeCloseDone, TypeCommandDone, TypeFunctionError:
			c.deliver(m)
		default:
			c.log.Debug("ignoring mbim message", "type", m.Type.String(), "txid", m.TxID)
		}
	}
}

func (c *Client) deliver(m *Message) {
	c.mu.Lock()
	ch := c.pending[m.TxID]
	delete(c.pending, m.TxID)
	c.mu.Unlock()
	if ch == nil {
		c.log.Debug("mbim response without pending request", "type", m.Type.String(), "txid", m.TxID)
		return
	}
	ch <- m
}

func (c *Client) stop(err error) {
	c.mu.Lock()
	if c.err == nil { // 自分で Close した場合は ErrClosed のまま
		c.err = err
	}
	c.mu.Unlock()
	close(c.done)
}

func isDisconnect(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

// trace は送受信メッセージを Trace レベルで記録する。鍵素材を含みうる情報バッファは長さだけ出す。
func (c *Client) trace(ctx context.Context, msg string, m *Message) {
	if !c.log.Enabled(ctx, logging.LevelTrace) {
		return
	}
	attrs := []slog.Attr{slog.String("type", m.Type.String()), slog.Uint64("txid", uint64(m.TxID))}
	switch m.Type {
	case TypeCommand, TypeCommandDone, TypeIndicateStatus:
		attrs = append(attrs, slog.String("service", ServiceName(m.Service)), slog.Uint64("cid", uint64(m.CID)))
		switch m.Type {
		case TypeCommand:
			attrs = append(attrs, slog.String("command_type", m.CommandType.String()))
		case TypeCommandDone:
			attrs = append(attrs, slog.String("status", Status(m.Status).String()))
		}
		if sensitive(m.Service, m.CID) {
			attrs = append(attrs, slog.Any("buffer", logging.Secret(m.Buffer)))
		} else {
			attrs = append(attrs, slog.Any("buffer", logging.Hex(m.Buffer)))
		}
	case TypeOpenDone, TypeCloseDone:
		attrs = append(attrs, slog.String("status", Status(m.Status).String()))
	case TypeFunctionError:
		attrs = append(attrs, slog.String("error", (&FunctionError{Code: ProtocolError(m.Status)}).Error()))
	}
	c.log.LogAttrs(ctx, logging.LevelTrace, msg, attrs...)
}
