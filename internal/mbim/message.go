package mbim

import (
	"errors"
	"fmt"
	"io"
	"uuid"
)

// MessageType は MBIM メッセージの種別（MBIM 1.0 §9.1）。
type MessageType uint32

const (
	TypeOpen           MessageType = 0x00000001
	TypeClose          MessageType = 0x00000002
	TypeCommand        MessageType = 0x00000003
	TypeHostError      MessageType = 0x00000004
	TypeOpenDone       MessageType = 0x80000001
	TypeCloseDone      MessageType = 0x80000002
	TypeCommandDone    MessageType = 0x80000003
	TypeFunctionError  MessageType = 0x80000004
	TypeIndicateStatus MessageType = 0x80000007
)

func (t MessageType) String() string {
	switch t {
	case TypeOpen:
		return "open"
	case TypeClose:
		return "close"
	case TypeCommand:
		return "command"
	case TypeHostError:
		return "host-error"
	case TypeOpenDone:
		return "open-done"
	case TypeCloseDone:
		return "close-done"
	case TypeCommandDone:
		return "command-done"
	case TypeFunctionError:
		return "function-error"
	case TypeIndicateStatus:
		return "indicate-status"
	}
	return fmt.Sprintf("type(0x%08x)", uint32(t))
}

// fragmented はフラグメントヘッダを持つ種別かを返す。
func (t MessageType) fragmented() bool {
	return t == TypeCommand || t == TypeCommandDone || t == TypeIndicateStatus
}

const (
	headerLen     = 12      // MessageType, MessageLength, TransactionId
	fragHeaderLen = 8       // TotalFragments, CurrentFragment
	maxMessageLen = 1 << 20 // 受信するメッセージ長の上限（proxy は再構成済みのメッセージを送る）
)

// Message は再構成済みの MBIM メッセージ。種別ごとに使うフィールドが異なる。
type Message struct {
	Type MessageType
	TxID uint32

	// Open
	MaxControlTransfer uint32

	// Command / CommandDone / IndicateStatus
	Service     uuid.UUID
	CID         uint32
	CommandType CommandType // Command
	Status      uint32      // OpenDone / CloseDone / CommandDone の Status、FunctionError / HostError の ErrorStatusCode
	Buffer      []byte
}

// Marshal はメッセージを 1 フラグメントのワイヤ形式にする。
func (m *Message) Marshal() []byte {
	b := make([]byte, headerLen, headerLen+fragHeaderLen+36+len(m.Buffer))
	switch m.Type {
	case TypeOpen:
		b = le.AppendUint32(b, m.MaxControlTransfer)
	case TypeOpenDone, TypeCloseDone, TypeFunctionError, TypeHostError:
		b = le.AppendUint32(b, m.Status)
	case TypeCommand, TypeCommandDone, TypeIndicateStatus:
		b = le.AppendUint32(b, 1) // TotalFragments
		b = le.AppendUint32(b, 0) // CurrentFragment
		b = append(b, m.Service[:]...)
		b = le.AppendUint32(b, m.CID)
		switch m.Type {
		case TypeCommand:
			b = le.AppendUint32(b, uint32(m.CommandType))
		case TypeCommandDone:
			b = le.AppendUint32(b, m.Status)
		}
		b = le.AppendUint32(b, uint32(len(m.Buffer)))
		b = append(b, m.Buffer...)
	}
	le.PutUint32(b[0:], uint32(m.Type))
	le.PutUint32(b[4:], uint32(len(b)))
	le.PutUint32(b[8:], m.TxID)
	return b
}

// ParseMessage は再構成済み（1 フラグメント）のメッセージを解析する。
func ParseMessage(b []byte) (*Message, error) {
	if len(b) < headerLen {
		return nil, fmt.Errorf("mbim: message too short (%d)", len(b))
	}
	m := &Message{
		Type: MessageType(le.Uint32(b[0:])),
		TxID: le.Uint32(b[8:]),
	}
	if int(le.Uint32(b[4:])) != len(b) {
		return nil, fmt.Errorf("mbim: length mismatch (header %d, actual %d)", le.Uint32(b[4:]), len(b))
	}
	body := b[headerLen:]
	need := func(n int) error {
		if len(body) < n {
			return fmt.Errorf("mbim: %s message too short (%d)", m.Type, len(b))
		}
		return nil
	}
	switch m.Type {
	case TypeOpen:
		if err := need(4); err != nil {
			return nil, err
		}
		m.MaxControlTransfer = le.Uint32(body)
	case TypeOpenDone, TypeCloseDone, TypeFunctionError, TypeHostError:
		if err := need(4); err != nil {
			return nil, err
		}
		m.Status = le.Uint32(body)
	case TypeClose:
	case TypeCommand, TypeCommandDone, TypeIndicateStatus:
		fixed := fragHeaderLen + 16 + 4 + 4
		if m.Type != TypeIndicateStatus {
			fixed += 4
		}
		if err := need(fixed); err != nil {
			return nil, err
		}
		if total := le.Uint32(body); total != 1 {
			return nil, fmt.Errorf("mbim: unassembled fragment (%d total)", total)
		}
		p := body[fragHeaderLen:]
		m.Service = uuid.UUID(p[0:16])
		m.CID = le.Uint32(p[16:])
		p = p[20:]
		switch m.Type {
		case TypeCommand:
			m.CommandType = CommandType(le.Uint32(p))
			p = p[4:]
		case TypeCommandDone:
			m.Status = le.Uint32(p)
			p = p[4:]
		}
		n := le.Uint32(p)
		p = p[4:]
		if uint64(n) > uint64(len(p)) {
			return nil, fmt.Errorf("mbim: information buffer length %d exceeds message (%d)", n, len(p))
		}
		m.Buffer = p[:n]
	default:
		return nil, fmt.Errorf("mbim: unknown message type %s", m.Type)
	}
	return m, nil
}

// ReadRaw はストリームから 1 つのメッセージ（フラグメント）を読む。
func ReadRaw(r io.Reader) ([]byte, error) {
	hdr := make([]byte, headerLen)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	n := le.Uint32(hdr[4:])
	if n < headerLen || n > maxMessageLen {
		return nil, fmt.Errorf("mbim: invalid message length %d", n)
	}
	b := make([]byte, n)
	copy(b, hdr)
	if _, err := io.ReadFull(r, b[headerLen:]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return b, nil
}

// Fragment は 1 フラグメントのメッセージを、各フラグメントが maxLen 以下になるよう分割する。
// フラグメントを持たない種別や、分割不要な場合はそのまま返す。
func Fragment(msg []byte, maxLen int) [][]byte {
	t := MessageType(le.Uint32(msg[0:]))
	if !t.fragmented() || len(msg) <= maxLen || maxLen <= headerLen+fragHeaderLen {
		return [][]byte{msg}
	}
	payload := msg[headerLen+fragHeaderLen:]
	chunk := maxLen - headerLen - fragHeaderLen
	total := (len(payload) + chunk - 1) / chunk
	out := make([][]byte, 0, total)
	for i := range total {
		part := payload[i*chunk : min((i+1)*chunk, len(payload))]
		f := make([]byte, headerLen+fragHeaderLen, headerLen+fragHeaderLen+len(part))
		copy(f, msg[:headerLen])
		le.PutUint32(f[4:], uint32(headerLen+fragHeaderLen+len(part)))
		le.PutUint32(f[12:], uint32(total))
		le.PutUint32(f[16:], uint32(i))
		out = append(out, append(f, part...))
	}
	return out
}

// reassembler は受信フラグメントを (種別, TxID) ごとに再構成する。
type reassembler struct {
	partial map[fragKey]*fragState
}

type fragKey struct {
	typ  MessageType
	txid uint32
}

type fragState struct {
	total, next uint32
	header      []byte
	payload     []byte
}

// add はフラグメントを追加する。メッセージが完成したら 1 フラグメント形式で返す。未完成なら nil。
func (r *reassembler) add(raw []byte) ([]byte, error) {
	t := MessageType(le.Uint32(raw[0:]))
	if !t.fragmented() {
		return raw, nil
	}
	if len(raw) < headerLen+fragHeaderLen {
		return nil, fmt.Errorf("mbim: %s fragment too short (%d)", t, len(raw))
	}
	total, cur := le.Uint32(raw[12:]), le.Uint32(raw[16:])
	if total == 1 && cur == 0 {
		return raw, nil
	}
	key := fragKey{t, le.Uint32(raw[8:])}
	if r.partial == nil {
		r.partial = make(map[fragKey]*fragState)
	}
	st := r.partial[key]
	switch {
	case total == 0 || cur >= total:
		delete(r.partial, key)
		return nil, fmt.Errorf("mbim: invalid fragment %d/%d", cur, total)
	case cur == 0:
		st = &fragState{total: total, next: 1, header: raw[:headerLen+fragHeaderLen]}
		st.payload = append([]byte(nil), raw[headerLen+fragHeaderLen:]...)
		r.partial[key] = st
		return nil, nil
	case st == nil || st.total != total || st.next != cur:
		delete(r.partial, key)
		return nil, fmt.Errorf("mbim: fragment out of sequence (%d/%d, txid %d)", cur, total, key.txid)
	}
	st.payload = append(st.payload, raw[headerLen+fragHeaderLen:]...)
	st.next++
	if len(st.payload) > maxMessageLen {
		delete(r.partial, key)
		return nil, fmt.Errorf("mbim: reassembled message too long")
	}
	if st.next < st.total {
		return nil, nil
	}
	delete(r.partial, key)
	full := make([]byte, headerLen+fragHeaderLen, headerLen+fragHeaderLen+len(st.payload))
	copy(full, st.header)
	le.PutUint32(full[4:], uint32(headerLen+fragHeaderLen+len(st.payload)))
	le.PutUint32(full[12:], 1)
	le.PutUint32(full[16:], 0)
	return append(full, st.payload...), nil
}
