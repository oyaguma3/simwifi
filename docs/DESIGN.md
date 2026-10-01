# simwifi 設計ドキュメント

MBIM モードの USB 通信ドングルに挿した SIM を使い、Linux PC から EAP-AKA / EAP-AKA' で無線 LAN 認証を行う CLI ツール。

- リポジトリ: https://github.com/oyaguma3/simwifi
- ライセンス: MIT
- 言語: Go 1.27（`CGO_ENABLED=0`、外部依存は最小限）
- ステータス: PoC（本ドキュメントは PoC 実装の設計。将来拡張は §14 に分離）

改訂履歴:

| 版 | 日付 | 内容 |
|---|---|---|
| 1 | 2026-09-30 | 初版 |
| 2 | 2026-09-30 | 開発プランのレビュー指摘（14 件）を反映。wpa_supplicant / libmbim / ModemManager のソースと MS UICC Low-Level Access 仕様で事実確認（§16.3–16.6）。E2E 用 Milenage バックエンドを追加。`mbimuicc` を PoC 初版に含めることを確定 |

---

## 1. 目的とスコープ

### 1.1 目的

EAP-AKA の暗号処理と状態機械は wpa_supplicant が持っている。足りないのは、wpa_supplicant が USIM に投げたい AUTHENTICATE（RAND / AUTN）を MBIM ドングル経由で解いて返す部分だけである。simwifi はこの橋渡しに加え、接続に必要な前後処理（モデム検出、NAI 生成、wpa_supplicant への設定投入、状態表示）を 1 コマンドで提供する。

### 1.2 スコープ内

- EAP-AKA（RFC 4187）と EAP-AKA'（RFC 5448 / 9048）による WPA2 / WPA3-Enterprise 接続
- USIM へのアクセス経路
  - 主経路: MBIM（`MBIM_CID_AKA`）
  - 予備経路: MS UICC Low-Level Access（論理チャネル上の APDU）。PoC 初版に含める
- ModemManager（以下 MM）を使ったモデム検出、SIM 情報の取得、SIM スロットの切替
- SSID を直接指定した接続
- 接続前の状態確認（`status`）とモデム能力の確認（`probe`）
- 標準エラー出力とファイルへのログ出力

### 1.3 スコープ外（PoC では扱わない）

- EAP-SIM（GSM 認証）
- AKA' から AKA へのフォールバック
- IP アドレスの取得や経路設定。DHCP クライアント等は外部に任せ、`--exec-up` フックだけを提供する
- SIM PIN の解除。MM に任せ、PIN ロック中はエラーで終了する
- NetworkManager 管理下での動作（§14.1）
- Passpoint / ANQP による自動ネットワーク選択（§14.2）
- MM が動いていない環境（§14.4）
- 複数モデム・複数 wlan の同時制御。1 組ずつ扱い、複数ある場合は明示的な指定を求める
- 非 root での動作（§14.3）

---

## 2. 前提環境

| 項目 | 前提 | 備考 |
|---|---|---|
| Distro | Debian 13 (trixie) 以降 | Ubuntu 派生も想定するが、検証は Debian 13 で行う |
| ModemManager | 1.18 以降（SIM スロット API） | Debian 13 は 1.24 系 |
| libmbim / mbim-proxy | 1.32 以降 | `mbim-proxy` は root からの接続だけを許可する（§13.1） |
| wpa_supplicant | 2.10 以降。`CONFIG_EAP_AKA` / `CONFIG_EAP_AKA_PRIME` / `CONFIG_CTRL_IFACE_DBUS_NEW` が有効 | D-Bus で動いていること（Debian の `wpa_supplicant.service` は `-u` 付き）。EAP メソッドは `status` がルートオブジェクトの `EapMethods` で確認する |
| D-Bus | system bus | MM と wpa_supplicant はどちらも system bus を使う |
| 権限 | root | PoC では root で実行する前提（§13） |
| Wi-Fi | nl80211 ドライバ。NetworkManager では `unmanaged` | NM 管理下だと wpa_supplicant の Interface を取り合う |

`status` コマンドは上記の前提を 1 つずつ検査し、満たしていない項目を明示する（§6.2）。

---

## 3. 全体構成

```
                      ┌──────────────────────────────────────────┐
                      │ simwifi (Go, root)                        │
                      │                                          │
   D-Bus (system)     │  modem ──── mm client                    │   D-Bus (system)
 ┌───────────────┐    │                                          │  ┌──────────────────┐
 │ ModemManager  │◀───┤  simauth ── mbim client ─┐               ├─▶│ wpa_supplicant   │
 │  Modem/Sim    │    │                          │               │  │ (systemd, -u)    │
 └───────┬───────┘    │  supplicant ─────────────┼───────────────┤  │ iface: external_ │
         │            │                          │               │  │   sim=1          │
         │            │  cli / status / logging  │               │  └────────┬─────────┘
         │            └──────────────────────────┼───────────────┘           │ nl80211
         │ mbim-proxy                            │ unix @mbim-proxy          ▼
         ▼                                       ▼                        [wlan0] ── AP ── AAA
 ┌──────────────────────────────────────────────────┐
 │ mbim-proxy (libmbim, root)                         │
 └───────────────────────┬──────────────────────────┘
                         ▼ /dev/cdc-wdmN
                    [MBIM modem] ── USIM
```

### 3.1 責務分担

| 役割 | 担当 | simwifi が書く部分 |
|---|---|---|
| EAP-AKA / AKA' 本体、鍵導出、4-way handshake | wpa_supplicant | なし |
| USIM への AUTHENTICATE 要求の受け取り | wpa_supplicant → simwifi（D-Bus シグナル `NetworkRequest`） | 受信と応答 |
| AKA 演算（RES / CK / IK / AUTS） | USIM（モデム経由） | MBIM メッセージの組み立てと解釈 |
| モデムと SIM の検出、IMSI / MCC / MNC の取得、スロット切替 | MM | D-Bus クライアント |
| `/dev/cdc-wdmN` の排他制御 | mbim-proxy | プロキシ接続のクライアント |
| wpa_supplicant プロセスの起動・常駐 | systemd（`wpa_supplicant.service`）/ D-Bus activation | なし（Interface の作成と削除だけ行う） |
| 無線接続の状態遷移 | wpa_supplicant | ネットワークの投入と監視 |

### 3.2 主要データフロー（認証 1 回分）

```
AAA ─EAP-Request/AKA-Challenge(RAND,AUTN)─▶ wpa_supplicant
wpa_supplicant ─NetworkRequest("SIM","UMTS-AUTH:<rand>:<autn>")─▶ simwifi
simwifi ─MBIM COMMAND AKA(Rand,Autn)─▶ mbim-proxy ─▶ modem ─▶ USIM
USIM ─(RES,CK,IK | AUTS)─▶ modem ─▶ mbim-proxy ─COMMAND_DONE─▶ simwifi
simwifi ─NetworkReply("SIM","UMTS-AUTH:<ik>:<ck>:<res>" | "UMTS-AUTS:<auts>" | "UMTS-FAIL")─▶ wpa_supplicant
wpa_supplicant ─EAP-Response/AKA-Challenge (| Synchronization-Failure | Authentication-Reject)─▶ AAA
```

AKA' の場合、CK' / IK' の導出は wpa_supplicant が行う。サーバーが送る AT_KDF_INPUT を使う。simwifi は AKA と AKA' で処理を変えない。

---

## 4. パッケージ構成

```
simwifi/
├── main.go                    # エントリ。internal/cli へ委譲
├── internal/
│   ├── cli/                   # サブコマンド定義、flag 解析、終了コード、connect オーケストレータ
│   ├── modem/                 # ModemManager D-Bus クライアント
│   ├── mbim/                  # 最小 MBIM クライアント（mbim-proxy 経由）
│   │   └── mbimtest/          # テスト用 fake mbim-proxy
│   ├── simauth/               # AKA 認証の抽象と実装
│   │   ├── mbimaka/           # MBIM_CID_AKA
│   │   ├── mbimuicc/          # MS UICC Low-Level Access
│   │   └── milenage/          # ソフトウェア Milenage（テスト / E2E 用）
│   ├── supplicant/            # wpa_supplicant の抽象と D-Bus 実装
│   ├── nai/                   # IMSI → NAI 生成
│   ├── status/                # 前提条件の収集と判定（status / connect で共用）
│   ├── logging/               # slog 設定、ファンアウト、秘匿情報マスク
│   └── lock/                  # iface ごとのロックファイル
├── contrib/
│   ├── simwifi@.service       # systemd ユニット（テンプレート）
│   ├── simwifi.env.example
│   └── simwifi.logrotate
├── docs/
│   ├── DESIGN.md              # 本ドキュメント
│   ├── SPIKE.md               # 実機での前提検証メモ
│   └── COMPAT.md              # 実機互換表
├── test/e2e/                  # mac80211_hwsim + hostapd + hlr_auc_gw による E2E
└── testdata/
```

### 4.1 `internal/supplicant`

wpa_supplicant への操作を interface で抽象化する。PoC の実装は D-Bus の 1 種類だけだが、将来の NM 相乗り（§14.1）を見据えて interface の境界を切っておく。

```go
type Supplicant interface {
    // Interface の作成（または残骸の回収）。既存の他者の Interface は ErrInterfaceBusy
    Attach(ctx context.Context, iface string, opts AttachOptions) error
    AddNetwork(ctx context.Context, cfg NetworkConfig) (NetworkID, error)
    SelectNetwork(ctx context.Context, id NetworkID) error
    RemoveNetwork(ctx context.Context, id NetworkID) error
    Disconnect(ctx context.Context) error
    // SIM 要求を受け取り、応答を返す
    SIMRequests(ctx context.Context) (<-chan SIMRequest, error)
    ReplySIM(ctx context.Context, req SIMRequest, resp SIMResponse) error
    // 状態監視
    Events(ctx context.Context) (<-chan Event, error)       // State 変化、EAP 状態、切断理由、サービス消失
    Capabilities(ctx context.Context) (Capabilities, error) // EapMethods、KeyMgmt など
    // Attach で自分が作った Interface だけを削除する
    Close(ctx context.Context) error
}
```

D-Bus 実装（`wpadbus`）:

- サービス `fi.w1.wpa_supplicant1`、ルートオブジェクト `/fi/w1/wpa_supplicant1`
- ルートオブジェクトのプロパティ
  - `EapMethods`（`as`）: `AKA` / `AKA'` が含まれるかを確認する
  - `Capabilities`（`as`）: `interworking` など（情報表示のみ）
- `Attach` の手順（§7.3）
  1. `GetInterface(ifname)` を呼ぶ
  2. Interface が無ければ、設定ファイルを書いて `CreateInterface({"Ifname":..., "Driver":"nl80211", "ConfigFile":...})` を呼ぶ
  3. Interface があり、その `ConfigFile` プロパティが simwifi の設定ファイルと一致する場合は、前回の simwifi が残した残骸とみなす。`RemoveInterface` してから 2 に進む。ロックを取れている時点で、前回のプロセスは生きていない
  4. それ以外の既存 Interface は他者の所有とみなし、`ErrInterfaceBusy`（終了コード 2）を返す
- `fi.w1.wpa_supplicant1.Interface`
  - メソッド `AddNetwork(a{sv})` → `SelectNetwork(o)` / `RemoveNetwork(o)` / `Disconnect()`
  - シグナル `NetworkRequest(o path, s field, s text)`: `field == "SIM"` のものを処理する
  - メソッド `NetworkReply(o path, s field, s value)`
  - プロパティ `State`、`DisconnectReason`、`CurrentNetwork`（`PropertiesChanged` で監視）
  - シグナル `EAP(s status, s parameter)`: `started`、`completion` + `success` / `failure` など
  - プロパティ `Capabilities`（`a{sv}`）の `KeyMgmt`: `--wpa3` のとき `wpa-eap-sha256` の有無を確認する
- `org.freedesktop.DBus.NameOwnerChanged` で `fi.w1.wpa_supplicant1` の消失を検知する
- `external_sim=1` は wpa_supplicant のグローバル設定項目なので、**Interface ごとの設定ファイルで与える**（§7.3）。D-Bus には汎用の SET が無い。既存インスタンスに相乗りする場合は、制御ソケットの `SET external_sim 1` が必要になる（§14.1）

ネットワーク設定（`AddNetwork` の引数）:

| key | value | 備考 |
|---|---|---|
| `ssid` | `--ssid` | 文字列で渡す（wpa_supplicant が引用符を付ける） |
| `key_mgmt` | `WPA-EAP`。`--wpa3` 指定時は `WPA-EAP-SHA256` | |
| `ieee80211w` | `--wpa3` 指定時のみ `2`（PMF 必須） | |
| `eap` | `AKA` または `AKA'` | |
| `identity` | §8 で生成した NAI | |
| `phase1` | `result_ind=1` | 保護された結果通知。サーバーが非対応でも無害 |

### 4.2 `internal/mbim`

mbim-proxy 経由でモデムに MBIM メッセージを送る最小限のクライアント。実装するのは以下だけ。

- 接続: `net.Dial("unix", "@mbim-proxy")`。抽象名前空間で、ファイルパスではない。テストのためにアドレスは差し替えられるようにする
- 接続直後に `MBIM_COMMAND_MSG` で Proxy Control サービスの `CONFIGURATION` CID（Set）を送り、`DevicePath` と `Timeout`（秒）を渡す
  - 情報バッファ: `DevicePath` の offset(u32) / size(u32)、`Timeout`(u32)、その後に UTF-16LE の文字列データ（4 バイト境界までパディング）
- `MBIM_OPEN_MSG`（`MaxControlTransfer=4096`）→ `MBIM_OPEN_DONE`。libmbim の `mbim_device_open_full(PROXY)` と同じ順序
- `MBIM_COMMAND_MSG`（Query / Set）→ `MBIM_COMMAND_DONE`。TransactionId で要求と応答を対応付ける。受信ループの goroutine と、txid ごとの応答チャネルで多重化する
- 受信側のフラグメント再構成。送信は 1 フラグメントで足りる
- `MBIM_INDICATE_STATUS`（Proxy が送る MBIMEx バージョン通知を含む）は読み捨てる
- `MBIM_CLOSE_MSG` は送らない。proxy 側で自動処理されるので、切断するだけでよい

メッセージ定義（MBIM 1.0 §9 の一般形。すべてリトルエンディアン）:

```
Header      : MessageType u32 | MessageLength u32 | TransactionId u32
Fragment    : TotalFragments u32 | CurrentFragment u32
Command     : DeviceServiceId [16] | CID u32 | CommandType u32 | InformationBufferLength u32 | Buffer
CommandDone : DeviceServiceId [16] | CID u32 | Status u32 | InformationBufferLength u32 | Buffer
```

使用するサービスと CID（UUID は libmbim `mbim-uuid.c` と照合済み。§16.4）:

| サービス | UUID | CID | 種別 | 用途 |
|---|---|---|---|---|
| Proxy Control（libmbim 独自） | `838cf7fb-8d0d-4d7f-871e-d71dbefbb39b` | `CONFIGURATION` (1) | Set | デバイスの指定 |
| Basic Connect | `a289cc33-bcbb-8b4f-b6b0-133ec2aae6df` | `DEVICE_CAPS` (1) | Query | 疎通確認 |
| Auth | `1d2b5ff7-0aa1-48b2-aa52-50f15767174e` | `AKA` (1) | Query | AKA 認証（主経路） |
| MS UICC Low-Level Access | `c2f6588e-f037-4bc9-8665-f4d44bd09367` | `ATR` (1) | Query | 予備経路の可否確認 |
| 〃 | 〃 | `OPEN_CHANNEL` (2) | Set | USIM を論理チャネルで選択 |
| 〃 | 〃 | `CLOSE_CHANNEL` (3) | Set | 論理チャネルを閉じる |
| 〃 | 〃 | `APDU` (4) | Set | AUTHENTICATE の送信 |
| 〃 | 〃 | `APPLICATION_LIST` (7) | Query | USIM の完全な AID を取得（MBIMEx 拡張。非対応なら部分 AID を使う） |

エラーの扱い:

- 接続直後に EOF → mbim-proxy が権限で拒否した（root でない）と判定し、`ErrProxyRejected`（終了コード 2）
- `MBIM_FUNCTION_ERROR_MSG` → `FunctionError{Code}`
- `Status != 0` → `StatusError{Status}` として上位へ返す。主な Status は §16.5
- コマンドのタイムアウトは既定 10 秒。proxy 内部のタイムアウトは 300 秒なので、それより十分短くする
- Trace レベルで送受信メッセージを hexdump する。ただし AKA 応答と APDU 応答の情報バッファは鍵素材を含むため、長さだけを出す（§11）

### 4.3 `internal/simauth`

```go
type Authenticator interface {
    // RAND / AUTN（各 16B）→ RES, CK, IK
    //   同期失敗:        Result.AUTS をセットし ErrResync
    //   MAC 不正:        ErrAuthReject
    //   経路が非対応:    ErrUnsupported（auto 経路の切替判定に使う）
    Authenticate(ctx context.Context, rand, autn []byte) (Result, error)
    Name() string
}

type Result struct {
    RES  []byte   // 4..16 bytes
    CK   [16]byte
    IK   [16]byte
    AUTS []byte   // 14 bytes。同期失敗時のみ
}

// Clear は鍵素材をゼロクリアする（§13.1）
func (r *Result) Clear()
```

実装:

| 実装 | 経路 | 備考 |
|---|---|---|
| `mbimaka` | `MBIM_CID_AKA` | 主経路。Response: `Res[16]`, `ResLen u32`, `IK[16]`, `CK[16]`, `Auts[14]` |
| `mbimuicc` | MS UICC Low-Level Access | `AKA` 非対応モデム向けの予備経路。下記参照 |
| `milenage` | ソフトウェア実装（K / OPc / SQN を指定） | 単体テスト、fake proxy、E2E 用。stdlib の `crypto/aes` だけで完結する。リリースバイナリの connect からは選べない（§6.1 `--auth-backend`） |
| `auto` | `mbimaka` → `mbimuicc` | 既定。下記「経路の自動選択」参照 |

**`mbimaka` の Status 正規化**

| MBIM の応答 | 正規化後 |
|---|---|
| `SUCCESS`、`ResLen` が 4..16 | 成功 |
| `SUCCESS`、`ResLen == 0` かつ `Auts` が非ゼロ | `ErrResync` |
| `AUTH_SYNC_FAILURE` (36)。情報バッファに `Auts` があればそれを使う | `ErrResync`（`Auts` が無ければ `ErrAuthReject`） |
| `AUTH_INCORRECT_AUTN` (35) | `ErrAuthReject` |
| `AUTH_AMF_NOT_SET` (37) | `ErrAuthReject` |
| `NO_DEVICE_SUPPORT` (9) | `ErrUnsupported` |
| その他の Status、タイムアウト | そのままエラー（接続は「ローカルエラー」として扱う。§7.2） |

**`mbimuicc` の手順**

1. USIM の AID を決める
   - `APPLICATION_LIST` が使えれば、`ApplicationType == USIM (4)` の `ApplicationId` を使う
   - 使えなければ部分 AID `A0000000871002` を使う（ETSI TS 102 221 の SELECT by DF name は右側を切り詰めた AID を許す）
2. `OPEN_CHANNEL`（`AppId`、`SelectP2Arg = 0x04`、`ChannelGroup` は simwifi 固有の定数）→ `Channel` を得る。応答の Status が SW1 / SW2 を持つ（§16.4）
3. `APDU`（`Channel`、`SecureMessaging = 0`、`ClassByteType = 0`（inter-industry。TS 31.102 の AUTHENTICATE は CLA `0X` / `4X` のため））で AUTHENTICATE を送る
   - コマンド: `00 88 00 81 22 10‖RAND 10‖AUTN 00`。CLA はモデムが論理チャネルに合わせて書き換える
4. 応答を解析する
   - SW `90 00` / `91 xx` かつ応答の先頭が `DB` → `DB L(RES) RES L(CK) CK L(IK) IK [L(Kc) Kc]` を取り出す
   - `DC` → `DC 0E AUTS`（`ErrResync`）
   - SW `98 62`（MAC 不正）→ `ErrAuthReject`
   - それ以外の SW → エラー
   - `61 xx` の GET RESPONSE はモデムが処理する仕様だが、念のため `61 xx` を受けた場合は GET RESPONSE を 1 回送る
5. `CLOSE_CHANNEL`（`Channel` 指定）。エラー時も `defer` で必ず閉じる。起動時には前回の残骸に備えて `ChannelGroup` 指定の CLOSE も送る

**経路の自動選択（`auto`）**

- 直近の probe 結果（§6.3）があり、`AKA` 非対応と記録されていれば、最初から `mbimuicc` を使う
- それ以外は `mbimaka` を使う。最初の要求で `ErrUnsupported` が返ったら `mbimuicc` に切り替え、**同じ RAND / AUTN で再実行する**。以後はそのプロセス内で `mbimuicc` を使い続ける
- `mbimaka` が `ErrAuthReject` を返した場合も、**同じ RAND / AUTN を `mbimuicc` で確かめる**。`mbimuicc` が受理（または同期失敗）すれば `mbimaka` は信用できないとみなし、その結果を返して以後は `mbimuicc` に切り替える。`mbimuicc` も拒否した場合、または使えない場合は、拒否として扱う
  - 正しい AUTN も `AUTH_INCORRECT_AUTN` で拒否する MBIM AKA の実装がある（Quectel EG25-G。docs/COMPAT.md）。`probe` はダミー AUTN しか使わないので、この不具合を事前に検出できない
  - AUTN を拒否した USIM の状態（SQN）は変わらないので、確かめ直しは無害
- `--auth-path aka|uicc` を指定すれば経路を固定できる（実機検証用）

### 4.4 `internal/modem`

MM の D-Bus クライアント（`org.freedesktop.ModemManager1`）。

- `/org/freedesktop/ModemManager1` の `Version` プロパティ
- `ObjectManager.GetManagedObjects` でモデムを列挙する
- `Modem` のプロパティ
  - `Manufacturer`, `Model`, `Revision`, `EquipmentIdentifier`（IMEI など。probe 結果のキーに使う）
  - `State`（`i`。`LOCKED = 2`、`FAILED = -1` など）、`StateFailedReason`、`UnlockRequired`（`u`。`NONE` / `SIM_PIN2` / `SIM_PUK2` 以外はロック中。PIN2 / PUK2 は ModemManager と同じく利用を妨げないものとして扱う）
  - `Ports`（`a(su)`）: 種別 `MBIM (7)` のポート名に `/dev/` を付けて cdc-wdm のパスを得る。`PrimaryPort`
  - `Sim`、`SimSlots`（`ao`。空スロットは `/`）、`PrimarySimSlot`（`u`。1 始まり。0 はマルチスロット非対応）
- `Sim` のプロパティ: `Active`, `Imsi`, `SimIdentifier`（ICCID）, `OperatorIdentifier`（MCC+MNC。EF_AD の MNC 長を反映済み）, `OperatorName`
- `Modem.SetPrimarySimSlot(u)`（`--switch-slot` のときだけ）。呼び出し後はモデムオブジェクトが作り直されるので、`InterfacesAdded` を待って再取得する（既定 60 秒）
- `InterfacesRemoved` を監視し、connect 中のモデム抜去を検知する
- モデムの選択: 1 台ならそれを使う。複数なら `--modem <index|path>` を必須にする

### 4.5 `internal/nai`

§8 参照。純関数とテーブル駆動テストで作る。

### 4.6 `internal/status`

`status` と `connect` が同じ収集関数を使う。各チェックは `Check{Name, OK, Detail, Fatal}` を返し、`connect` は `Fatal` が 1 つでもあれば終了コード 2 で止まる。各チェックは依存（D-Bus クライアント、sysfs のルート、時計など）を注入できる関数として書き、テーブルテストで判定を検証する。

### 4.7 `internal/logging`

§11 参照。

### 4.8 `internal/lock`

`/run/simwifi/<iface>.lock` に `flock(LOCK_EX|LOCK_NB)` をかける。ディレクトリが無ければ 0700 で作る。ロックを取れなければ「already running」として終了コード 2。

---

## 5. 依存ライブラリ方針

- CLI: 標準 `flag`。サブコマンドは自前でディスパッチする
- D-Bus: `github.com/godbus/dbus/v5`（唯一の直接依存。純 Go。間接依存として `golang.org/x/sys` が入る）
- ログ: 標準 `log/slog`。複数の出力先は自前の小さなファンアウト Handler で扱う
- MBIM: 自前（バイナリフレーミングのみ）
- Milenage: 自前（`crypto/aes`）。3GPP TS 35.208 のテストベクタで検証する。外部実装（`github.com/wmnsk/milenage`）は採用を検討したが、実装量が小さく、テストベクタで正しさを担保できるため依存は増やさない
- テスト: 標準 `testing` のみ。D-Bus のテストはプライベートな `dbus-daemon` を起動して行い、`dbus-daemon` が無い環境では skip する

`go.mod` の module path は `github.com/oyaguma3/simwifi`。

---

## 6. CLI 仕様

### 6.1 サブコマンド

```
simwifi status   [--iface wlan0] [--modem N] [--json]
simwifi probe    [--modem N] [--json]
simwifi identity [--modem N] [--method aka|akap] [--realm REALM]
simwifi connect  --iface wlan0 --ssid SSID [options]
simwifi version
```

共通フラグ:

| フラグ | 既定 | 説明 |
|---|---|---|
| `--modem` | 自動（1 台のとき） | MM のモデム index またはオブジェクトパス |
| `--iface` | `wlan0` | 無線 LAN インターフェース |
| `--log-file PATH` | なし | ファイルにも JSON ログを出す |
| `-v` / `-vv` | Info | Debug / Trace（MBIM・D-Bus の生メッセージ）。標準 `flag` では `-vv` を別の bool フラグとして定義する |
| `--log-imsi` | false | IMSI をマスクせずにログへ出す |
| `--json` | false | 機械可読な出力（status / probe） |

`connect` 固有:

| フラグ | 既定 | 説明 |
|---|---|---|
| `--ssid` | 必須 | 接続先の SSID |
| `--method aka\|akap` | `aka` | EAP メソッド |
| `--realm REALM` | IMSI から自動生成 | NAI の realm 全体を上書きする |
| `--sim-slot N` | 現在のアクティブスロット | 認証に使うスロット。一致しなければ停止する |
| `--switch-slot` | false | `--sim-slot` が非アクティブなら切替を実行する（セルラーが切断され、数秒止まる） |
| `--auth-path auto\|aka\|uicc` | `auto` | USIM への経路（§4.3）。`aka` / `uicc` は実機検証用の固定指定 |
| `--wpa3` | false | `WPA-EAP-SHA256` + PMF 必須 |
| `--timeout SEC` | 60 | 接続完了までの上限 |
| `--max-auth-failures N` | 3 | 認証失敗がこの回数だけ連続したら終了する（§7.1） |
| `--exec-up CMD` | なし | 接続成功時に実行するコマンド（DHCP の起動など。§6.5） |
| `--exec-down CMD` | なし | 切断時に実行するコマンド |
| `--daemon` | false | 接続後にバックグラウンド化する（PoC では未実装。systemd の利用を推奨） |

E2E ビルド専用（ビルドタグ `e2e` のときだけ存在する。リリースバイナリには含めない）:

| フラグ | 説明 |
|---|---|
| `--auth-backend milenage` | USIM の代わりに `milenage` 実装で応答する |
| `--milenage-imsi` / `--milenage-k` / `--milenage-opc` / `--milenage-sqn` | Milenage のパラメータ（hex） |

`--auth-backend milenage` のとき、MM と mbim-proxy に関するチェック（§6.2）はスキップし、IMSI と MCC / MNC は `--milenage-imsi` と `--realm` から得る。

### 6.2 `status`

読み取り専用で、副作用は無い。表示項目と取得元:

| 項目 | 取得元 | Fatal 条件 |
|---|---|---|
| root か | `os.Geteuid()` | 非 root |
| MM の存在とバージョン | D-Bus `ModemManager1.Version` | MM が無い |
| モデム一覧、型番、FW、State | MM `Modem` | モデムが 0 台、複数台で `--modem` 未指定、`State == FAILED` |
| SIM スロット一覧、アクティブスロット、SIM 状態 | MM `Modem.SimSlots` / `PrimarySimSlot` / `Sim` | SIM が無い |
| PIN ロック | MM `Modem.UnlockRequired` / `State == LOCKED` | `UnlockRequired` が `NONE` / `SIM_PIN2` / `SIM_PUK2` 以外、または `State == LOCKED` |
| IMSI（マスク）、ICCID、MCC / MNC | MM `Sim` | IMSI を取得できない。`OperatorIdentifier` が空で `--realm` も無い |
| 生成される NAI | `nai` | — |
| mbim-proxy への接続可否 | 実際に接続し、`DEVICE_CAPS` を Query する | 接続できない、拒否された |
| wlan の存在とドライバ、NM の管理状態 | sysfs、NM の D-Bus（NM が居るときだけ） | iface が無い、NM が managed |
| wpa_supplicant の存在、`EapMethods` | wpa D-Bus（ルートオブジェクト） | サービスが無い（D-Bus activation も失敗）、`EapMethods` に `AKA` / `AKA'` が無い |
| iface の既存 Interface | wpa D-Bus `GetInterface` と `ConfigFile` | 他者の Interface が存在する（simwifi の残骸なら情報表示のみ） |
| Interworking / HS20 対応 | wpa D-Bus ルートの `Capabilities` | —（将来用。情報表示のみ） |
| 直近の probe 結果 | `/run/simwifi/probe-<EquipmentIdentifier>.json` | — |
| ロック状態 | `lock` | 別のインスタンスが実行中 |

出力は 1 行 1 項目で、先頭に `✔` / `✘`（情報表示は `·`）を付ける。`--json` で同じ内容を JSON 出力する。

### 6.3 `probe`

モデムに実際に MBIM を投げて能力を確認する。**AKA を 1 回実行する**（SIM の認証を 1 回使う）ため、`status` とは分けている。

1. mbim-proxy 経由でデバイスを開き、`DEVICE_CAPS` を Query する
2. `AKA` をダミー値（RAND: ランダム、AUTN: 全ゼロ）で Query する
   - `ErrAuthReject` / `ErrResync` → **CID は通る**と判定する。USIM は MAC 検証に失敗した時点で止まるので、SQN は進まない
   - `ErrUnsupported` → 非対応
   - どちらだったか（どの Status で返ったか）を記録し、§4.3 の正規化が働くかも確かめる
3. MS UICC Low-Level Access を確認する（`AKA` の結果にかかわらず行う）
   - `ATR` を Query
   - `APPLICATION_LIST` を Query（非対応でも可。その場合は部分 AID を使うと記録する）
   - `OPEN_CHANNEL`（USIM）→ `CLOSE_CHANNEL`。AUTHENTICATE は送らない
4. 結果を `/run/simwifi/probe-<EquipmentIdentifier>.json` に保存し、`status` と `connect`（経路の自動選択）が参照する。モデムの index は挿し直すと変わるため、キーには使わない

### 6.4 `identity`

IMSI から NAI を生成して表示するだけ。`--method` と `--realm` を受け付ける。

### 6.5 `connect`

§7 参照。前面で常駐し、Ctrl-C で切断と後始末をして終了する。

`--exec-up` / `--exec-down` の仕様:

- `/bin/sh -c CMD` で実行する（上限 30 秒。超えたら kill してログに記録）。フックは専用の goroutine で順番に実行し、イベントループ（SIM 要求への応答）は止めない。終了時は実行中・待ち中のフックの完了を待つ
- 環境変数 `SIMWIFI_IFACE`、`SIMWIFI_SSID`、`SIMWIFI_EVENT`（`up` / `down`）を渡す
- 失敗（非 0 終了、タイムアウト）はエラーログに残すが、simwifi 自体は続行する
- `--exec-up` は接続が `completed` になるたびに実行する。`--exec-down` は `completed` から外れたときと終了時に実行する（`up` を実行済みの場合のみ）

### 6.6 終了コード

| コード | 意味 |
|---|---|
| 0 | 成功（`connect` は Ctrl-C / SIGTERM による正常終了も 0） |
| 1 | 使い方の誤り、内部エラー、wpa_supplicant の消失 |
| 2 | 前提条件 NG（`status` の Fatal 項目に該当）、ロック競合、モデム抜去 |
| 3 | 認証失敗（連続失敗が `--max-auth-failures` に到達、resync 上限超過） |
| 4 | タイムアウト |

---

## 7. `connect` の処理シーケンス

### 7.1 手順

```
1. lock 取得
2. status 収集 → Fatal があれば表示して exit 2
3. --sim-slot 指定時: PrimarySimSlot と比較
     一致 → 続行
     不一致 かつ --switch-slot → SetPrimarySimSlot → モデム再出現待ち → 2. をやり直す
     不一致 かつ --switch-slot なし → exit 2
4. mbim-proxy に接続し、デバイスを OPEN。DEVICE_CAPS で疎通を確認する（AKA は実行しない）
   Authenticator を組み立てる（§4.3 経路の自動選択）
5. NAI 生成
6. wpa_supplicant の Interface を用意する（§7.3）
     無い → 設定ファイルを書いて CreateInterface
     simwifi の残骸 → RemoveInterface → CreateInterface
     他者の Interface → exit 2
7. NetworkRequest の購読を開始する（AddNetwork より前に）
8. AddNetwork → SelectNetwork
9. イベントループ
     NetworkRequest("SIM") → §7.2
     EAP "started"                 → resync カウンタを 0 に戻し、新しい EAP セッションとして扱う
     EAP "completion" + "failure"  → そのセッションを失敗として記録（認証失敗カウンタはセッションにつき 1 回だけ加算。§7.2）
     State == "completed"          → 接続成功。認証失敗カウンタを 0 に戻す。--exec-up を実行。以後は常駐
     State が completed から外れた → 記録し、--exec-down を実行。再接続は wpa_supplicant に任せる
                                     （4way_handshake / group_handshake は再認証・鍵更新の途中なので除く）
     認証失敗カウンタ ≥ --max-auth-failures → 10. の後始末をして exit 3
     resync カウンタ > 32          → 10. の後始末をして exit 3
     --timeout 到達（一度も completed になっていない） → 10. の後始末をして exit 4
     モデム抜去（InterfacesRemoved） → 10. の後始末をして exit 2
     wpa_supplicant 消失（NameOwnerChanged） → D-Bus の後始末はせず（呼ぶと D-Bus activation で再起動してしまう）、設定ファイル削除と lock 解放だけして exit 1
     SIGINT / SIGTERM → 10. の後始末をして exit 0
10. 後始末: --exec-down（up 実行済みなら）、RemoveNetwork、自分で作った Interface を RemoveInterface、
    設定ファイル削除、lock 解放
```

イベントループは純粋な状態機械として実装し、依存（Supplicant、Authenticator、モデム監視、時計、シグナル）を注入する。終了コードの分岐はモックで網羅的にテストする（§12.1）。

### 7.2 SIM 要求の処理

`NetworkRequest(path, "SIM", text)` の `text` の書式（wpa_supplicant `eap_aka_ext_sim_req`）:

```
UMTS-AUTH:<RAND hex 32>:<AUTN hex 32>
```

応答（`NetworkReply(path, "SIM", value)`）:

| Authenticate の結果 | 応答 | wpa_supplicant の動作 |
|---|---|---|
| 成功 | `UMTS-AUTH:<IK hex 32>:<CK hex 32>:<RES hex>`（**IK が先、CK が後**） | EAP-Response/AKA-Challenge |
| `ErrResync` | `UMTS-AUTS:<AUTS hex 28>`。resync カウンタを加算 | EAP-Response/AKA-Synchronization-Failure |
| `ErrAuthReject` | `UMTS-FAIL`。認証失敗カウンタを加算 | EAP-Response/AKA-Authentication-Reject |
| ローカルエラー（MBIM タイムアウト、モデムエラー、経路切替後も非対応など） | `UMTS-FAIL`。エラーログを出し、認証失敗カウンタを加算 | 同上 |

- wpa_supplicant は `UMTS-AUTH:` / `UMTS-AUTS:` 以外の応答を「AUTN 不正」と同じに扱い、Authentication-Reject を送る（§16.3）。無応答にすると EAP のタイムアウトまで状態が進まないため、必ず応答する
- `GSM-AUTH:` 形式（EAP-SIM）が来た場合は、エラーログを出して `GSM-FAIL` を返す（スコープ外）
- 応答を返したら `Result.Clear()` で鍵素材を消す
- SIM 要求は 1 件ずつ直列に処理する（USIM への要求を並行させない）

resync カウンタは EAP セッション単位で数える。EAP シグナル `started`（EAP-Request/Identity の受信時に出る）で 0 に戻す。上限は 32。

認証失敗カウンタは、1 回の EAP セッション（EAP `started` から次の `started` まで）につき最大 1 回だけ加算する。`UMTS-FAIL` を返した場合も、サーバーからの EAP-Failure（`completion` + `failure`）を受けた場合も「そのセッションは失敗」と記録し、二重には数えない。`UMTS-FAIL` の後にはふつう EAP-Failure が続くため。

### 7.3 wpa_supplicant の Interface 管理

PoC では、wlan の Interface は simwifi が所有する。wpa_supplicant のプロセス自体は systemd（Debian の `wpa_supplicant.service`: `-u -s -O ...`）と D-Bus activation に任せ、simwifi は起動しない。

- 設定ファイル `/run/simwifi/wpa-<iface>.conf`（0600）:
  ```
  # generated by simwifi; do not edit
  external_sim=1
  ```
- `CreateInterface({"Ifname": iface, "Driver": "nl80211", "ConfigFile": 上記})` で Interface を作る
- `fi.w1.wpa_supplicant1` がバスに居ない場合は、D-Bus activation で起動を試みる。それでも居なければ `status` で Fatal にし、`systemctl enable --now wpa_supplicant` を案内する
- 既存 Interface の扱いは §4.1 の `Attach` を参照
- 終了時は、自分が作った Interface だけを `RemoveInterface` する。プロセスの kill はしない（他の iface を巻き込まないため）
- NetworkManager が iface を管理している場合は `status` で Fatal にし、`nmcli device set <iface> managed no` を案内する

### 7.4 タイムアウト一覧

| 処理 | 既定 |
|---|---|
| MBIM コマンドの応答 | 10 秒 |
| MM の応答 | 10 秒 |
| スロット切替後のモデム再出現 | 60 秒 |
| wpa_supplicant の接続完了 | 60 秒（`--timeout`） |
| SIM 要求への応答 | MBIM の応答に準ずる。超えたら `UMTS-FAIL` を返す（§7.2） |
| `--exec-up` / `--exec-down` | 30 秒 |

---

## 8. アイデンティティ（NAI）生成

3GPP TS 23.003 §14 に従う。

```
<prefix><IMSI>@wlan.mnc<MNC3>.mcc<MCC>.3gppnetwork.org

prefix : EAP-AKA  → "0"
         EAP-AKA' → "6"
MNC3   : MNC を 3 桁にゼロ埋めしたもの（2 桁 MNC "10" → "010"）
```

- IMSI は 6〜15 桁の数字であることを検査する
- MCC / MNC は MM の `Sim.OperatorIdentifier` から取得する。長さ 5 なら MNC 2 桁、6 なら 3 桁として分割する（EF_AD の MNC 長を MM が反映済み）。先頭が IMSI の先頭と一致することも検査する
- `OperatorIdentifier` が空または不正な場合（SIM の読み込み未完了など）:
  - `--realm` があればそれを使う
  - 無ければ exit 2 とし、`--realm` で指定するよう案内する（IMSI だけでは MNC の桁数を決められないため）
- `--realm` を指定した場合は、`@` 以降をそのまま置き換える（prefix と IMSI は維持する）
- 疑似 ID と高速再認証 ID は wpa_supplicant が扱う。simwifi は永続化しない（毎回、永久 ID から始める）
- 永久 ID は EAP-Response/Identity で平文のまま送られる。PoC ではこれを許容する

---

## 9. SIM スロット

MBIM の `AKA` にも UICC APDU にもスロットを指定するフィールドが無く、認証は常に「現在アクティブなスロット」に対して行われる。MM も DSSS モデル（複数スロットのうちアクティブは 1 つ）である。

- `--sim-slot N` は「N がアクティブであること」の要求と解釈する
- 非アクティブなら既定ではエラーにする。`--switch-slot` を付けると `SetPrimarySimSlot(N)` を実行する。切替はモデムの再起動に相当し、セルラー接続は切れる
- ModemManager がスロット N を空き（`SimSlots` の要素が `/`）と報告している場合は、`--switch-slot` があっても切り替えずに exit 2 にする。空きスロットへの切り替えで、電源を入れ直すまでモデムが使えなくなった実例がある（Sierra EM7455。docs/COMPAT.md）
- 切り替え後にモデムが期待どおりに戻らなかった場合（タイムアウト）は、モデムを問い合わせ直し、再出現の有無、アクティブなスロット、状態（`failed` なら理由）をエラーに添える
- `PrimarySimSlot == 0`（マルチスロット非対応）のモデムで `--sim-slot` を指定した場合は、1 のときだけ受け付ける
- 「セルラーは SIM1、Wi-Fi 認証は SIM2」という使い方は、この方式ではできない。必要なら 2 台目のドングルを使うか、PC/SC リーダーと wpa_supplicant 標準の `pcsc` 経路を使う（`simauth` に実装を追加すれば対応できる）

---

## 10. エラー処理

| 事象 | 挙動 |
|---|---|
| mbim-proxy に接続できない | `status` で Fatal。MM が動いていれば proxy は常駐しているはずなので、MM 側の問題として案内する |
| mbim-proxy に拒否された（EOF） | 「root で実行してください」と表示して exit 2 |
| `AKA` が非対応 | `mbimuicc` に切り替えて同じ要求を再実行する（§4.3）。それも非対応なら `UMTS-FAIL` を返し、エラーを記録する。probe 結果で両方非対応と分かっていれば、`status` で Fatal にする |
| AUTS | `UMTS-AUTS` で返し、resync カウンタを加算する |
| AUTN の MAC 不正 | `UMTS-FAIL` で返し（Authentication-Reject）、認証失敗カウンタを加算する |
| EAP-Failure | ログに記録し（`EAP` シグナルの parameter を含む）、認証失敗カウンタを加算する。再試行は wpa_supplicant に任せる |
| AKA' で AMF の分離ビットが立っていない | wpa_supplicant が拒否する。simwifi からは EAP の失敗としか見えない。トラブルシュートに記載する |
| モデムの抜去 | MM の `InterfacesRemoved` を検知 → 後始末 → exit 2 |
| wpa_supplicant の消失 | D-Bus `NameOwnerChanged` → exit 1 |
| 既存 Interface（他者のもの） | exit 2。`status` にも表示する |

---

## 11. ログ

- 既定では **stderr** に `slog.TextHandler`（Info）で出す。標準出力は結果データ用に空けておく
- systemd 経由なら journald が拾う（`journalctl -u simwifi@wlan0 -f`）
- `--log-file PATH` を付けると `slog.JSONHandler` にも同時に出す。ファイルは 0600、ディレクトリは 0700
- レベル: 既定 Info、`-v` で Debug、`-vv` で Trace（`slog.Level(-8)`。MBIM の生メッセージの hexdump、D-Bus の引数）
- 共通属性: `component=cli|modem|mbim|simauth|supplicant`, `iface`, `modem`, `txid`（MBIM の TransactionId）
- ローテーションは `logrotate`（`contrib/simwifi.logrotate`、`copytruncate`）で行う
- メッセージは英語に固定する

秘匿情報の扱い:

| 値 | 既定 | 備考 |
|---|---|---|
| RES / CK / IK / AUTS | **出力しない**（Trace でも長さのみ） | `slog.LogValuer` を実装した型で包み、書き忘れても漏れないようにする。MBIM の AKA 応答と APDU 応答の hexdump も長さのみにする |
| RAND / AUTN | Debug で出力 | 調査価値が高く、秘密ではない |
| IMSI | マスク（`44010…3421`） | `--log-imsi` で全桁 |
| ICCID | マスク（先頭 4 桁と末尾 4 桁） | |
| NAI | IMSI 部分をマスク | |

---

## 12. テスト戦略

### 12.1 単体テスト

- `nai`: テーブル駆動（2 桁 / 3 桁 MNC、realm 上書き、AKA / AKA' の prefix、`OperatorIdentifier` の空・不正、IMSI の桁数異常）
- `simauth/milenage`: 3GPP TS 35.208 のテストベクタ。テスト用の「HSS 側」ヘルパー（RAND / AUTN の生成、AUTS の検証）も用意する
- `mbim`: unix ソケット上の fake proxy（`mbimtest`、Go 実装）に対して、フレーミング、フラグメント再構成、IndicateStatus の読み捨て、タイムアウト、拒否（即切断）、エラー変換を検証する
- `simauth/mbimaka`: fake proxy + `milenage` で RES / CK / IK / AUTS の往復と、Status の正規化を検証する
- `simauth/mbimuicc`: fake proxy に USIM の AUTHENTICATE 応答（`DB` / `DC` / `98 62`）を実装して検証する。チャネルが必ず閉じられることも確認する
- `modem` / `supplicant`（`wpadbus`）: プライベートな `dbus-daemon` 上に fake の MM / wpa_supplicant オブジェクトを export して検証する
- connect のイベントループ: interface のモックで検証する（成功、タイムアウト、resync 上限、連続認証失敗、MAC 不正、`GSM-AUTH`、経路の自動切替、モデム抜去、wpa_supplicant 消失、シグナルによる後始末、残骸 Interface の回収）
- `status`: 各チェックの Fatal 判定

### 12.2 E2E テスト（SIM なし）

- 環境: 実機の Debian 13（ミニ PC）上で `mac80211_hwsim radios=2`、hostapd（WPA-EAP、内蔵 EAP サーバー）、`hlr_auc_gw`（Milenage DB）を動かす
- simwifi は `e2e` ビルドタグ付きでビルドし、`--auth-backend milenage` で動かす
- シナリオ: AKA、AKA'（AMF 分離ビット ON の加入者）、AUTS（DB の SQN をずらす）、MAC 不正（K を変える）、WPA3、Ctrl-C の後始末、残骸 Interface の回収
- `test/e2e/run.sh` にまとめて 1 コマンドで再実行できるようにする

### 12.3 実機テスト

- 環境: Debian 13、MBIM ドングル、Soracom Onyx、sysmocom SIM、EAP-AKA PoC サーバー
- PoC サーバーは回線ごとに AMF を設定できる。**テスト回線は AMF 分離ビットを ON** にしておけば、AKA と AKA' の両方に使える（EAP-AKA 側はこのビットを検査しない）
- 手順: `status` → `probe` → `connect --method aka` → `connect --method akap` → 再認証（AP 側で reauth を強制）→ AUTS（サーバー側で SQN をずらす）→ `--auth-path uicc` で同じ手順を繰り返す
- 結果は `docs/COMPAT.md` に、モデムの型番、FW、`AKA` の可否、UICC APDU の可否、返った Status の癖として記録する

---

## 13. セキュリティと権限

### 13.1 現状（PoC）

- root での実行を前提とする。理由: mbim-proxy は接続元の UID を `SO_PEERCRED` で検査し、root（または libmbim のビルド時に `-Dmbim_username` で指定したユーザー）以外を拒否する。Debian 13 の libmbim 1.32 には、グループ指定のオプション（1.34 で追加）が無い
- simwifi は SIM に対する認証オラクルとして振る舞う。root 以外から操作できる口（ソケット等）は作らない
- wpa_supplicant の設定ファイルとログファイルは 0600
- 鍵素材（CK / IK / RES / AUTS）はログに出さず、応答後はメモリをゼロクリアする（`Result.Clear()`。Go では完全には保証できないが、方針として行う）
- E2E 用の Milenage バックエンドはビルドタグで分離し、リリースバイナリには含めない

### 13.2 systemd での運用

`contrib/simwifi@.service`（テンプレート。`%i` = iface）:

```
[Unit]
Description=simwifi EAP-AKA supplicant bridge on %i
Wants=ModemManager.service wpa_supplicant.service
After=ModemManager.service wpa_supplicant.service

[Service]
Type=simple
EnvironmentFile=/etc/simwifi/%i.env
ExecStart=/usr/local/bin/simwifi connect --iface %i --ssid ${SSID} --method ${METHOD} --log-file /var/log/simwifi/%i.log
Restart=on-failure
RestartSec=10
RuntimeDirectory=simwifi
RuntimeDirectoryPreserve=yes
RuntimeDirectoryMode=0700
LogsDirectory=simwifi
LogsDirectoryMode=0700
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
NoNewPrivileges=yes
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW

[Install]
WantedBy=multi-user.target
```

- `/run/simwifi` は `RuntimeDirectory=` で作る。`ReadWritePaths=` だと、ディレクトリが無いときに名前空間の準備で起動に失敗するため
- `RuntimeDirectoryPreserve=yes` にするのは、複数の iface のインスタンスが同じディレクトリを共有していて、1 つを止めたときに他のインスタンスのロックや設定ファイルが消えないようにするため
- wpa_supplicant とは D-Bus でしか話さないので、`/run/wpa_supplicant` への書き込み権限は不要
- `--exec-up` で起動する DHCP クライアントも、この Capability の範囲で動く

root のまま hardening で絞る方針（§14.3）。

---

## 14. 将来拡張（設計上の配慮のみ。PoC では未実装）

### 14.1 NetworkManager 対応

- NM は `external_sim` を扱えない。想定する形は「NM が所有する wpa_supplicant の Interface に相乗りし、`NetworkRequest("SIM")` にだけ応答する」
- そのために `Supplicant` interface の `Attach` に `AttachOptions` を持たせている（PoC では他者の Interface を拒否する）
- 課題: 相乗り時に `external_sim=1` を有効にする手段。D-Bus には無いため、制御ソケットの `SET external_sim 1` を併用する必要がある。NM のネットワークプロファイルに `eap=aka` を書けるかも含めて要調査
- 接続方式を最初から D-Bus にしているのはこのため

### 14.2 Passpoint 設定

- YAML で `cred` ブロック相当（realm、RCOI、domain、優先度）と SSID の一覧を記述し、`interworking=1` / `hs20=1` で ANQP ベースの自動選択を行う
- `--ssid` の直接指定は将来も残す
- 設定の優先順位: フラグ > 環境変数 > 設定ファイル
- `status` でルートの `Capabilities` から Interworking / HS20 の有無を表示するのは、この布石である

### 14.3 権限分離

- 選択肢 A: root のまま systemd の hardening で絞る（§13.2）。第一候補
- 選択肢 B: MBIM 部分だけを root のヘルパーに分離し、本体は専用ユーザーで実行する
- 選択肢 C: libmbim 1.34 以降の `-Dmbim_groupname` を使った distro のビルドを待つ

### 14.4 MM なしでの運用

- simwifi が mbim-proxy の spawn（`/usr/libexec/mbim-proxy`。アイドル 300 秒で自動終了）とライフサイクル管理、IMSI の取得（`SUBSCRIBER_READY_STATUS`）を持つ必要がある
- PoC では MM を必須とする

### 14.5 その他

- `--daemon`（自前でのデーモン化）。PoC では systemd を推奨し、実装しない
- EAP-SIM は対象外のまま（`GSM-AUTH` には `GSM-FAIL` を返す）

---

## 15. 決定事項一覧

| # | 決定 | 理由 |
|---|---|---|
| D1 | wlan の Interface は simwifi が作成して所有する。wlan は NM で unmanaged にする | 設定の衝突を避ける。NM 対応は将来 |
| D2 | wpa_supplicant との接続は D-Bus | NM 相乗りを見据える。制御ソケットは `SET external_sim` 用に限る |
| D3 | root での実行を前提とする | mbim-proxy が root 以外を拒否する |
| D4 | EAP の既定は AKA。`--method akap` で AKA'。フォールバックは無し。EAP-SIM は非対応 | 単純化 |
| D5 | `--nwname` は採用しない | wpa_supplicant 側に検証も設定も無く、simwifi は AT_KDF_INPUT を見られない |
| D6 | MNC の桁数は MM の `OperatorIdentifier` から自動判定する。`--realm` で全体を上書きできる。判定できなければ `--realm` を必須にする | EF_AD を MM が反映している |
| D7 | 初期は SSID の直接指定だけ。Passpoint は YAML で将来対応 | |
| D8 | 対象は Debian 13。前提のバージョンは `status` で検出する | |
| D9 | resync の上限は 32。EAP セッション単位で数え、EAP `started` で 0 に戻す | |
| D10 | 接続タイムアウトは 60 秒。終了コードは 0 / 1 / 2 / 3 / 4 | |
| D11 | DHCP・PIN・複数モデムの同時制御はスコープ外 | |
| D12 | iface 単位のロックファイル | |
| D13 | 外部依存は godbus だけ。CLI は標準 flag。Milenage も自前で実装する | テストベクタで正しさを担保できる |
| D14 | ログとメッセージは英語 | |
| D15 | 静的バイナリ + systemd ユニット + logrotate。設定はフラグのみ | |
| D16 | MM を必須とする | proxy の管理を持たない |
| D17 | ライセンスは MIT、公開リポジトリ | |
| D18 | wpa_supplicant のプロセスは systemd / D-Bus activation に任せ、simwifi は起動しない | `-i -c -B` だけの起動では D-Bus が有効にならない。起動方法を 1 経路に絞る |
| D19 | SIM 要求には必ず応答する。MAC 不正とローカルエラーには `UMTS-FAIL` を返す | 無応答だと EAP のタイムアウトまで状態が進まない |
| D20 | 認証失敗が `--max-auth-failures`（既定 3）回連続したら exit 3。`completed` で 0 に戻す | SIM や AAA への試行を繰り返しすぎない。systemd の `Restart=on-failure` と組み合わせる |
| D21 | 経路は `auto`（AKA → UICC の遅延フォールバック）を既定とし、`--auth-path` で固定もできる。AKA が AUTN を拒否したときも UICC で確かめ直す | probe を暗黙に実行して SIM を消費することを避ける。正しい AUTN を拒否する MBIM AKA 実装に対応する |
| D22 | `mbimuicc` を PoC 初版に含める | AKA 非対応のモデムでも動かすため |
| D23 | E2E 用の Milenage バックエンドは、ビルドタグ `e2e` のときだけ有効にする | SIM 無しで wpa_supplicant 連携を自動テストするため。リリースバイナリには含めない |
| D24 | probe 結果のキーは `EquipmentIdentifier` | MM の index は挿し直しで変わる |
| D25 | 既存 Interface は、`ConfigFile` が simwifi のものなら残骸として回収し、それ以外は exit 2 にする | 他者の Interface に `external_sim=0` のまま相乗りすると、SIM 要求が来ずに黙ってタイムアウトする |
| D26 | `--exec-up` / `--exec-down` は `/bin/sh -c` で実行し、失敗しても続行する | |

---

## 16. 事前調査の要約

### 16.1 mbim-proxy の認可（libmbim 1.32）

- 抽象 unix ソケット `mbim-proxy` でリッスンする。ファイル権限は無い
- `incoming_cb` で `SO_PEERCRED` から UID を取得し、`mbim_helpers_check_user_allowed` / `check_group_allowed` で検査する。既定のビルドでは **UID 0 だけを許可**する。許可しない接続は応答せずに切断する
- `-Dmbim_username` で追加のユーザー、`-Dmbim_groupname`（1.34 以降）で追加のグループを許可できる
- 単体で起動した場合、クライアントもデバイスも無い状態が 300 秒続くと終了する（`--no-exit` / `--empty-timeout`）。MM が動いていれば MM がデバイスを開き続けるので、常駐する

### 16.2 wpa_supplicant の AKA' ネットワーク名（`eap_peer/eap_aka.c`）

- AT_KDF_INPUT が空なら AUTN 不正として拒否する
- 受け取った値をそのまま `network_name` に保存する。`/* TODO: check Network Name per 3GPP.33.402 */` のままで、検証は実装されておらず、設定項目も無い
- その値で `eap_aka_prime_derive_ck_ik_prime` を実行する
- AKA' では、AUTN の AMF 分離ビット（0x8000）が立っていないと拒否する
- hostapd 側（`eap_server_aka.c`）のネットワーク名は `"WLAN"` 固定

### 16.3 wpa_supplicant の外部 SIM と D-Bus（2026-09 時点の hostap master で確認）

- 外部 SIM 要求は `eap_aka_ext_sim_req` が `UMTS-AUTH:<RAND>:<AUTN>` を作り、`eap_sm_request_sim` → `wpa_supplicant_eap_param_needed` → `wpas_notify_network_request` を経て、D-Bus シグナル `NetworkRequest(path, "SIM", text)` になる（`dbus_new.c`）
- 応答の処理は `eap_aka_ext_sim_result` で行う
  - `UMTS-AUTS:` → 同期失敗（Synchronization-Failure）
  - `UMTS-AUTH:<IK>:<CK>:<RES>`（IK が先）→ 成功
  - それ以外 → `-1` を返し、呼び出し元が **Authentication-Reject** を送る。hostap の hwsim テストは `UMTS-FAIL` を使っている
  - 形式不正（hex の誤りなど）の場合も `-1`
- `NetworkReply(path, "SIM", value)` は `external_sim_resp` に値を保存し、`pending_req_sim` をクリアする
- ルートオブジェクトのプロパティ: `EapMethods`（`as`）、`Capabilities`（`as`。`interworking` などを含む）
- Interface のプロパティ: `Capabilities`（`a{sv}`。`KeyMgmt` など）、`ConfigFile`（`s`）、`DisconnectReason`（`i`）、`CurrentNetwork`（`o`）
- EAP シグナル: `started`（EAP-Request/Identity を処理したとき）、`accept proposed method`、`completion` + `success` / `failure` など
- `external_sim` は `wpa_config` のグローバル項目で、Interface ごとの設定ファイルに書ける
- hwsim テスト（`tests/hwsim/test_ap_eap.py`）と `hlr_auc_gw.milenage_db` が、E2E 環境の参考になる

### 16.4 MBIM のメッセージ形式（libmbim master と MS の仕様で確認）

- Proxy Control `CONFIGURATION`（Set）: `DevicePath`（string）、`Timeout`（u32）。libmbim は proxy に接続した後、Proxy 設定 → `OPEN` の順に送る
- Auth `AKA`（Query）: `Rand[16]`, `Autn[16]` → `Res[16]`, `ResLen u32`, `IntegratingKey[16]`, `CipheringKey[16]`, `Auts[14]`
- MS UICC Low-Level Access のバイト配列（libmbim の `uicc-ref-byte-array`）は、**size が先、offset が後**（通常の offset / size と逆）
  - `OPEN_CHANNEL`（Set）: `AppIdSize`, `AppIdOffset`, `SelectP2Arg`, `ChannelGroup`, data → `Status`, `Channel`, `ResponseSize`, `ResponseOffset`, data
  - `CLOSE_CHANNEL`（Set）: `Channel`（0 なら `ChannelGroup` 指定）, `ChannelGroup` → `Status`
  - `APDU`（Set）: `Channel`, `SecureMessaging`, `ClassByteType`, `CommandSize`, `CommandOffset`, data → `Status`, `ResponseSize`, `ResponseOffset`, data
  - 応答の `Status` は 4 バイトのフィールドに **SW1, SW2 の順に** 格納される（u32 LE で読むと `SW1 | SW2<<8`）。`Response` に SW は含まれない
  - `90 00` と `91 xx` を正常とみなす。`61 xx` の GET RESPONSE はモデムが処理する
  - CLA はモデムが Type / Channel / SecureMessaging に合わせて置き換える
  - `APPLICATION_LIST`（CID 7）は MBIMEx の拡張で、libmbim 1.28 以降に定義がある。`ApplicationType`: `USIM = 4`
- `mbimcli` には AKA 認証や APDU を直接送るオプションが無い。実機での確認は simwifi の `probe` で行う（`--query-device-caps` と `--ms-query-uicc-application-list` は mbimcli で確認できる）

### 16.5 MBIM の Status コード

| 値 | 名前 | simwifi での扱い |
|---|---|---|
| 0 | `SUCCESS` | — |
| 1 | `BUSY` | ローカルエラー |
| 3 | `SIM_NOT_INSERTED` | ローカルエラー（`status` でも検出） |
| 9 | `NO_DEVICE_SUPPORT` | `ErrUnsupported` |
| 35 | `AUTH_INCORRECT_AUTN` | `ErrAuthReject` |
| 36 | `AUTH_SYNC_FAILURE` | `ErrResync` |
| 37 | `AUTH_AMF_NOT_SET` | `ErrAuthReject` |
| 0x87430001 | `MS_NO_LOGICAL_CHANNELS` | UICC 経路が使えない（`ErrUnsupported` 相当） |
| 0x87430002 | `MS_SELECT_FAILED` | 部分 AID で失敗した場合などに発生。UICC 経路が使えない |
| 0x87430003 | `MS_INVALID_LOGICAL_CHANNEL` | 内部エラー |

### 16.6 ModemManager の D-Bus API（ModemManager master で確認）

- `MMModemPortType`: `MBIM = 7`
- `MMModemLock`: `UNKNOWN = 0`, `NONE = 1`, `SIM_PIN = 2`, ...
- `MMModemState`: `FAILED = -1`, `UNKNOWN = 0`, `INITIALIZING = 1`, `LOCKED = 2`, `DISABLED = 3`, ...
- `Modem`: `Sim (o)`, `SimSlots (ao)`, `PrimarySimSlot (u)`, `Ports (a(su))`, `PrimaryPort (s)`, `EquipmentIdentifier (s)`, `DeviceIdentifier (s)`, `UnlockRequired (u)`, `State (i)`, `StateFailedReason (u)`, メソッド `SetPrimarySimSlot (u)`
- `Sim`: `Active (b)`, `SimIdentifier (s)`, `Imsi (s)`, `OperatorIdentifier (s)`, `OperatorName (s)`
- ルート `org.freedesktop.ModemManager1`: `Version (s)`

---

## 17. 参考資料

- RFC 4187 (EAP-AKA), RFC 5448 / RFC 9048 (EAP-AKA')
- 3GPP TS 23.003 §14（NAI）、TS 33.402（非 3GPP アクセスのセキュリティ）、TS 31.102（USIM AUTHENTICATE）、TS 35.206 / 35.208（Milenage とテストデータ）
- ETSI TS 102 221（UICC、SELECT / MANAGE CHANNEL）
- MBIM 1.0 仕様（USB-IF）、Microsoft MBIM 拡張（MS Basic Connect Extensions、MS UICC Low-Level Access: https://learn.microsoft.com/en-us/windows-hardware/drivers/network/mb-low-level-uicc-access）
- libmbim: `src/libmbim-glib/mbim-proxy.c`, `mbim-device.c`, `mbim-helpers.c`, `mbim-uuid.c`, `data/mbim-service-*.json`
- wpa_supplicant: `src/eap_peer/eap_aka.c`, `wpa_supplicant/wpas_glue.c`, `wpa_supplicant/dbus/dbus_new.c`, `dbus_new_handlers.c`, `doc/dbus.doxygen`, `tests/hwsim/test_ap_eap.py`
- ModemManager D-Bus API: `introspection/org.freedesktop.ModemManager1.Modem.xml`, `.Sim.xml`, `include/ModemManager-enums.h`
