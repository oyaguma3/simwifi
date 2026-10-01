# 実機互換表

`simwifi probe` と実機テストの結果を記録する（DESIGN §12.3）。

| 製品 | モジュール | FW | MBIM AKA | UICC Low-Level Access | APPLICATION_LIST | MAC 不正時の応答 | 実 SIM での接続 | 確認日 |
|---|---|---|---|---|---|---|---|---|
| Soracom Onyx | Quectel EG25-G | EG25GGBR07A08M2G | **応答はするが結果が不正**（正しい AUTN も `AUTH_INCORRECT_AUTN` で拒否） | **対応**（実 SIM で RES / CK / IK を取得） | 非対応 | Status `AUTH_INCORRECT_AUTN` (35) | **成功**（自動経路で UICC に切り替え。EAP-AKA / AKA'、再認証、再同期） | 2026-10-02 |

## 機種ごとのメモ

### Quectel EG25-G（Soracom Onyx）

- ModemManager の plugin は `quectel`、ドライバは `cdc_mbim` + `option`。MBIM ポートは `cdc-wdm0`
- SIM スロットは 1 つ（MM の `PrimarySimSlot` / `SimSlots` は空）
- ATR: `3b9f96801fc78031e073fe2117574a3382033339001e`
- **MBIM AKA（Auth サービス AKA CID）は使えない。** PoC サーバーの正しいチャレンジに対しても `AUTH_INCORRECT_AUTN` を返す。同じチャレンジを UICC Low-Level Access の APDU で USIM に直接送ると受理され、wpa_supplicant はサーバーの AT_MAC の検証に合格した（＝ SIM とサーバーの鍵は一致している）。既定の自動経路（`--auth-path auto`）は、MBIM AKA が AUTN を拒否すると UICC で確かめ直して切り替えるので、指定なしで動く（2026-10-02 に対応）。`--auth-path uicc` で固定してもよい
- `probe` はダミー AUTN への応答しか見ないため、この不具合を検出できない（ダミーには正しく `AUTH_INCORRECT_AUTN` を返すので「対応」と判定される）
- MBIM AKA は同期失敗（SQN ずれ）のチャレンジにも `AUTH_INCORRECT_AUTN` を返し、AUTS を返さない。UICC 経路では応答タグ `DC` で AUTS が返る

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


