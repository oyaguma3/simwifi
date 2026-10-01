# simwifi

[English version](README.md)

MBIM モードの USB 通信モデムに挿した SIM を使い、Linux PC から EAP-AKA / EAP-AKA' で WPA2 / WPA3-Enterprise の無線 LAN に接続する CLI ツールです。

EAP-AKA の処理そのものは wpa_supplicant が行います。simwifi は、wpa_supplicant が D-Bus で出す USIM への AUTHENTICATE の要求（RAND / AUTN）を受け取り、MBIM モデムの SIM に答えさせて、結果を返します。あわせて、モデムの検出、ID（NAI）の生成、wpa_supplicant の設定、状態の表示を 1 つのコマンドで行います。

> **ステータス: PoC（概念実証）**。Debian 13 と実機・実 SIM・実際の EAP-AKA サーバーで、EAP-AKA / AKA' の接続、再認証、再同期（AUTS）を確認済みです。詳しくは [docs/COMPAT.md](docs/COMPAT.md) を参照してください。

## 仕組み

```
AAA ─ AP ─ wlan0 ─ wpa_supplicant ─(D-Bus: NetworkRequest "SIM")─ simwifi ─ mbim-proxy ─ MBIM モデム ─ USIM
```

- USIM への主な経路は、MBIM の Auth サービスの `AKA` コマンドです。Qualcomm 系のモデムは AKA の値を 128 ビットのリトルエンディアン整数として扱う（RAND / AUTN も RES / CK / IK / AUTS も、すべてバイトが逆順になる）ため、simwifi はモデムごとにバイト順を自動で判別します。
- `AKA` コマンドに対応していないモデムでは、MS UICC Low-Level Access（論理チャネル上の APDU）に自動で切り替えます。
- 設計の詳細: [docs/DESIGN.md](docs/DESIGN.md)

## 動作確認済みの機材

| 製品 | モジュール | MBIM AKA | UICC Low-Level Access | 結果 |
|---|---|---|---|---|
| Soracom Onyx | Quectel EG25-G | 対応（バイト順は逆） | 対応 | EAP-AKA / AKA'、再認証、再同期 |
| M.2 USB アダプタ | Sierra Wireless EM7455 | 対応（バイト順は逆） | 非対応 | EAP-AKA / AKA' |

詳細と機種ごとの癖: [docs/COMPAT.md](docs/COMPAT.md)

## 前提

- Debian 13（trixie）以降
- ModemManager 1.18 以降、libmbim 1.32 以降（`mbim-proxy`）
- wpa_supplicant 2.10 以降。D-Bus が有効な状態で動いていて（`wpa_supplicant.service`）、EAP-AKA / AKA' に対応していること
- nl80211 ドライバの無線 LAN。NetworkManager を使っている場合は、対象のインターフェースを管理対象から外す: `nmcli device set wlan0 managed no`
- root で実行すること（`mbim-proxy` が root 以外を受け付けないため）
- SIM の PIN は無効にしておくか、解除済みであること（simwifi は PIN を解除しません）

## インストール

[GitHub Releases](https://github.com/oyaguma3/simwifi/releases) から、アーキテクチャに合った tar.gz と `SHA256SUMS` を取得します。

```bash
sha256sum -c --ignore-missing SHA256SUMS
```

```bash
tar -xzf simwifi-<版>-linux-amd64.tar.gz
```

```bash
sudo install -m 0755 simwifi-<版>-linux-amd64/simwifi /usr/local/bin/simwifi
```

サービスとして常駐させる場合は、tar.gz に同梱の `contrib/` のファイルを使います。`simwifi@.service` は `/etc/systemd/system/` に、環境変数ファイルは `/etc/simwifi/<iface>.env` に（`simwifi.env.example` を参照）、logrotate の設定は `/etc/logrotate.d/simwifi` に置き、次のコマンドで起動します。

```bash
sudo systemctl enable --now simwifi@wlan0
```

## 使い方

```bash
sudo simwifi status                     # 前提条件の確認（読み取りのみ）
sudo simwifi probe                      # モデムの能力の確認（SIM の認証を 1 回使う）
sudo simwifi identity --method akap     # SIM から作った永久 ID（NAI）を表示
sudo simwifi connect --iface wlan0 --ssid corp-wifi --method aka --exec-up "dhclient -1 -nw wlan0"
```

`connect` は前面で動き続けます。Ctrl-C（または SIGTERM）で切断し、後始末をして終了します。

`connect` の主なオプション:

| オプション | 既定 | 説明 |
|---|---|---|
| `--ssid` | （必須） | 接続先の SSID |
| `--method aka\|akap` | `aka` | EAP-AKA か EAP-AKA' か |
| `--iface` | `wlan0` | 無線 LAN のインターフェース |
| `--realm` | SIM から生成 | NAI の realm 全体を上書きする |
| `--auth-path auto\|aka\|uicc` | `auto` | USIM への経路（`auto` は使える経路を自動で選ぶ） |
| `--wpa3` | オフ | `WPA-EAP-SHA256` と PMF 必須を使う |
| `--timeout` | 60 | 接続を待つ秒数 |
| `--max-auth-failures` | 3 | 認証がこの回数続けて失敗したら終了する |
| `--exec-up` / `--exec-down` | なし | 接続時・切断時に実行するシェルコマンド |
| `--sim-slot N` / `--switch-slot` | なし | SIM スロット N がアクティブであることを求める ／ 必要なら切り替える |
| `--log-file`、`-v`、`-vv` | 標準エラー、Info | JSON のログファイル、デバッグ ／ トレースのログ |

終了コード:

| コード | 意味 |
|---|---|
| 0 | 成功（`connect` 中の Ctrl-C / SIGTERM による終了を含む） |
| 1 | 使い方の誤り、内部エラー、wpa_supplicant の消失 |
| 2 | 前提条件を満たしていない（`simwifi status` で確認） |
| 3 | 認証に失敗した |
| 4 | タイムアウト |

ログに RES / CK / IK / AUTS は出力しません。IMSI は、`--log-imsi` を付けない限りマスクします。

## トラブルシュート

| 症状 | 確認すること |
|---|---|
| `mbim-proxy closed the connection immediately` | root で実行しているか。 |
| `wireless interface is already managed by another wpa_supplicant client` | NetworkManager がインターフェースを管理していないか。 |
| `SIM is locked` | ModemManager で SIM のロックを解除する（`mmcli -i <SIM の番号> --pin=...`）か、PIN を無効にする。 |
| `cannot determine MCC/MNC` | `--realm` で realm を指定する。 |
| EAP-AKA' だけ失敗する | 網側が AUTN の AMF で分離ビット（0x8000）を立てているか（wpa_supplicant が検査する）。 |
| Lenovo 向けの EM7455（USB ID `1199:9079`）で無線をオンにできない | FCC ロックがかかっている。EAP-AKA に無線は不要なので、認証には影響しない。 |

## ソースからのビルド

Go 1.27 以降が必要です。外部依存は `github.com/godbus/dbus/v5` だけで、静的バイナリになります。

```bash
make build        # bin/simwifi
make dist         # dist/simwifi-<版>-linux-{amd64,arm64}.tar.gz と SHA256SUMS
make test         # 単体テストと統合テスト（D-Bus の fake には dbus-daemon が必要）
make lint         # go vet と golangci-lint
```

`v1.2.3` 形式のタグを push すると、GitHub Actions がリリースを作ります（ハイフン付きのタグはプレリリースになります）。

## テスト

- 単体テストと統合テスト: `make test`。mbim-proxy、ModemManager、wpa_supplicant の fake を使います。
- SIM を使わない実機での E2E テスト（mac80211_hwsim + hostapd）: [test/e2e/README.md](test/e2e/README.md)

## ライセンス

MIT
