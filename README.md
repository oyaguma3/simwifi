# simwifi

MBIM モードの USB 通信ドングルに挿した SIM を使い、Linux PC から EAP-AKA / EAP-AKA' で無線 LAN（WPA2 / WPA3-Enterprise）に接続する CLI ツール。

EAP-AKA の本体は wpa_supplicant が担う。simwifi は、wpa_supplicant が USIM に投げたい AUTHENTICATE（RAND / AUTN）を D-Bus で受け取り、MBIM ドングル経由で SIM に解かせて返す。あわせて、モデム検出、NAI 生成、wpa_supplicant への設定投入、状態表示を 1 コマンドで行う。

> **ステータス: PoC**。Soracom Onyx（Quectel EG25-G）と Debian 13 の実機で、EAP-AKA / AKA' の接続・再認証・再同期を確認済み（[docs/COMPAT.md](docs/COMPAT.md)）。
>
> 既知の制限: 試した 2 機種（Quectel EG25-G、Sierra Wireless EM7455）とも、Linux では MBIM の AKA コマンドが正しいチャレンジを拒否した（原因は未特定）。EG25-G は UICC Low-Level Access の経路で動作するが、この経路を持たない EM7455 は使えない。

## 仕組み

```
AAA ─ AP ─ wlan0 ─ wpa_supplicant ─(D-Bus: NetworkRequest "SIM")─ simwifi ─ mbim-proxy ─ MBIM ドングル ─ USIM
```

- USIM への経路は MBIM の Auth `AKA` CID が主経路。非対応のモデムや、`AKA` CID が正しく動かないモデム（Quectel EG25-G など）では、MS UICC Low-Level Access（論理チャネル上の APDU）に自動で切り替える
- 詳細は [docs/DESIGN.md](docs/DESIGN.md)

## 前提

- Debian 13（trixie）以降
- ModemManager 1.18 以降、libmbim 1.32 以降（mbim-proxy）
- wpa_supplicant 2.10 以降（`wpa_supplicant.service` で D-Bus 有効、EAP-AKA / AKA' 対応）
- 無線 LAN は nl80211 ドライバ。NetworkManager を使っている場合は対象の iface を unmanaged にする（`nmcli device set wlan0 managed no`）
- root で実行する（mbim-proxy が root 以外を拒否するため）

## ビルド

Go 1.27 以降。外部依存は `github.com/godbus/dbus/v5` だけで、静的バイナリになる。

```bash
make build        # bin/simwifi
make dist         # dist/simwifi-<版>-linux-{amd64,arm64}.tar.gz と SHA256SUMS
make test         # 単体テスト（dbus-daemon があれば D-Bus の fake を使うテストも走る）
```

## インストール

[GitHub Releases](https://github.com/oyaguma3/simwifi/releases) から、アーキテクチャに合った tar.gz と `SHA256SUMS` を取得する。

```bash
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf simwifi-<版>-linux-amd64.tar.gz
sudo install -m 0755 simwifi-<版>-linux-amd64/simwifi /usr/local/bin/simwifi
```

systemd で常駐させる場合は、同梱の `contrib/` の各ファイルを使う（`simwifi@.service` は `/etc/systemd/system/`、環境変数ファイルは `/etc/simwifi/<iface>.env`、logrotate 設定は `/etc/logrotate.d/simwifi`）。

リリースは `v1.2.3` 形式のタグを push すると GitHub Actions が作る（ハイフン付きのタグはプレリリース）。

## 使い方

```bash
sudo simwifi status                     # 前提条件の確認（副作用なし）
sudo simwifi probe                      # モデムの能力確認（SIM の認証を 1 回使う）
sudo simwifi identity --method akap     # SIM から生成した NAI を表示
sudo simwifi connect --iface wlan0 --ssid corp-wifi --method aka --exec-up "dhclient -1 -nw wlan0"
```

`connect` は前面で常駐し、Ctrl-C で切断して後始末する。常駐させる場合は systemd ユニット（[contrib/simwifi@.service](contrib/simwifi@.service)）を使う。

終了コード: 0 成功、1 使い方の誤り・内部エラー、2 前提条件 NG、3 認証失敗、4 タイムアウト。

## トラブルシュート

| 症状 | 確認すること |
|---|---|
| `mbim-proxy closed the connection immediately` | root で実行しているか |
| `wireless interface is already managed by another wpa_supplicant client` | NetworkManager が iface を管理していないか |
| EAP-AKA' だけ失敗する | 網側の AUTN の AMF で分離ビット（0x8000）が立っているか（wpa_supplicant が検査する） |
| `cannot determine MCC/MNC` | `--realm` で realm を指定する |

## テスト

- 単体テスト・統合テスト: `make test`（fake の mbim-proxy / ModemManager / wpa_supplicant を使う）
- E2E テスト（実機、SIM 不要）: [test/e2e/README.md](test/e2e/README.md)

## ライセンス

MIT
