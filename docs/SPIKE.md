# 実機での前提検証（M0 スパイク）

開発プラン（docs/PLAN.md）M0 の S1〜S6 の手順と結果の記録。ミニ PC（Debian 13）で実行する。

| # | 内容 | 結果 |
|---|---|---|
| S1 | D-Bus の `NetworkRequest(path, "SIM", "UMTS-AUTH:…")` | **合格**（2026-10-02、E2E で確認） |
| S2 | Debian 13 の wpa_supplicant が AKA / AKA' に対応 | **合格**（2026-10-02） |
| S3 | mbim-proxy 経由の疎通、非 root の拒否 | **合格**（2026-10-02） |
| S4 | ドングルの `AKA` / UICC Low-Level Access の対応状況 | **合格**: 両方の経路に対応（2026-10-02。docs/COMPAT.md） |
| S5 | UUID 定数と Proxy Control の形式 | 完了（libmbim ソース。DESIGN §16.4） |
| S6 | 外部 SIM 応答 `UMTS-FAIL` で Authentication-Reject | 完了（hostap ソース。DESIGN §16.3）。E2E の wrong-key でも確認 |

## 検証環境

| 項目 | 内容 |
|---|---|
| 本体 | Intel Alder Lake-N のミニ PC（amd64）、有線 LAN（`enp1s0`）で管理 |
| OS | Debian 13.7、カーネル 6.12.111+deb13-amd64 |
| 無線 LAN | Intel CNVi（`iwlwifi`）、インターフェース名 `wlo1`。NetworkManager は未導入 |
| パッケージ | modemmanager 1.24.0-1+deb13u1、libmbim 1.32.0-1、wpasupplicant 2:2.10-24、hostapd 2:2.10-24 |
| モデム | Soracom Onyx（Quectel EG25-G、FW EG25GGBR07A08M2G） |

## 事前準備

```bash
sudo apt install modemmanager libmbim-utils wpasupplicant hostapd iw rfkill
```

WSL2 側でビルドして転送する（ミニ PC は amd64）。

```bash
make build build-e2e && scp bin/simwifi bin/simwifi-e2e bin/hlrgw simwifi:simwifi/
```

`iw` などは `/usr/sbin` にあり、一般ユーザーの PATH に無い点に注意。

## S2: wpa_supplicant の EAP メソッド

wpa_supplicant の D-Bus は root 以外を拒否するので `sudo` が必要。

```bash
sudo busctl get-property fi.w1.wpa_supplicant1 /fi/w1/wpa_supplicant1 fi.w1.wpa_supplicant1 EapMethods
```

結果: `AKA` と `AKA'` を含む 22 メソッド。ルートの `Capabilities` は `ap ibss-rsn p2p interworking pmf mesh ft sha384 owe suiteb192`。Debian の `wpa_supplicant.service` は `-u -s -O "DIR=/run/wpa_supplicant GROUP=netdev"` で起動している。

## S3: mbim-proxy

```bash
sudo mbimcli -p -d /dev/cdc-wdm0 --query-device-caps
./simwifi status --iface wlo1    # 非 root
```

結果:
- root: `DEVICE_CAPS` 成功（Device type `remote`、Cellular class `gsm`、SIM class `removable`、Max sessions 8）
- 非 root: mbim-proxy は接続直後に切断する。simwifi は 10 ms 未満で `mbim-proxy closed the connection immediately` と報告した。mbimcli は自前の再試行のため、タイムアウトまで待ち続ける

## S4: ドングルの能力

```bash
sudo mbimcli -p -d /dev/cdc-wdm0 --ms-query-uicc-application-list
sudo ./simwifi probe -v
```

結果:
- mbimcli の `--ms-query-uicc-application-list` は `NoDeviceSupport`
- `simwifi probe`: MBIM AKA は対応（ダミー AUTN に `AUTH_INCORRECT_AUTN` を返す）。UICC Low-Level Access も対応（ATR 取得、部分 AID `A0000000871002` で `OPEN_CHANNEL` / `CLOSE_CHANNEL` 成功）
- 実 SIM での AUTHENTICATE（RES / CK / IK の取得）は、AP を使う実機テストで確認する

## E2E（SIM なし）

```bash
sudo SIMWIFI=$PWD/simwifi-e2e HLRGW=$PWD/hlrgw PATH=$PATH:/usr/sbin ./test/e2e/run.sh
```

結果（2026-10-02）: 6 シナリオすべて合格（aka、akap、resync、wrong-key、after-crash、wpa3）。

- 実物の wpa_supplicant 2.10 が D-Bus で `NetworkRequest("SIM")` を出し、`NetworkReply` で返した値で EAP-AKA / AKA' が成功した（S1）
- Debian の hostapd 2.10 は EAP-AKA / AKA' のサーバー機能と `eap_sim_db` を持ち、hlrgw に認証ベクタを要求した
- 初回は hwsim の監視用インターフェース `hwsim0` を AP に選んで失敗した。`phy80211` を持つインターフェースだけを選ぶよう `run.sh` を修正済み
