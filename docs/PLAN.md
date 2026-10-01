# simwifi 開発プラン

対象: `docs/DESIGN.md`（PoC 設計、第 2 版）の実装計画。
作成日: 2026-09-30（同日改訂: 決定事項の反映、E2E 環境をミニ PC 上に変更、実機環境の準備手順を追加）

---

## 0. 方針

- **リスクの大きい前提を最初に実機で潰す**（M0 スパイク）。特に「wpa_supplicant が D-Bus で `NetworkRequest("SIM")` を出すか」「ドングルが `AUTH_AKA` に応えるか」の 2 点は、外れると設計の根幹が変わる。
- **ハードウェア非依存部分を先に固める**。開発機は WSL2 で、USB モデム・nl80211 の wlan を扱えない。純粋ロジック（NAI、Milenage、MBIM フレーミング）と fake を使ったテストは WSL2 で完結させ、実機（Debian 13）は検証に絞る。
- **fake を部品として残す**。fake mbim-proxy、fake MM / wpa_supplicant（プライベート D-Bus 上）、Milenage 認証器は、単体テストだけでなく後の E2E でも再利用する。
- **connect のイベントループは依存を注入できる形にする**（Supplicant / Authenticator / モデム監視 / 時計）。終了コードの分岐をモックで網羅するため。

---

## 進捗（2026-10-02 時点）

| # | 状態 | メモ |
|---|---|---|
| M0 | 完了 | スパイク S1〜S6 すべて合格（docs/SPIKE.md） |
| M1 | 完了 | Milenage は TS 35.208 の全 20 テストセットで一致 |
| M2 | 完了 | fake mbim-proxy で検証。`mbimuicc` と経路の自動選択（`simauth.Auto`）も前倒しで実装 |
| M3 | 完了 | 実機で `status` / `probe` を確認 |
| M4 | 完了 | fake wpa_supplicant を相手に `connect` 全体を 1 プロセス内で通す統合テストあり |
| M5 | 完了 | ミニ PC で E2E 6 シナリオすべて合格（2026-10-02） |
| M6 | 完了 | 複数スロットの切替は対象機が単一スロットのため未実施。実 SIM と実 AP で EAP-AKA / AKA' の接続、再認証、再同期（AUTS）に成功。EG25-G の MBIM AKA は正しい AUTN も拒否するため、自動経路が UICC で確かめ直して切り替える |
| M7 | 完了 | `contrib/`、README、リリースワークフロー（`v*` タグで GitHub Releases を作成）。最初のリリースは未実施 |

残り: 最初のリリース（タグの push）。

追加検証（2026-10-02）: Sierra Wireless EM7455（M.2 SIM スロット付き USB アダプタ）でも試験した。MBIM AKA は正しいチャレンジを拒否し、UICC 経路も無いため接続できなかった。Windows では同じ機材で EAP-AKA が通るので、原因の切り分けを進めたが（docs/COMPAT.md）、特定できないまま PoC としては区切った。この過程で、SIM-PIN2 の誤判定、空きスロットへの切り替え、切り替え失敗時の説明不足の 3 点を直した。

---

## 1. マイルストーン概要

| # | マイルストーン | 主な成果物 | 実行環境 | 目安 |
|---|---|---|---|---|
| M0 | 準備・スパイク | リポジトリ雛形、CI、前提検証メモ | WSL2 + 実機 | 1–2 日 |
| M1 | 基盤・純粋ロジック | `nai` `simauth/milenage` `logging` `lock` `cli` 骨格 | WSL2 | 2–3 日 |
| M2 | MBIM クライアント | `mbim`、fake proxy、`simauth/mbimaka` | WSL2 | 3–5 日 |
| M3 | ModemManager と status | `modem`、`status`、`status` / `identity` / `probe` コマンド | WSL2 + 実機 | 3–4 日 |
| M4 | wpa_supplicant 連携と connect | `supplicant`（wpadbus）、connect オーケストレータ | WSL2 | 5–7 日 |
| M5 | E2E 環境（SIM なし） | hwsim + hostapd + hlr_auc_gw による自動 E2E | 実機（ミニ PC） | 2–4 日 |
| M6 | 実機検証と UICC 予備経路 | `simauth/mbimuicc`、`docs/COMPAT.md` | 実機 | 3–5 日 |
| M7 | パッケージング・リリース | `contrib/`、README、リリースワークフロー | WSL2 + GitHub | 1–2 日 |

合計の目安: 20–32 人日。

### 依存関係と並行化

```
M0 ─▶ M1 ─┬─▶ M2 (mbim, mbimaka) ──┐
          ├─▶ M3 (modem, status) ──┼─▶ M4 (connect) ─▶ M5 (E2E) ─▶ M6 (実機) ─▶ M7
          └─▶ M4a (wpadbus 単体) ───┘
```

- M1 内の各パッケージは互いに独立しており、並行して作業できる。
- M2・M3・M4a（`supplicant` の D-Bus 実装単体）は M1 完了後に並行できる。
- クリティカルパス: M0 スパイク → M4 connect オーケストレータ → M5 E2E。

---

## 2. マイルストーン詳細

### M0: 準備・スパイク

**リポジトリ準備（WSL2）**

- [x] `git init`、`go.mod`（`github.com/oyaguma3/simwifi`、`go 1.27`）、`LICENSE`（MIT）、`.gitignore`
- [x] `simwifi-DESIGN.md` を `docs/DESIGN.md` へ移動（§4 の構成に合わせる）
- [x] GitHub Actions: `go vet`、`go test -race ./...`、`CGO_ENABLED=0` で amd64/arm64 ビルド
- [x] `Makefile`（`build` / `test` / `lint`、`-ldflags "-X ...version"`）

**前提検証スパイク（実機 Debian 13）** — 結果は `docs/SPIKE.md` に記録

| # | 検証内容 | 方法 | 外れた場合の影響 |
|---|---|---|---|
| S1 | wpa_supplicant が `external_sim=1` で D-Bus シグナル `NetworkRequest(path, "SIM", "UMTS-AUTH:…")` を出す | `CreateInterface(ConfigFile=…)` → AKA の AP に接続 → `dbus-monitor --system` で観測 | D-Bus 方式が成立せず、制御ソケット方式への切替が必要（D2 見直し） |
| S2 | Debian 13 の wpa_supplicant で `EapMethods` に `AKA` / `AKA'` が含まれる | D-Bus ルートオブジェクトの `EapMethods` プロパティ | 自前ビルドの wpa_supplicant が必要 |
| S3 | mbim-proxy 経由で疎通でき、非 root は無応答で切断される | `mbimcli -p -d /dev/cdc-wdm0 --query-device-caps` を root / 非 root で実行 | §4.2 のエラー判定を修正 |
| S4 | 手持ちドングルの `AUTH_AKA` 対応状況と返る Status | mbimcli の該当オプション（存在すれば）、無ければ M2 完了後に `probe` で確認 | `mbimuicc` の優先度を上げる |
| S5 | libmbim のソースから UUID 定数と Proxy Control `CONFIGURATION` の情報バッファ形式を確認 | `mbim-uuid.c`、`mbim-proxy.c` | — |
| S6 | 外部 SIM 応答で `UMTS-AUTH` / `UMTS-AUTS` 以外を返すと Authentication-Reject になるか | `eap_aka.c` の `eap_aka_ext_sim_result` を確認 | MAC 失敗時の応答方針（§3 論点 3）に影響 |

S1 は PoC サーバーの AP、または M5 の hwsim 環境を先に作って確認してもよい（SIM 不要で済む）。

### M1: 基盤・純粋ロジック（WSL2 で完結）

- [x] `internal/nai`: `Generate(imsi, operatorID, method, realmOverride)`
  - テーブルテスト: 2 桁 / 3 桁 MNC、realm 上書き、AKA=`0` / AKA'=`6` prefix、不正入力（`OperatorIdentifier` が空、IMSI 桁数異常）
- [x] `internal/simauth`: `Authenticator` / `Result` / `ErrResync` / 認証拒否エラー（MAC 失敗）の定義
- [x] `internal/simauth/milenage`: f1–f5、f1\*、f5\*（`crypto/aes` のみ）
  - 3GPP TS 35.208 のテストセットで検証
  - テスト用の「HSS 側」ヘルパー（K/OPc/SQN から RAND/AUTN を生成、AUTS を検証）も用意し、M2・M4・M5 で使う
- [x] `internal/logging`
  - Trace レベル（`slog.Level(-8)`）、stderr Text + ファイル JSON のファンアウト Handler
  - マスク関数（IMSI・ICCID・NAI）
  - 鍵素材は `slog.LogValuer` を実装した型で包み、常に長さだけを出す（書き忘れによる漏洩を防ぐ）
  - ファイル 0600 / ディレクトリ 0700
- [x] `internal/lock`: `/run/simwifi/<iface>.lock` への `flock(LOCK_EX|LOCK_NB)`。ディレクトリが無ければ 0700 で作成
- [x] `internal/cli` の骨格
  - サブコマンドのディスパッチ、共通フラグ、`version`（`debug.ReadBuildInfo` と ldflags）
  - `-v` / `-vv`: 標準 `flag` ではそのまま書けないため、`-vv` を別の bool フラグとして定義するか、引数を事前に正規化する
  - エラー型 → 終了コード（0–4）の対応を 1 箇所に集約（`ExitError{Code}` 等）

**完了条件**: `go test ./...` が通り、`simwifi version` と `simwifi identity --help` が動く。

### M2: MBIM クライアント

- [x] `internal/mbim`
  - メッセージのエンコード / デコード（Header、Fragment、Command、CommandDone、IndicateStatus、FunctionError、Open / OpenDone）
  - 受信ループ用 goroutine と TransactionId → 応答チャネルの多重化。受信フラグメントの再構成
  - Proxy Control `CONFIGURATION`（DevicePath UTF-16LE + Timeout）→ `OPEN`（MaxControlTransfer=4096）
  - `Query(ctx, service, cid, buf)`。既定タイムアウトは 10 秒
  - エラー型: `MBIMError{Status}`、`ErrProxyRejected`（接続直後の EOF）
  - Trace レベルで送受信を hexdump
  - 接続先アドレスは差し替え可能にする（テストでランダムな抽象ソケット名を使うため）
- [x] `internal/mbim/mbimtest`: fake mbim-proxy
  - 応答をスクリプトで与えられる。応答を複数フラグメントに分割できる。IndicateStatus を混ぜられる。接続直後に切断して拒否を再現できる
  - `AUTH_AKA` ハンドラを Milenage で実装できるようにする
- [x] `internal/simauth/mbimaka`
  - Query の組み立てと Response（`Res[16]`, `ResLength`, `IK`, `CK`, `Auts[14]`）の解釈
  - Status の正規化: `AUTH_SYNC_FAILURE` と「成功だが `ResLength==0` かつ AUTS が非ゼロ」→ `ErrResync`、`AUTH_INCORRECT_AUTN` → 認証拒否、`NO_DEVICE_SUPPORT` → 非対応エラー
  - fake proxy + Milenage で RES/CK/IK/AUTS の往復をテスト

**完了条件**: 正常系・フラグメント・タイムアウト・拒否・各 Status のテストが通る。

### M3: ModemManager と status

- [x] `internal/modem`
  - `GetManagedObjects` でモデムを列挙し、`--modem <index|path>` で選択
  - Modem / Sim / SimSlots のプロパティ取得。MBIM ポートから `/dev/cdc-wdmN` を導出
  - PIN ロック判定（`UnlockRequired`、`State == locked`）
  - `SetPrimarySimSlot` と、`InterfacesAdded` によるモデム再出現待ち（60 秒）
  - `InterfacesRemoved` の監視（connect 中のモデム抜去検知用）
- [x] D-Bus 側のテスト基盤: `dbus-daemon` をプライベートバスとして起動し、godbus で fake MM オブジェクトを export する。`dbus-daemon` が無い環境では skip
- [x] `internal/status`: §6.2 の各 Check を独立した関数として実装し、`Fatal` 判定をテーブルテストする
- [x] コマンド: `status`（テキスト / `--json`）、`identity`、`probe`（DEVICE_CAPS → AUTH_AKA ダミー → ATR、結果を JSON 保存）

**完了条件**: 実機で `status` / `identity` / `probe` が期待通り動き、S4 の結果が `docs/SPIKE.md` に記録されている。

### M4: wpa_supplicant 連携と connect

- [x] `internal/supplicant`: interface（§4.1）と `wpadbus` 実装
  - `Attach`: `GetInterface`。無ければ設定ファイル（0600）を書いて `CreateInterface({Ifname, Driver, ConfigFile})`
  - `AddNetwork` のキー対応（§4.1 の表）と `--wpa3`
  - `SIMRequests`: 自分の Interface パスに絞ったシグナル購読
  - `Events`: `State`、`EAP`、`DisconnectReason` などを `PropertiesChanged` から取り出す
  - `Capabilities`: ルートオブジェクトの `EapMethods` / `Capabilities` と、Interface の `Capabilities.KeyMgmt`
  - `fi.w1.wpa_supplicant1` の `NameOwnerChanged` 監視
  - プライベートバス上の fake wpa_supplicant でテスト
- [x] connect オーケストレータ（§7.1 の手順 1–10）
  - イベントループは純粋な状態機械として書き、依存（Supplicant、Authenticator、モデム監視、時計、シグナル）を注入する
  - SIM 要求の処理: `UMTS-AUTH` をパース → Authenticate → `UMTS-AUTH:<IK>:<CK>:<RES>` / `UMTS-AUTS:<AUTS>`。`GSM-AUTH` はエラーログのみ
  - 応答後は鍵素材をゼロクリア
  - `--exec-up` / `--exec-down`: `/bin/sh -c` で実行し、`SIMWIFI_IFACE` などを環境変数で渡す（仕様化が必要）
- [x] モックを使ったシナリオテスト
  - 接続成功 → exec-up → SIGINT → 後始末 → exit 0
  - 未接続のまま `--timeout` → exit 4
  - resync が上限を超える → RemoveNetwork → exit 3
  - AUTN 不正 → exit 3
  - モデム抜去 → exit 2
  - wpa_supplicant 消失 → exit 1
  - 前提 NG → exit 2、ロック競合 → exit 2
  - 自前で作った Interface だけを後始末で Remove すること

**完了条件**: シナリオテストが全て通る。`go test -race` がクリーン。

### M5: E2E 環境（SIM なし）

SIM・ドングル・実 AP なしで、wpa_supplicant との連携を自動テストする。

- [x] 実機のミニ PC（Debian 13 の標準カーネルに `mac80211_hwsim` がある。VM は不要）
  - `modprobe mac80211_hwsim radios=2`（実 wlan とは別の `wlanN` ができる。テスト後に `rmmod`）
  - hostapd: WPA-EAP、`eap_server=1`、`eap_sim_db=unix:…`、EAP-AKA / AKA' のユーザー定義
  - `hlr_auc_gw -m milenage.db`（K/OPc/AMF/SQN を定義。AKA' 用に AMF の分離ビットを ON）
- [x] simwifi 側に開発用バックエンドを追加（DESIGN §6.1、D23）: ビルドタグ `e2e` で `--auth-backend milenage` を有効にし、MM / mbim のチェックを省く
- [x] シナリオ: AKA 接続、AKA' 接続、AUTS（USIM 側の SQN を進めておく）、MAC 不正、異常終了からの回復、WPA3（`WPA-EAP-SHA256` + PMF）、Ctrl-C で後始末。再認証は実 AP のテスト（M6）で確認する
- [x] `test/e2e/run.sh` にまとめ、手元で 1 コマンドで再実行できるようにする

**完了条件**: 上記シナリオがミニ PC 上で自動で通る。

### M6: 実機検証と UICC 予備経路

- [x] §12.2 の実機手順: `status` → `probe` → `connect --method aka` → `--method akap` → 再認証 → AUTS
- [ ] `--sim-slot` / `--switch-slot` の実機確認（切替後の再出現待ちと、そのタイムアウト）。Soracom Onyx（EG25-G）は単一スロットのため実機では未実施。fake ModemManager の統合テストでのみ確認
- [x] `internal/simauth/mbimuicc`
  - USIM AID の取得（MS UICC Low-Level Access の `APPLICATION_LIST`、または EF_DIR）→ `OPEN_CHANNEL`
  - `APDU`: `AUTHENTICATE`（CLA は論理チャネル番号を反映、INS 0x88、P1 0x00、P2 0x81、データ `10‖RAND‖10‖AUTN`）
  - 応答の解析: タグ `DB`（RES / CK / IK）、`DC`（AUTS）、SW `98 62`（MAC 失敗）、`61xx` の GET RESPONSE
  - `CLOSE_CHANNEL`。エラー時も必ず閉じる
  - fake proxy でテストする
  - 手持ちドングルが `AUTH_AKA` に対応していれば、実機確認は UICC 経路を強制するフラグ（`--auth-path uicc` 等）で行う
- [x] `docs/COMPAT.md`: モデム型番、FW、`AUTH_AKA` 可否、UICC APDU 可否、返った Status の癖

**完了条件**: 実機で AKA / AKA' / AUTS が成功し、COMPAT.md に 1 機種以上記録されている。

### M7: パッケージング・リリース

- [x] `contrib/simwifi@.service`（論点 8 の修正を反映）、`/etc/simwifi/<iface>.env` の例
- [x] `contrib/simwifi.logrotate`（ファイルを開いたまま書き続けるため `copytruncate`。または SIGHUP で開き直す）
- [x] README: 前提、NM の unmanaged 化手順、インストール、使用例、トラブルシュート（root、AMF 分離ビット、NM 競合、PIN）
- [x] リリースワークフロー: タグ push で静的バイナリ（amd64 / arm64）とチェックサムを添付

---

## 3. 設計書への指摘・確認事項

14 件すべてを `docs/DESIGN.md` 第 2 版に反映済み（2026-09-30）。★ の事実確認は、wpa_supplicant / libmbim / ModemManager のソースと MS の仕様で行った（DESIGN §16.3–16.6）。

| # | 指摘 | 反映先 |
|---|---|---|
| 1 | `-i -c -B` だけの起動では D-Bus が無効 | §7.3、D18 |
| 2 | `EapMethods` はルートオブジェクトのプロパティ（確認済み） | §4.1、§6.2、§16.3 |
| 3 | MAC 失敗時は `UMTS-FAIL` で Authentication-Reject（確認済み） | §7.2、D19 |
| 4 | 暗黙 probe と AKA 消費の矛盾 | §4.3「経路の自動選択」、§7.1、D21 |
| 5 | resync のリセット条件の統一 | §7.2、D9 |
| 6 | probe 結果のキーを `EquipmentIdentifier` に | §6.3、D24 |
| 7 | `mbimuicc` の AID と APDU の詳細（`61xx` はモデムが処理することを確認） | §4.3、§16.4 |
| 8 | systemd の `RuntimeDirectory=` | §13.2 |
| 9 | PIN ロックの判定元 | §4.4、§6.2 |
| 10 | `OperatorIdentifier` が空の場合 | §8、D6 |
| 11 | 既存 Interface（`ConfigFile` プロパティで残骸を判別できることを確認） | §4.1、§7.3、D25 |
| 12 | E2E 用 Milenage バックエンド | §6.1、D23 |
| 13 | `main.go` の委譲先 | §4 |
| 14 | `--exec-up` の仕様 | §6.5、D26 |

改訂の中で追加した決定: 連続認証失敗での終了（`--max-auth-failures`、D20）、`--auth-path`（D21）。

## 4. リスク

| リスク | 影響 | 対策 |
|---|---|---|
| D-Bus の `NetworkRequest("SIM")` が期待通りに動かない | 大（D2 の見直し） | S1 で最初に確認。駄目なら制御ソケットの `CTRL-REQ-SIM` / `CTRL-RSP-SIM` に切り替え、`Supplicant` interface の裏側だけ差し替える |
| ドングルごとに `AUTH_AKA` の対応状況や Status の癖が違う | 中 | Status の正規化、`mbimuicc` の予備経路、COMPAT.md への蓄積 |
| WSL2 でハードウェアを扱えない | 中 | fake と E2E（M5）で大半を検証し、実機での確認は M0 / M3 / M6 に集中させる |
| MM のスロット切替の挙動がモデムによって違う | 小〜中 | `--switch-slot` は任意機能。タイムアウトとエラー案内を丁寧にする |
| Debian の wpa_supplicant で AKA' が無効 | 中 | S2 で確認。無効なら README で自前ビルドを案内する |

---

## 5. 決定事項（2026-09-30）

1. 実機: ミニ PC に Debian 13 をインストールすれば揃う。WSL2 から Tailscale 経由の SSH で操作する（§6）
2. E2E 用の Milenage バックエンド: 採用。外部ライブラリ（`wmnsk/milenage`）も使ってよいとの許可を得たが、実装量が小さく、テストベクタで検証できるため自前で実装する（D13）
3. `mbimuicc`: PoC 初版に含める（D22）
4. 指摘はすべて設計書に反映してから実装に入る → 反映済み

---

## 6. 実機（ミニ PC）の準備

WSL2 から SSH で入れるようにすれば、Claude から実機を操作できる。

- **管理用の回線は有線 LAN にする**。テスト対象の wlan は simwifi が Interface を作り直すので、SSH がその wlan 経由だと切れる。Tailscale も有線側で動かす
- SSH の接続先に別名を付ける（例: `~/.ssh/config` の `Host simwifi-dut`）。鍵認証のみにする
- simwifi は root が必要なので、非対話で root 権限を使えるようにする。次のどちらか
  - 作業用ユーザー + `sudo` の NOPASSWD（推奨）
  - root での鍵ログイン（`PermitRootLogin prohibit-password`）
- ミニ PC に入れるパッケージ: `modemmanager`, `libmbim-utils`, `wpasupplicant`, `hostapd`（E2E 用）, `iw`, `rfkill`, `dbus`。Go はミニ PC に不要（WSL2 で静的バイナリをビルドして `scp` する）
- NetworkManager を使う場合は、テスト用 wlan を `unmanaged` にする
- WSL2 から Tailscale のホスト名が引けない場合は、100.x の IP アドレスで指定するか、WSL の networking mode を `mirrored` にする

