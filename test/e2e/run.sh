#!/bin/bash
# simwifi の E2E テスト（SIM・ドングル・実 AP 不要）
#
#   mac80211_hwsim の 2 つの無線で AP（hostapd + hlrgw）と STA（simwifi-e2e）を作り、
#   EAP-AKA / AKA' の接続シナリオを順に実行する。root で実行すること。
#
#   使い方: sudo SIMWIFI=/path/to/simwifi-e2e HLRGW=/path/to/hlrgw ./run.sh
#   （既定はリポジトリの bin/。WSL2 で `make build-e2e` して scp する）
set -uo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
SIMWIFI=${SIMWIFI:-$HERE/../../bin/simwifi-e2e}
HLRGW=${HLRGW:-$HERE/../../bin/hlrgw}
IMSI=555444333222111
K=5122250214c33e723a5dd523fc145fc0
OPC=981d464c7c52eb6e5036234984ad0bcf

WORK=$(mktemp -d /tmp/simwifi-e2e.XXXXXX)
LOADED_HWSIM=0
HLR_PID=
AP_PID=
PASS=()
FAIL=()

log() { echo "[e2e] $*" >&2; }

cleanup() {
	[ -n "$AP_PID" ] && kill "$AP_PID" 2>/dev/null
	[ -n "$HLR_PID" ] && kill "$HLR_PID" 2>/dev/null
	pkill -f "simwifi-e2e connect" 2>/dev/null
	sleep 1
	[ "$LOADED_HWSIM" = 1 ] && rmmod mac80211_hwsim 2>/dev/null
	log "logs: $WORK"
}
trap cleanup EXIT

[ "$(id -u)" = 0 ] || { log "run as root"; exit 2; }
for c in hostapd iw modprobe; do
	command -v "$c" >/dev/null || { log "$c not found"; exit 2; }
done
[ -x "$SIMWIFI" ] || { log "simwifi-e2e not found: $SIMWIFI"; exit 2; }
[ -x "$HLRGW" ] || { log "hlrgw not found: $HLRGW"; exit 2; }
"$SIMWIFI" version | grep -q "e2e build" || { log "$SIMWIFI is not an e2e build (make build-e2e)"; exit 2; }

# --- 無線の準備 -------------------------------------------------------------
if lsmod | grep -q '^mac80211_hwsim'; then
	log "mac80211_hwsim is already loaded; unload it first (rmmod mac80211_hwsim)"
	exit 2
fi
modprobe mac80211_hwsim radios=2 || exit 2
LOADED_HWSIM=1
sleep 2
IFACES=()
for d in /sys/class/net/*; do
	# hwsim0（監視用の radiotap インターフェース）は無線 LAN ではないので除く
	if [ -e "$d/phy80211" ] && readlink -f "$d/device" 2>/dev/null | grep -q hwsim; then
		IFACES+=("$(basename "$d")")
	fi
done
[ "${#IFACES[@]}" -ge 2 ] || { log "hwsim interfaces not found"; exit 2; }
AP_IF=${IFACES[0]}
STA_IF=${IFACES[1]}
log "AP=$AP_IF STA=$STA_IF"
if command -v nmcli >/dev/null && nmcli -t general status >/dev/null 2>&1; then
	nmcli device set "$AP_IF" managed no
	nmcli device set "$STA_IF" managed no
fi
rfkill unblock wifi 2>/dev/null

# --- AP ---------------------------------------------------------------------
"$HLRGW" -socket "$WORK/hlr.sock" -db "$HERE/milenage.db" >"$WORK/hlrgw.log" 2>&1 &
HLR_PID=$!

start_ap() { # start_ap KEY_MGMT PMF
	sed -e "s|@IFACE@|$AP_IF|" -e "s|@WORK@|$WORK|g" -e "s|@HERE@|$HERE|g" \
		-e "s|@KEYMGMT@|$1|" -e "s|@PMF@|$2|" "$HERE/hostapd.conf.in" >"$WORK/hostapd.conf"
	hostapd "$WORK/hostapd.conf" >"$WORK/hostapd-$1.log" 2>&1 &
	AP_PID=$!
	for _ in $(seq 20); do
		grep -q "AP-ENABLED" "$WORK/hostapd-$1.log" && return 0
		sleep 0.5
	done
	log "hostapd did not start (see $WORK/hostapd-$1.log)"
	exit 2
}

stop_ap() {
	kill "$AP_PID" 2>/dev/null
	wait "$AP_PID" 2>/dev/null
	AP_PID=
}

# --- シナリオ ---------------------------------------------------------------
# run_case NAME EXPECTED_EXIT [simwifi の追加引数...]
# 接続できたら --exec-up で simwifi 自身に SIGTERM を送り、終了コード 0 で終わらせる
run_case() {
	local name=$1 want=$2
	shift 2
	log "case $name (expect exit $want)"
	timeout 90 "$SIMWIFI" connect --iface "$STA_IF" --ssid simwifi-e2e --timeout 30 -v \
		--auth-backend milenage --milenage-imsi "$IMSI" --milenage-k "$K" --milenage-opc "$OPC" \
		--exec-up 'kill -TERM $PPID' "$@" >"$WORK/$name.out" 2>"$WORK/$name.log"
	local got=$?
	if [ "$got" = "$want" ]; then
		PASS+=("$name")
		log "  PASS"
	else
		FAIL+=("$name (exit $got, want $want)")
		log "  FAIL: exit $got (see $WORK/$name.log)"
	fi
}

start_ap WPA-EAP 0
run_case aka 0 --method aka
run_case akap 0 --method akap
# USIM 側の SQN が網より進んでいる → AUTS で再同期してから成功する
run_case resync 0 --method aka --milenage-sqn ffffffff
grep -q "synchronization failure" "$WORK/resync.log" || FAIL+=("resync (no AUTS was sent)")
# K が違う → MAC 不正で Authentication-Reject → 連続失敗で exit 3
run_case wrong-key 3 --method aka --milenage-k 00000000000000000000000000000000 --max-auth-failures 2

# simwifi が異常終了した後の残骸 Interface を回収できる
log "case crash-recovery"
"$SIMWIFI" connect --iface "$STA_IF" --ssid simwifi-e2e --timeout 30 -v \
	--auth-backend milenage --milenage-imsi "$IMSI" --milenage-k "$K" --milenage-opc "$OPC" \
	>"$WORK/crash.out" 2>"$WORK/crash.log" &
CRASH_PID=$!
for _ in $(seq 60); do
	grep -q 'msg=connected' "$WORK/crash.log" && break
	sleep 0.5
done
kill -9 "$CRASH_PID" 2>/dev/null
wait "$CRASH_PID" 2>/dev/null
run_case after-crash 0 --method aka
grep -q "stale wpa_supplicant interface" "$WORK/after-crash.log" || FAIL+=("after-crash (stale interface not detected)")
stop_ap

start_ap WPA-EAP-SHA256 2
run_case wpa3 0 --method aka --wpa3
stop_ap

# --- 結果 -------------------------------------------------------------------
echo
echo "PASS: ${#PASS[@]}  ${PASS[*]}"
if [ "${#FAIL[@]}" -gt 0 ]; then
	echo "FAIL: ${#FAIL[@]}"
	printf '  %s\n' "${FAIL[@]}"
	exit 1
fi
echo "all E2E cases passed"
