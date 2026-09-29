// Package mbimtest はテスト用の fake mbim-proxy を提供する。
// 抽象 unix ソケットで待ち受け、Proxy 設定 → OPEN → COMMAND の流れを模擬する。
package mbimtest

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"sync"
	"testing"
	"uuid"

	"github.com/oyaguma3/simwifi/internal/mbim"
)

// Request はハンドラに渡すコマンド。
type Request struct {
	Type   mbim.CommandType
	Buffer []byte
}

// Response はハンドラの応答。
type Response struct {
	Status mbim.Status
	Buffer []byte
	Drop   bool // true なら応答しない（タイムアウトの再現）
}

// Handler はコマンドを処理する。
type Handler func(Request) Response

type key struct {
	service uuid.UUID
	cid     uint32
}

// Options は fake proxy の動作設定。
type Options struct {
	// RejectAll は接続直後に切断する（非 root の拒否を再現）。
	RejectAll bool
	// FragmentSize が正なら、応答をこの長さ以下のフラグメントに分割して送る。
	FragmentSize int
	// Indications なら、各応答の前に IndicateStatus を送る。
	Indications bool
}

// Server は fake mbim-proxy。
type Server struct {
	Addr string
	opts Options

	ln       net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	handlers map[key]Handler
	counts   map[key]int
	device   string
	conns    []net.Conn
}

// NewServer はランダムな抽象ソケット名で fake proxy を起動する。テスト終了時に閉じる。
func NewServer(t testing.TB, opts Options) *Server {
	t.Helper()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	addr := "@simwifi-mbimtest-" + hex.EncodeToString(b)
	ln, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatalf("mbimtest: listen: %v", err)
	}
	s := &Server{
		Addr:     addr,
		opts:     opts,
		ln:       ln,
		handlers: make(map[key]Handler),
		counts:   make(map[key]int),
	}
	s.wg.Go(s.accept)
	t.Cleanup(s.Close)
	return s
}

// Handle はサービス / CID のハンドラを登録する。未登録の CID は NO_DEVICE_SUPPORT を返す。
func (s *Server) Handle(service uuid.UUID, cid uint32, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[key{service, cid}] = h
}

// Count はそのサービス / CID を受け取った回数を返す。
func (s *Server) Count(service uuid.UUID, cid uint32) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[key{service, cid}]
}

// DevicePath は Proxy 設定で渡されたデバイスパスを返す。
func (s *Server) DevicePath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.device
}

// Close は待ち受けと全接続を閉じる。
func (s *Server) Close() {
	s.ln.Close()
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		if s.opts.RejectAll {
			conn.Close()
			continue
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		s.wg.Go(func() { s.serve(conn) })
	}
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	configured, opened := false, false
	for {
		raw, err := mbim.ReadRaw(conn)
		if err != nil {
			return
		}
		m, err := mbim.ParseMessage(raw)
		if err != nil {
			return
		}
		var resp *mbim.Message
		switch m.Type {
		case mbim.TypeOpen:
			opened = true
			resp = &mbim.Message{Type: mbim.TypeOpenDone, TxID: m.TxID}
		case mbim.TypeClose:
			opened = false
			resp = &mbim.Message{Type: mbim.TypeCloseDone, TxID: m.TxID}
		case mbim.TypeCommand:
			if m.Service == mbim.ServiceProxyControl && m.CID == mbim.CIDProxyControlConfiguration {
				d := mbim.NewDecoder(m.Buffer)
				dev := d.String()
				_ = d.U32()
				s.mu.Lock()
				s.device = dev
				s.mu.Unlock()
				configured = d.Err() == nil && dev != ""
				st := mbim.StatusSuccess
				if !configured {
					st = mbim.StatusInvalidParameters
				}
				resp = &mbim.Message{Type: mbim.TypeCommandDone, TxID: m.TxID, Service: m.Service, CID: m.CID, Status: uint32(st)}
				break
			}
			if !configured || !opened {
				resp = &mbim.Message{Type: mbim.TypeFunctionError, TxID: m.TxID, Status: 5} // not-opened
				break
			}
			r := s.dispatch(m)
			if r.Drop {
				continue
			}
			resp = &mbim.Message{Type: mbim.TypeCommandDone, TxID: m.TxID, Service: m.Service, CID: m.CID,
				Status: uint32(r.Status), Buffer: r.Buffer}
		default:
			continue
		}
		if err := s.send(conn, resp); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(m *mbim.Message) Response {
	k := key{m.Service, m.CID}
	s.mu.Lock()
	s.counts[k]++
	h := s.handlers[k]
	s.mu.Unlock()
	if h == nil {
		return Response{Status: mbim.StatusNoDeviceSupport}
	}
	return h(Request{Type: m.CommandType, Buffer: m.Buffer})
}

func (s *Server) send(conn net.Conn, m *mbim.Message) error {
	var errs []error
	if s.opts.Indications {
		ind := &mbim.Message{Type: mbim.TypeIndicateStatus, Service: mbim.ServiceProxyControl,
			CID: mbim.CIDProxyControlVersion, Buffer: []byte{0x00, 0x02, 0x00, 0x03}}
		_, err := conn.Write(ind.Marshal())
		errs = append(errs, err)
	}
	for _, f := range mbim.Fragment(m.Marshal(), s.opts.FragmentSize) {
		_, err := conn.Write(f)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
