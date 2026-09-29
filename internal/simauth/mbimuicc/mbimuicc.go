// Package mbimuicc は MS UICC Low-Level Access（論理チャネル上の APDU）で
// USIM の AUTHENTICATE を実行する（DESIGN §4.3 予備経路）。
package mbimuicc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/oyaguma3/simwifi/internal/logging"
	"github.com/oyaguma3/simwifi/internal/mbim"
	"github.com/oyaguma3/simwifi/internal/simauth"
)

// Commander は MBIM コマンドを送る口（*mbim.Client が実装する）。
type Commander interface {
	Command(ctx context.Context, service uuid.UUID, cid uint32, typ mbim.CommandType, buf []byte) ([]byte, error)
}

// PartialUSIMAID は USIM の AID の先頭（RID A000000087 + アプリケーションコード 1002）。
// ETSI TS 102 221 の SELECT by DF name は右側を切り詰めた AID を許す。
var PartialUSIMAID = []byte{0xa0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02}

const (
	// channelGroup は simwifi が開く論理チャネルのタグ。前回の残骸をまとめて閉じるのに使う。
	channelGroup uint32 = 0x53574946 // "SWIF"
	// selectP2 は SELECT の P2（FCP テンプレートを返す、最初または唯一の出現）。
	selectP2 uint32 = 0x04
	// classInterIndustry は MBIM_MS_UICC_CLASS_BYTE_TYPE の inter-industry（CLA 0X / 4X）。
	classInterIndustry uint32 = 0
	// applicationTypeUSIM は MbimUiccApplicationType の USIM。
	applicationTypeUSIM uint32 = 4
	// closeTimeout は後始末の CLOSE_CHANNEL の待ち時間。
	closeTimeout = 5 * time.Second
)

// Application は APPLICATION_LIST の 1 要素。
type Application struct {
	Type uint32
	AID  []byte
	Name string
}

// Authenticator は UICC Low-Level Access による simauth.Authenticator。
// USIM への要求は直列に処理する。
type Authenticator struct {
	c   Commander
	log *slog.Logger

	mu      sync.Mutex
	aid     []byte // 決定済みの USIM AID
	cleaned bool   // 前回の残骸チャネルを閉じたか
}

var _ simauth.Authenticator = (*Authenticator)(nil)

// New は Authenticator を作る。
func New(c Commander, logger *slog.Logger) *Authenticator {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Authenticator{c: c, log: logger.With("component", "simauth", "path", "mbim-uicc")}
}

// Name は経路名を返す。
func (a *Authenticator) Name() string { return "mbim-uicc" }

// Authenticate は USIM を論理チャネルで選択し、AUTHENTICATE（3G コンテキスト）を送る。
func (a *Authenticator) Authenticate(ctx context.Context, rand, autn []byte) (simauth.Result, error) {
	if err := simauth.CheckChallenge(rand, autn); err != nil {
		return simauth.Result{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.log.Debug("AUTHENTICATE request", "rand", logging.Hex(rand), "autn", logging.Hex(autn))

	if !a.cleaned {
		a.cleanup(ctx)
		a.cleaned = true
	}
	ch, err := a.openUSIM(ctx)
	if err != nil {
		return simauth.Result{}, err
	}
	defer a.closeChannel(ctx, ch)

	cmd := make([]byte, 0, 5+2+2*simauth.RANDLen+1)
	cmd = append(cmd, 0x00, 0x88, 0x00, 0x81, 0x22, 0x10)
	cmd = append(cmd, rand...)
	cmd = append(cmd, 0x10)
	cmd = append(cmd, autn...)
	cmd = append(cmd, 0x00) // Le
	resp, sw, err := a.transmit(ctx, ch, cmd)
	defer clear(resp)
	if err != nil {
		return simauth.Result{}, err
	}
	return parseAuthenticate(resp, sw)
}

// transmit は APDU を送り、応答データと SW（SW1<<8 | SW2）を返す。
// 61xx はモデムが処理する仕様だが、念のため GET RESPONSE を 1 回送る。
func (a *Authenticator) transmit(ctx context.Context, ch uint32, cmd []byte) ([]byte, uint16, error) {
	resp, sw, err := a.apdu(ctx, ch, cmd)
	if err == nil && sw>>8 == 0x61 {
		a.log.Debug("GET RESPONSE", "sw", fmt.Sprintf("%04x", sw))
		resp, sw, err = a.apdu(ctx, ch, []byte{0x00, 0xc0, 0x00, 0x00, byte(sw)})
	}
	return resp, sw, err
}

func (a *Authenticator) apdu(ctx context.Context, ch uint32, cmd []byte) ([]byte, uint16, error) {
	buf, err := a.c.Command(ctx, mbim.ServiceMSUICCLowLevelAccess, mbim.CIDUICCAPDU, mbim.Set,
		mbim.Encode(mbim.U32(ch), mbim.U32(0), mbim.U32(classInterIndustry), mbim.UICCRefBytes(cmd)))
	defer clear(buf)
	if err != nil {
		return nil, 0, unsupportedIf(err, mbim.StatusNoDeviceSupport)
	}
	d := mbim.NewDecoder(buf)
	sw := statusWord(d.U32())
	resp := d.UICCRefBytes()
	if err := d.Err(); err != nil {
		return nil, 0, fmt.Errorf("parse APDU response: %w", err)
	}
	return resp, sw, nil
}

// statusWord は MBIM の Status フィールド（BYTE[2]: SW1, SW2 の順）を SW1<<8 | SW2 にする。
func statusWord(v uint32) uint16 {
	return uint16(v&0xff)<<8 | uint16(v>>8&0xff)
}

func swOK(sw uint16) bool { return sw == 0x9000 || sw>>8 == 0x91 }

// parseAuthenticate は AUTHENTICATE の応答を解析する（TS 31.102 §7.1.2.1）。
func parseAuthenticate(resp []byte, sw uint16) (simauth.Result, error) {
	switch {
	case sw == 0x9862:
		return simauth.Result{}, fmt.Errorf("%w (SW 9862)", simauth.ErrAuthReject)
	case !swOK(sw):
		return simauth.Result{}, fmt.Errorf("USIM AUTHENTICATE failed: SW %04x", sw)
	case len(resp) == 0:
		return simauth.Result{}, errors.New("USIM AUTHENTICATE: empty response")
	}
	p := resp[1:]
	next := func() []byte {
		if len(p) < 1 || len(p) < 1+int(p[0]) {
			return nil
		}
		v := p[1 : 1+int(p[0])]
		p = p[1+int(p[0]):]
		return v
	}
	switch resp[0] {
	case 0xdb: // 成功: RES, CK, IK[, Kc]
		res, ck, ik := next(), next(), next()
		if len(res) < simauth.MinRES || len(res) > simauth.MaxRES || len(ck) != simauth.KeyLen || len(ik) != simauth.KeyLen {
			return simauth.Result{}, errors.New("USIM AUTHENTICATE: malformed success response")
		}
		r := simauth.Result{RES: slices.Clone(res)}
		copy(r.CK[:], ck)
		copy(r.IK[:], ik)
		return r, nil
	case 0xdc: // 同期失敗: AUTS
		auts := next()
		if len(auts) != simauth.AUTSLen {
			return simauth.Result{}, errors.New("USIM AUTHENTICATE: malformed AUTS response")
		}
		return simauth.Result{AUTS: slices.Clone(auts)}, simauth.ErrResync
	}
	return simauth.Result{}, fmt.Errorf("USIM AUTHENTICATE: unknown response tag %02x", resp[0])
}

// usimAID は USIM の AID を決める。APPLICATION_LIST が使えればそこから、無理なら部分 AID。
func (a *Authenticator) usimAID(ctx context.Context) []byte {
	if a.aid != nil {
		return a.aid
	}
	a.aid = PartialUSIMAID
	apps, err := ApplicationList(ctx, a.c)
	if err != nil {
		a.log.Debug("APPLICATION_LIST unavailable; using partial USIM AID", "error", err)
		return a.aid
	}
	for _, app := range apps {
		if app.Type == applicationTypeUSIM && bytes.HasPrefix(app.AID, PartialUSIMAID) {
			a.aid = app.AID
			a.log.Debug("USIM AID from APPLICATION_LIST", "aid", logging.Hex(app.AID))
			break
		}
	}
	return a.aid
}

// openUSIM は USIM を選択した論理チャネルを開く。
func (a *Authenticator) openUSIM(ctx context.Context) (uint32, error) {
	aid := a.usimAID(ctx)
	ch, err := OpenChannel(ctx, a.c, aid)
	if err != nil && !bytes.Equal(aid, PartialUSIMAID) {
		if st, _ := mbim.StatusOf(err); st == mbim.StatusMSSelectFailed {
			// 完全 AID で失敗したら部分 AID で再試行する
			a.log.Debug("SELECT with full AID failed; retrying with partial AID", "error", err)
			a.aid = PartialUSIMAID
			ch, err = OpenChannel(ctx, a.c, PartialUSIMAID)
		}
	}
	return ch, err
}

// closeChannel は論理チャネルを閉じる。呼び出し元の ctx が切れていても閉じる。
func (a *Authenticator) closeChannel(ctx context.Context, ch uint32) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	if err := CloseChannel(ctx, a.c, ch, 0); err != nil {
		a.log.Warn("failed to close UICC logical channel", "channel", ch, "error", err)
	}
}

// cleanup は前回のプロセスが残したチャネルを ChannelGroup 指定でまとめて閉じる。
func (a *Authenticator) cleanup(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	if err := CloseChannel(ctx, a.c, 0, channelGroup); err != nil {
		a.log.Debug("closing stale UICC channels failed", "error", err)
	}
}

// unsupportedIf は Status が statuses のいずれかなら ErrUnsupported で包む。
func unsupportedIf(err error, statuses ...mbim.Status) error {
	if st, ok := mbim.StatusOf(err); ok && slices.Contains(statuses, st) {
		return fmt.Errorf("%w: %w", simauth.ErrUnsupported, err)
	}
	return err
}

// ATR は UICC の ATR を取得する（probe 用）。
func ATR(ctx context.Context, c Commander) ([]byte, error) {
	buf, err := c.Command(ctx, mbim.ServiceMSUICCLowLevelAccess, mbim.CIDUICCATR, mbim.Query, nil)
	if err != nil {
		return nil, unsupportedIf(err, mbim.StatusNoDeviceSupport)
	}
	d := mbim.NewDecoder(buf)
	atr := d.UICCRefBytes()
	return atr, d.Err()
}

// ApplicationList は UICC のアプリケーション一覧を取得する（MBIMEx 拡張。非対応のモデムもある）。
func ApplicationList(ctx context.Context, c Commander) ([]Application, error) {
	buf, err := c.Command(ctx, mbim.ServiceMSUICCLowLevelAccess, mbim.CIDUICCApplicationList, mbim.Query, nil)
	if err != nil {
		return nil, unsupportedIf(err, mbim.StatusNoDeviceSupport)
	}
	d := mbim.NewDecoder(buf)
	_ = d.U32() // Version
	count := d.U32()
	_ = d.U32() // ActiveApplicationIndex
	_ = d.U32() // ApplicationListSizeBytes
	structs := d.RefStructs(count)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("parse APPLICATION_LIST: %w", err)
	}
	apps := make([]Application, 0, len(structs))
	for _, s := range structs {
		app := Application{Type: s.U32(), AID: s.RefBytes(), Name: s.StringUTF8()}
		if err := s.Err(); err != nil {
			return nil, fmt.Errorf("parse APPLICATION_LIST entry: %w", err)
		}
		apps = append(apps, app)
	}
	return apps, nil
}

// OpenChannel は aid を選択した論理チャネルを開き、チャネル番号を返す。
// 論理チャネルが無い、非対応などで経路自体が使えない場合は ErrUnsupported で包む。
func OpenChannel(ctx context.Context, c Commander, aid []byte) (uint32, error) {
	buf, err := c.Command(ctx, mbim.ServiceMSUICCLowLevelAccess, mbim.CIDUICCOpenChannel, mbim.Set,
		mbim.Encode(mbim.UICCRefBytes(aid), mbim.U32(selectP2), mbim.U32(channelGroup)))
	d := mbim.NewDecoder(buf)
	sw := statusWord(d.U32())
	ch := d.U32()
	if err != nil {
		if len(buf) >= 4 {
			err = fmt.Errorf("%w (SW %04x)", err, sw)
		}
		return 0, unsupportedIf(err, mbim.StatusNoDeviceSupport, mbim.StatusMSNoLogicalChannels, mbim.StatusMSSelectFailed)
	}
	if d.Err() != nil || ch == 0 {
		return 0, fmt.Errorf("OPEN_CHANNEL: invalid response (channel %d, SW %04x)", ch, sw)
	}
	return ch, nil
}

// CloseChannel は論理チャネルを閉じる。ch が 0 なら group のチャネルをすべて閉じる。
func CloseChannel(ctx context.Context, c Commander, ch, group uint32) error {
	_, err := c.Command(ctx, mbim.ServiceMSUICCLowLevelAccess, mbim.CIDUICCCloseChannel, mbim.Set,
		mbim.Encode(mbim.U32(ch), mbim.U32(group)))
	return err
}

// CheckChannel は USIM を開いてすぐ閉じる（probe 用。AUTHENTICATE は送らない）。
func CheckChannel(ctx context.Context, c Commander, aid []byte) error {
	ch, err := OpenChannel(ctx, c, aid)
	if err != nil {
		return err
	}
	return CloseChannel(ctx, c, ch, 0)
}
