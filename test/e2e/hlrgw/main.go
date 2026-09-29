// Command hlrgw は hostapd の eap_sim_db に認証ベクタを返す、hlr_auc_gw の最小限の代替（E2E 専用）。
// EAP-AKA / AKA' の AKA-REQ-AUTH と AKA-AUTS だけを扱う。Milenage DB は hlr_auc_gw と同じ書式:
//
//	# IMSI Ki OPc AMF SQN
//	555444333222111 5122250214c33e723a5dd523fc145fc0 981d464c7c52eb6e5036234984ad0bcf c3ab 16f3b3f70fc1
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/oyaguma3/simwifi/internal/simauth/milenage"
)

// indLen は SQN の IND 部のビット数（hlr_auc_gw の既定と同じ）。
const indLen = 5

type subscriber struct {
	m   *milenage.Milenage
	amf [2]byte
	sqn uint64
}

// hlr は加入者 DB。
type hlr struct {
	mu   sync.Mutex
	subs map[string]*subscriber
}

func loadDB(path string) (*hlr, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := &hlr{subs: make(map[string]*subscriber)}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) != 5 {
			return nil, fmt.Errorf("%s:%d: want 5 fields", path, n)
		}
		k, err1 := hex.DecodeString(fs[1])
		opc, err2 := hex.DecodeString(fs[2])
		amf, err3 := hex.DecodeString(fs[3])
		sqn, err4 := strconv.ParseUint(fs[4], 16, 48)
		if err := errors.Join(err1, err2, err3, err4); err != nil || len(amf) != 2 {
			return nil, fmt.Errorf("%s:%d: bad value: %v", path, n, err)
		}
		m, err := milenage.New(k, opc)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		h.subs[fs[0]] = &subscriber{m: m, amf: [2]byte(amf), sqn: sqn}
	}
	return h, sc.Err()
}

// incSQN は SEQ と IND をそれぞれ 1 進める（hlr_auc_gw の inc_sqn と同じ）。
func incSQN(v uint64) uint64 {
	seq := (v >> indLen) + 1
	ind := (v + 1) & (1<<indLen - 1)
	return (seq<<indLen | ind) & milenage.MaxSQN
}

// handle は 1 つの要求を処理し、応答（無ければ ""）を返す。
func (h *hlr) handle(req string) string {
	fs := strings.Fields(req)
	if len(fs) == 0 {
		return ""
	}
	switch fs[0] {
	case "AKA-REQ-AUTH":
		if len(fs) != 2 {
			return ""
		}
		imsi := fs[1]
		h.mu.Lock()
		defer h.mu.Unlock()
		s, ok := h.subs[imsi]
		if !ok {
			log.Printf("AKA-REQ-AUTH: unknown IMSI %s", imsi)
			return "AKA-RESP-AUTH " + imsi + " FAILURE"
		}
		s.sqn = incSQN(s.sqn)
		rnd := make([]byte, 16)
		_, _ = rand.Read(rnd)
		autn, v := s.m.GenerateAUTN(rnd, s.sqn, s.amf)
		log.Printf("AKA-REQ-AUTH: IMSI=%s SQN=%012x", imsi, s.sqn)
		return fmt.Sprintf("AKA-RESP-AUTH %s %x %x %x %x %x", imsi, rnd, autn, v.IK, v.CK, v.RES)
	case "AKA-AUTS":
		if len(fs) != 4 {
			return ""
		}
		imsi := fs[1]
		auts, err1 := hex.DecodeString(fs[2])
		rnd, err2 := hex.DecodeString(fs[3])
		h.mu.Lock()
		defer h.mu.Unlock()
		s, ok := h.subs[imsi]
		if !ok || err1 != nil || err2 != nil {
			log.Printf("AKA-AUTS: bad request for %s", imsi)
			return ""
		}
		sqnMS, err := s.m.ResyncSQN(rnd, auts)
		if err != nil {
			log.Printf("AKA-AUTS: IMSI=%s: %v", imsi, err)
			return ""
		}
		s.sqn = sqnMS
		log.Printf("AKA-AUTS: IMSI=%s re-synchronized SQN=%012x", imsi, sqnMS)
		return ""
	case "SIM-REQ-AUTH":
		if len(fs) >= 2 {
			return "SIM-RESP-AUTH " + fs[1] + " FAILURE"
		}
	}
	return ""
}

func main() {
	sock := flag.String("socket", "/tmp/hlr_auc_gw.sock", "UNIX datagram socket path (hostapd eap_sim_db=unix:PATH)")
	db := flag.String("db", "", "Milenage database file (IMSI Ki OPc AMF SQN)")
	flag.Parse()
	if *db == "" {
		log.Fatal("-db is required")
	}
	h, err := loadDB(*db)
	if err != nil {
		log.Fatal(err)
	}
	_ = os.Remove(*sock)
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: *sock, Net: "unixgram"})
	if err != nil {
		log.Fatal(err)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		conn.Close()
		os.Remove(*sock)
		os.Exit(0)
	}()
	log.Printf("hlrgw: %d subscribers, listening on %s", len(h.subs), *sock)
	if err := serve(conn, h); err != nil {
		log.Fatal(err)
	}
}

func serve(conn *net.UnixConn, h *hlr) error {
	buf := make([]byte, 1000)
	for {
		n, from, err := conn.ReadFromUnix(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		resp := h.handle(string(buf[:n]))
		if resp == "" || from == nil {
			continue
		}
		if _, err := conn.WriteToUnix([]byte(resp), from); err != nil {
			log.Printf("reply: %v", err)
		}
	}
}
