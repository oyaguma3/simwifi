# 実機互換表

`simwifi probe` と実機テストの結果を記録する（DESIGN §12.3）。

| 製品 | モジュール | FW | MBIM AKA | UICC Low-Level Access | APPLICATION_LIST | MAC 不正時の応答 | 実 SIM での接続 | 確認日 |
|---|---|---|---|---|---|---|---|---|
| Soracom Onyx | Quectel EG25-G | EG25GGBR07A08M2G | **対応（値を逆順で扱う）** | **対応**（実 SIM で RES / CK / IK を取得） | 非対応 | Status `AUTH_INCORRECT_AUTN` (35) | **成功**（MBIM AKA で EAP-AKA / AKA'。UICC 経路でも再認証・再同期まで確認） | 2026-10-02 |
| M.2 SIM スロット付き USB アダプタ（2 スロット） | Sierra Wireless EM7455 | SWI9X30C_02.24.03.00 | **対応（値を逆順で扱う）** | **非対応**（`NO_DEVICE_SUPPORT`） | 非対応 | Status `AUTH_INCORRECT_AUTN` (35) | **成功**（MBIM AKA、EAP-AKA / AKA'） | 2026-10-02 |

## 機種ごとのメモ

### Quectel EG25-G（Soracom Onyx）

- ModemManager の plugin は `quectel`、ドライバは `cdc_mbim` + `option`。MBIM ポートは `cdc-wdm0`
- SIM スロットは 1 つ（MM の `PrimarySimSlot` / `SimSlots` は空）
- ATR: `3b9f96801fc78031e073fe2117574a3382033339001e`
- **MBIM AKA は値を逆順（128 ビットのリトルエンディアン）で扱う。** 3GPP の並びで送ると正しいチャレンジも拒否される。バイト順の自動判別を入れた修正版で、MBIM AKA による EAP-AKA / AKA' の接続に成功した（2026-10-02。UICC 経路への切り替えは起きなかった）。 PoC サーバーの正しいチャレンジに対しても `AUTH_INCORRECT_AUTN` を返す。同じチャレンジを UICC Low-Level Access の APDU で USIM に直接送ると受理され、wpa_supplicant はサーバーの AT_MAC の検証に合格した（＝ SIM とサーバーの鍵は一致している）。（バイト順の修正前の記録。修正前は、既定の自動経路が MBIM AKA の拒否を UICC で確かめ直して切り替えることで動いていた）
- `probe` はダミー AUTN への応答しか見ないため、この不具合を検出できない（ダミーには正しく `AUTH_INCORRECT_AUTN` を返すので「対応」と判定される）
- MBIM AKA は同期失敗（SQN ずれ）のチャレンジにも `AUTH_INCORRECT_AUTN` を返し、AUTS を返さない。UICC 経路では応答タグ `DC` で AUTS が返る

### Sierra Wireless EM7455（M.2 SIM スロット付き USB アダプタ）

- ModemManager の plugin は `sierra`、ドライバは `cdc_mbim`。MBIM ポートは `cdc-wdm0`。MBIM の中で QMI も使える（`MBIM device is QMI capable`）
- ModemManager は `unlock-required: sim-pin2` を報告する。PIN2 は利用を妨げないので、simwifi もロックとはみなさない（2026-10-02 に修正）
- UICC Low-Level Access は非対応なので、USIM への経路は MBIM AKA だけ
- MBIM 標準のスロット操作（MS Basic Connect Extensions の slot mappings / slot info）は非対応。ModemManager は QMI でスロットを切り替える
- ModemManager はスロットを 2 つと報告するが、アダプタの 2 つ目のスロットの SIM は見えない（QMI の slot status でも物理スロット 2 のカード状態は `unknown`）
- 当初、MBIM AKA は PoC サーバーの正しいチャレンジを `AUTH_INCORRECT_AUTN` で拒否した。AMF を `8000`（分離ビット ON）から `0000` に変えても同じだったので、分離ビットが原因ではない（原因は下記のバイト順）
- 同じチャレンジ（AMF `0000`）を `qmicli --uim-send-apdu`（QMI over MBIM、論理チャネル）で USIM に直接送ると、SW `61 35`（53 バイトの応答あり＝成功応答 `DB` + RES / CK / IK / Kc の長さ）が返った。SIM はチャレンジを受理している
- QMI の論理チャネル経由の APDU では、`61 xx` の GET RESPONSE をモデムが自動では行わない
- **Windows では、同じ EM7455 と SIM で EAP-AKA が通る**（AMF `0000`、ユーザー確認）。USBPcap で記録した Windows の AKA 要求は、simwifi が同じ RAND / AUTN で作る要求とバイト単位で同一。Windows の応答は Status `SUCCESS`、RES 長 8
- Windows でも、AKA の時点のモデムはソフトウェア無線オフ・網に未登録。Windows は AKA の直前に `DEVICE_SERVICE_SUBSCRIBE_LIST` で Auth サービス（AKA_AUTH、SIM_AUTH）を購読している
- Linux で切り分けた結果（いずれも、MBIM AKA に拒否されてまだ有効なチャレンジを使用）:
  - Auth サービスを購読した状態で送る → `AUTH_INCORRECT_AUTN`（購読は無関係）
  - ModemManager を止め、mbim-proxy がデバイスを開き直した直後に送る → `NOT_INITIALIZED`
  - ModemManager を止め、SIM の準備完了（`initialized`）を待って送る → `AUTH_INCORRECT_AUTN`（ModemManager の同時操作は無関係）
  - ModemManager と mbim-proxy を止め、`/dev/cdc-wdm0` を直接開いて（`OPEN` → SIM の準備完了を確認 → AKA）送る → `AUTH_INCORRECT_AUTN`（mbim-proxy は無関係）
- ~~結論: 原因は特定できていない~~ → **原因が判明した（2026-10-02）。MBIM AKA は値を 128 ビットのリトルエンディアン整数として扱う。** Windows の記録の AUTN は、AMF の位置に `d94f` があり、`0000` が 2 バイト後ろにあった。これは AUTN の 16 バイトを丸ごと逆順にした形と一致する（サーバーの AUTN の組み立ては標準どおりであることをソースで確認）。拒否されたチャレンジの RAND / AUTN を逆順にして送ると、MBIM AKA は成功した
- simwifi を修正し（逆順 → 標準の順で試し、判明した順を覚える。応答の RES / CK / IK / AUTS も逆順として読む）、**EM7455 の MBIM AKA で EAP-AKA と EAP-AKA' の接続に成功した**（AMF `8000`）。wpa_supplicant がサーバーの AT_MAC の検証に合格し、サーバーも RES を受け入れたので、応答の読み方も正しい
- Windows の初期化手順（`DEVICE_SERVICE_SUBSCRIBE_LIST`、DMS などの QMI による情報取得）は無関係だった。Windows でも `RADIO_STATE` の Set は失敗しており、無線オフ・未登録のまま AKA を行っている
- この型番（USB ID `1199:9079`）は Lenovo 向けで FCC ロックがかかっている。ModemManager の解除スクリプト（`fcc-unlock.available.d/1199:9079`）は既定で無効。今回だけ `qmicli --dms-set-fcc-authentication` で解除したが、無線のオンは `Failure` / `InvalidTransition` で失敗した
- USIM アプリは 1 つ（AID `A0000000871002FFFFFFFF8903020000`）。モデムの Primary GW セッションもこのアプリ
- **スロット 2 への切り替えで回復不能に近い状態になった**（2026-10-02）:
  1. simwifi が `SetPrimarySimSlot(2)` を要求 → モデムは作り直されたが `failed`（`sim-missing`）。QMI では物理スロット 1 がアクティブのまま、カードは `power-down` のエラー状態
  2. ModemManager は「今はスロット 1」と認識しているため、`mmcli --set-primary-sim-slot=1` は何もしない
  3. `mmcli --reset` は成功を返したが再起動せず、QMI にも応答しなくなった
  4. アダプタを USB から抜き挿しして復旧（スロット 1 のカードで正常に戻った）

## 実機テストの記録

### 2026-10-02: SSID `SIMAUTH`（WPA2-Enterprise、5180 MHz、PMF なし）、PoC サーバー

| 試行 | 経路 | SIM の応答 | 端末での AT_MAC 検証 | 結果 |
|---|---|---|---|---|
| EAP-AKA × 3 | MBIM AKA | `AUTH_INCORRECT_AUTN` | — | 終了コード 3（Authentication-Reject） |
| EAP-AKA × 2 | UICC APDU | RES 8 バイト、CK / IK | 合格（2 回目で wpa_supplicant のデバッグログを取得して確認） | サーバーから EAP-Failure |
| EAP-AKA' × 1 | UICC APDU | RES 8 バイト、CK / IK | 未確認（デバッグログなし） | サーバーから EAP-Failure |

- NAI は `0` / `6` + IMSI + `@wlan.mnc009.mcc440.3gppnetwork.org`。サーバーは NAI を受け付けてチャレンジを送ってきた
- チャレンジの属性は AT_RAND / AT_AUTN / AT_MAC のみ（AT_RESULT_IND なし）。AUTN の AMF は `8000`
- 端末が AT_RES（64 ビット）と AT_MAC を返した約 70 ms 後に EAP-Failure。原因は PoC サーバーの認可ルールの登録ミスと判明（ユーザー確認、2026-10-02）

### 2026-10-02: 同上（PoC サーバーの認可ルール修正後）

| 試行 | 経路 | 結果 |
|---|---|---|
| EAP-AKA | UICC APDU | **接続成功**。5180 MHz、80MHz 幅、VHT。SIGINT で切断・後始末して終了コード 0 |
| EAP-AKA' | UICC APDU | **接続成功** |
| EAP-AKA'、接続中に再認証（`wpa_cli reauthenticate`） | UICC APDU | **成功**。サーバーは高速再認証を使わず、通常の認証をやり直した（2 回目の SIM 要求に応答） |
| EAP-AKA、再認証 × 2（フック付き） | UICC APDU | **成功**。再認証の途中の `4way_handshake` を切断と誤判定する不具合を見つけて修正し、修正後はフックが接続時と終了時の 1 回ずつだけ動くことを確認 |

- 鍵交換後に wpa_supplicant の状態が一瞬 `4way_handshake` になる（約 60 ms）
- wpa_supplicant 2.10 の D-Bus API には `Reauthenticate` メソッドが無い。再認証は制御ソケット（`wpa_cli reauthenticate`）で起こした

### 2026-10-02: 同上（自動経路の改良後、PoC サーバー側で SQN をずらした状態）

`--auth-path` を指定せずに EAP-AKA で接続し、**成功**（終了コード 0）。

1. 1 回目のチャレンジ: MBIM AKA は `AUTH_INCORRECT_AUTN`（情報バッファなし）
2. 同じチャレンジを UICC 経路で確かめ直すと、USIM は同期失敗（`DC` + AUTS）を返した。自動経路は UICC に切り替え、AUTS をサーバーへ返した
3. サーバーが再同期して新しいチャレンジを送り、UICC 経路で受理されて接続した


