# simwifi ユーザーガイド

[English version](USER_GUIDE.md)

README の「[使い方](../README.ja.md#使い方)」の詳細版です。すべてのコマンドとオプション、`connect` の動き、よくある使い方ごとのコマンド例をまとめています。インストールと前提条件は [README](../README.ja.md) を参照してください。

## 目次

- [1. 最初の接続までの流れ](#1-最初の接続までの流れ)
- [2. 共通オプション](#2-共通オプション)
- [3. status — 前提条件の確認](#3-status--前提条件の確認)
- [4. probe — モデムの能力の確認](#4-probe--モデムの能力の確認)
- [5. identity — ID（NAI）の表示](#5-identity--idnaiの表示)
- [6. connect — 接続](#6-connect--接続)
- [7. 使い方の例](#7-使い方の例)
- [8. systemd で常駐させる](#8-systemd-で常駐させる)
- [9. 終了コード](#9-終了コード)
- [10. ログ](#10-ログ)
- [11. トラブルシュート](#11-トラブルシュート)

## 1. 最初の接続までの流れ

`version` 以外のコマンドは root で実行します（`mbim-proxy` が root 以外を受け付けないため）。

1. **前提条件を確かめる。** `--iface` には、実際の無線 LAN のインターフェース名を指定します（既定は `wlan0`）。
   ```bash
   sudo simwifi status --iface wlan0
   ```
   `✘` の付いた項目があれば、その行のメッセージに従って直します。

2. **モデムの能力を確かめる。** モデムを初めて使うときに 1 回実行します。SIM の認証を 1 回使います。
   ```bash
   sudo simwifi probe
   ```
   `MBIM AKA` と `UICC LLA` のどちらかが `supported` なら、simwifi で使えます。

3. **ID を確かめる。** 認証サーバーに登録されている加入者の ID と一致しているか確かめます。
   ```bash
   sudo simwifi identity --method aka
   ```

4. **接続する。**
   ```bash
   sudo simwifi connect --iface wlan0 --ssid corp-wifi --method aka
   ```
   `connected` のログが出たら接続できています。`connect` は前面で動き続けるので、Ctrl-C で切断して終了します。IP アドレスの取得は simwifi では行いません（[7.2](#72-dhcp-で-ip-アドレスを取得する) を参照）。

## 2. 共通オプション

`status` / `probe` / `identity` / `connect` で使えます。

| オプション | 既定 | 説明 |
|---|---|---|
| `--modem N` | なし | 使うモデム。ModemManager のモデム番号（`mmcli -L` の末尾の数字）か、D-Bus のオブジェクトパス（`/org/freedesktop/ModemManager1/Modem/0`）。モデムが 1 台なら省略でき、複数あるときは必須 |
| `--iface IFACE` | `wlan0` | 無線 LAN のインターフェース |
| `--json` | オフ | 結果を JSON で標準出力に出す（`status` と `probe` だけ） |
| `--log-file PATH` | なし | 標準エラーに加えて、JSON 形式のログを PATH に追記する。ファイルは 0600 で作る |
| `-v` | オフ | デバッグログ。RAND / AUTN、wpa_supplicant の状態遷移、EAP のイベントなどを出す |
| `-vv` | オフ | トレースログ。MBIM と D-Bus のメッセージの中身も出す |
| `--log-imsi` | オフ | ログと出力で IMSI をマスクしない |

> モデム番号は ModemManager が付け直すことがあります（モデムの抜き挿しやスロットの切り替えのあと）。複数台を固定して使う場合は、`mmcli -L` で確かめてから指定してください。

## 3. status — 前提条件の確認

```
simwifi status [--iface wlan0] [--modem N] [--method aka|akap] [--realm REALM] [--json]
```

simwifi が動くための条件を 1 つずつ確かめます。読み取りだけで、SIM の認証もしません。

| オプション | 既定 | 説明 |
|---|---|---|
| `--method aka\|akap` | `aka` | wpa_supplicant が対応しているかを確かめる EAP メソッドと、表示する ID の種類 |
| `--realm REALM` | SIM から生成 | ID の realm を置き換えて確かめる（[5](#5-identity--idnaiの表示) を参照） |

表示の記号は、`✔` が満たしている、`✘` が満たしていない（致命的）、`·` が参考情報です。

| 項目 | 確かめること |
|---|---|
| `root` | root で実行しているか |
| `ModemManager` | ModemManager が動いているか |
| `modem` | モデムが 1 台に決まるか（複数なら `--modem` が必要）。状態が `failed` でないか |
| `MBIM port` | モデムに MBIM のポート（`/dev/cdc-wdm*`）があるか。無ければ MBIM モードになっていない |
| `SIM slots` | スロットごとの SIM と、どれがアクティブか（参考） |
| `SIM lock` | SIM が PIN でロックされていないか（PIN2 は問題にしない） |
| `SIM` | アクティブなスロットに SIM があり、IMSI と MCC/MNC を読めるか |
| `identity` | ID（NAI）を生成できるか |
| `mbim-proxy` | `mbim-proxy` 経由でモデムと話せるか |
| `probe` | 保存されている `probe` の結果（参考。どちらの経路も非対応なら致命的） |
| `wlan` | インターフェースがあり、nl80211 の無線 LAN か |
| `NetworkManager` | NetworkManager がそのインターフェースを管理していないか |
| `wpa_supplicant` | wpa_supplicant が D-Bus で使えるか |
| `EAP methods` | wpa_supplicant が `--method` の EAP メソッドに対応しているか |
| `Interworking/HS20` | wpa_supplicant の Interworking / Hotspot 2.0 の対応（参考） |
| `wpa interface` | そのインターフェースを、ほかのクライアントが wpa_supplicant で使っていないか |
| `lock` | 同じインターフェースで、ほかの simwifi が動いていないか |

致命的な項目が 1 つでもあれば終了コード 2、すべて満たしていれば 0 です。`--json` のときは次の形で出力します（終了コードは同じ）。

```json
{
  "ok": true,
  "checks": [
    { "name": "root", "ok": true, "detail": "running as root" },
    { "name": "SIM slots", "ok": true, "info": true, "detail": "1: 44010…3421 (active)" }
  ]
}
```

満たしていない項目には `"fatal": true` が付きます。

## 4. probe — モデムの能力の確認

```
simwifi probe [--modem N] [--json]
```

simwifi が USIM に届く 2 つの経路を、モデムが使えるかどうか確かめます。

- **MBIM AKA**: MBIM の Auth サービスの `AKA` コマンド。simwifi が主に使う経路
- **UICC LLA**: MS UICC Low-Level Access。論理チャネルを開き、USIM に APDU を直接送る経路

MBIM AKA は、わざと正しくない AUTN を 1 回送って応答を見ます。USIM はこれを拒否するだけなので、SQN などの SIM の状態は変わりません。ただし、SIM の認証を 1 回使います。

```
modem:     PIX-MT200e (PIX-MT200e_MB_V1.01)
device:    /dev/cdc-wdm0
MBIM AKA:  supported (auth-incorrect-autn)
UICC LLA:  unsupported (ms-select-failed)
  ATR:              3b9f…
  APPLICATION_LIST: false
  USIM AID:         a0000000871002
```

- どちらかが `supported` なら使えます。両方が `unsupported` なら、最後に「This modem cannot be used by simwifi」と表示します
- 括弧の中は、判定の根拠にしたモデムの応答（MBIM の Status）です
- 結果は `/run/simwifi/probe-<IMEI>.json` に保存します。`status` はこれを表示し、`connect` は MBIM AKA が非対応と記録されていれば最初から UICC 経路を使います。`/run` は再起動で消えるので、再起動後は必要に応じて実行し直してください
- `probe` が見るのはダミーの値への応答だけです。正しいチャレンジに正しく答えられるかは、実際に接続しないとわかりません

## 5. identity — ID（NAI）の表示

```
simwifi identity [--modem N] [--method aka|akap] [--realm REALM]
```

SIM の IMSI から、EAP の永久 ID（3GPP TS 23.003 の NAI）を作って標準出力に出します。`connect` が EAP-Response/Identity で送るのと同じ値です。この出力では IMSI をマスクしません。

```
<先頭の文字><IMSI>@wlan.mnc<MNC 3 桁>.mcc<MCC>.3gppnetwork.org
```

| オプション | 既定 | 説明 |
|---|---|---|
| `--method aka\|akap` | `aka` | 先頭の文字を決める。EAP-AKA は `0`、EAP-AKA' は `6` |
| `--realm REALM` | SIM から生成 | `@` から後ろを REALM に置き換える（先頭の文字と IMSI はそのまま） |

- MCC と MNC は、ModemManager が SIM から読んだ事業者コード（MCC + MNC）から取ります。2 桁の MNC は 3 桁にゼロ埋めします（`10` → `010`）
- 事業者コードが読めない（SIM の読み込みが終わっていないなど）か、IMSI と一致しない場合は、`--realm` を指定するよう求めて終了します。IMSI だけでは MNC が 2 桁か 3 桁かを決められないためです
- 永久 ID は平文で送られます。疑似 ID や高速再認証 ID は wpa_supplicant が扱い、simwifi は保存しません（毎回、永久 ID から始めます）

## 6. connect — 接続

```
simwifi connect --ssid SSID [--iface wlan0] [options]
```

### 6.1 オプション

| オプション | 既定 | 説明 |
|---|---|---|
| `--ssid SSID` | （必須） | 接続先の SSID |
| `--method aka\|akap` | `aka` | EAP-AKA か EAP-AKA' か |
| `--realm REALM` | SIM から生成 | ID の realm を置き換える（[5](#5-identity--idnaiの表示)） |
| `--auth-path auto\|aka\|uicc` | `auto` | USIM への経路（[6.3](#63---auth-path)） |
| `--wpa3` | オフ | 鍵管理を `WPA-EAP-SHA256` にし、PMF を必須にする（[6.4](#64---wpa3)） |
| `--timeout 秒` | 60 | 最初の接続を待つ秒数。過ぎたら終了コード 4 で終わる |
| `--max-auth-failures N` | 3 | 認証が N 回続けて失敗したら、終了コード 3 で終わる |
| `--exec-up CMD` | なし | 接続したときに実行するシェルコマンド（[6.5](#65-フック--exec-up----exec-down)） |
| `--exec-down CMD` | なし | 切断したときに実行するシェルコマンド |
| `--sim-slot N` | 今のスロット | SIM スロット N がアクティブであることを求める（[6.6](#66-sim-スロット)） |
| `--switch-slot` | オフ | スロット N がアクティブでなければ切り替える。`--sim-slot` と一緒に使う |
| `--daemon` | — | 未実装。指定するとエラーになる。常駐には systemd を使う（[8](#8-systemd-で常駐させる)） |

共通オプション（[2](#2-共通オプション)）も使えます。`--json` は `connect` では意味を持ちません。

### 6.2 connect の動き

1. インターフェースごとのロック（`/run/simwifi/<iface>.lock`）を取ります。同じインターフェースで 2 つ目の simwifi は起動できません
2. `status` と同じ確認をします。満たしていない項目があれば、項目ごとに `prerequisite check failed` のログ（`check` と `detail`）を出し、終了コード 2 で終わります
3. `--sim-slot` があれば、スロットを確かめます（必要なら切り替えます）
4. wpa_supplicant にインターフェースを作らせ（設定ファイルは `/run/simwifi/wpa-<iface>.conf`）、ネットワークを登録して接続を始めます。前回の simwifi が異常終了して残したインターフェースは回収して使います。ほかのクライアントが使っているインターフェースには手を出さず、終了コード 2 で終わります
5. wpa_supplicant から USIM への認証の要求が届くたびに、モデムの SIM に答えさせます
6. 接続が完了したら `connected` のログを出し、`--exec-up` を実行します

接続したあとも simwifi は動き続けます。

- 電波が途切れるなどして切断すると `connection lost` のログを出し、`--exec-down` を実行します。つなぎ直しは wpa_supplicant が自動で行い、また接続したら `--exec-up` を実行します
- 接続中の再認証や鍵の更新は切断とみなしません（フックは動きません）
- `--timeout` が効くのは最初の接続までです。一度接続したあとは、切断が続いても時間切れでは終わりません
- 認証の失敗は、EAP のやり取り 1 回につき 1 回と数えます。接続に成功すると数え直します

次のときに終了します。

| きっかけ | 終了コード |
|---|---|
| Ctrl-C（SIGINT）か SIGTERM | 0 |
| 最初の接続が `--timeout` 秒以内に完了しない | 4 |
| 認証が `--max-auth-failures` 回続けて失敗した | 3 |
| モデムが抜かれた | 2 |
| wpa_supplicant が終了した | 1 |

終了するときは、接続中なら `--exec-down` を実行し、登録したネットワークと、自分が作った wpa_supplicant のインターフェースを削除します。wpa_supplicant 自体は止めません。

### 6.3 --auth-path

| 値 | 動き |
|---|---|
| `auto` | MBIM AKA を使い、使えなければ UICC 経路に切り替える。おすすめ |
| `aka` | MBIM AKA だけを使う |
| `uicc` | UICC 経路だけを使う |

`auto` の詳しい動き:

- MBIM AKA が非対応と返したら、同じチャレンジを UICC 経路で送り直します
- MBIM AKA がチャレンジを拒否したら、同じチャレンジを UICC 経路で確かめます。UICC 経路で受理されたら、MBIM AKA は当てにならないとみなし、以後は UICC 経路を使います（正しいチャレンジも拒否するモデムがあるため）
- 一度切り替えたら、そのプロセスが終わるまで UICC 経路を使い続けます
- `probe` の結果で MBIM AKA が非対応とわかっていれば、最初から UICC 経路を使います

Qualcomm 系のモデムは、MBIM AKA の値をバイトの逆順で扱います。simwifi はどちらの順で通じるかを最初の認証で判別し、以後はその順を使います（`MBIM AKA byte order detected` のログ）。どの値でも設定は不要です。

`aka` や `uicc` は、経路ごとの動きを切り分けるときに使います。

### 6.4 --wpa3

鍵管理を `WPA-EAP-SHA256` にし、PMF（802.11w）を必須にします。WPA3-Enterprise（192 ビットモードではないもの）の AP に接続するときに使います。

- AP が `WPA-EAP-SHA256` を提供していないと接続できません
- wpa_supplicant かドライバが `WPA-EAP-SHA256` に対応していないと報告した場合は、警告を出したうえで試みます
- 192 ビットモード（`WPA-EAP-SUITE-B-192`）には対応していません

### 6.5 フック（--exec-up / --exec-down）

`--exec-up` は接続したとき、`--exec-down` は切断したとき（終了時の切断を含む）に、`/bin/sh -c` で実行します。

- 次の環境変数を渡します

  | 変数 | 値 |
  |---|---|
  | `SIMWIFI_EVENT` | `up` か `down` |
  | `SIMWIFI_IFACE` | インターフェース名 |
  | `SIMWIFI_SSID` | SSID |

- `$PPID` は simwifi のプロセス ID です
- フックは 1 つずつ順番に実行します。SIM への要求の処理は止めません
- 1 回の実行の上限は 30 秒です。過ぎたら止めます。長く動き続けるプログラムは、自分でバックグラウンドに回るものを使うか、`&` で起動してください
- フックの標準出力と標準エラーは、simwifi の標準エラーに出ます
- フックが失敗してもログに残すだけで、simwifi は動き続けます
- 終了するときは、実行中や実行待ちのフックが終わるまで待ちます

### 6.6 SIM スロット

MBIM の認証にはスロットを指定する方法が無く、いつもアクティブなスロットの SIM が使われます。

- `--sim-slot N` は「スロット N がアクティブであること」を求めます。アクティブでなければ終了コード 2 で終わります
- `--switch-slot` を付けると、ModemManager にスロット N への切り替えを頼みます。モデムの再起動に相当し、モバイルデータ通信は切れます。simwifi はモデムが戻ってくるのを最大 60 秒待ちます
- ModemManager がスロット N を空きと報告している場合は、`--switch-slot` があっても切り替えません。空きスロットに切り替えると、電源を入れ直すまでモデムが使えなくなった例があるためです
- スロットが 1 つのモデムで受け付けるのは `--sim-slot 1` だけです
- 「モバイルデータはスロット 1、Wi-Fi の認証はスロット 2」のような使い分けはできません

## 7. 使い方の例

以下では、インターフェースを `wlan0`、SSID を `corp-wifi` としています。

### 7.1 接続できるかだけ確かめる

接続したら自分に SIGTERM を送り、すぐに終わらせます。うまくいけば終了コード 0、認証に失敗すれば 3、時間切れなら 4 です。

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --timeout 30 --exec-up 'kill -TERM $PPID'
```

`$PPID` は sh から見た親（simwifi）です。展開されないように、シングルクォートで囲みます。

### 7.2 DHCP で IP アドレスを取得する

simwifi は IP アドレスを設定しないので、フックで DHCP クライアントを動かします。Debian 13 の標準の DHCP クライアントは `dhcpcd`（`dhcpcd-base` パッケージ）です。

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi \
  --exec-up 'dhcpcd -b wlan0' \
  --exec-down 'dhcpcd -k wlan0'
```

- `dhcpcd -b` はすぐにバックグラウンドに回るので、フックの 30 秒の上限にかかりません
- `dhcpcd -k` はアドレスを返して dhcpcd を止めます
- `isc-dhcp-client` を入れている場合は `--exec-up 'dhclient -1 -nw wlan0' --exec-down 'dhclient -r wlan0'` が使えます

### 7.3 固定の IP アドレスを使う

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi \
  --exec-up 'ip addr add 192.0.2.10/24 dev wlan0 && ip route add default via 192.0.2.1 dev wlan0 metric 600' \
  --exec-down 'ip addr flush dev wlan0'
```

`metric` は、有線などほかの経路を優先させたいときに大きめにします。

### 7.4 フックをスクリプトにまとめる

`SIMWIFI_EVENT` で分岐すれば、1 つのスクリプトを両方のフックに使えます。

```sh
#!/bin/sh
# /usr/local/sbin/simwifi-hook
case "$SIMWIFI_EVENT" in
up)
	logger -t simwifi "connected to $SIMWIFI_SSID on $SIMWIFI_IFACE"
	dhcpcd -b "$SIMWIFI_IFACE"
	;;
down)
	logger -t simwifi "disconnected from $SIMWIFI_SSID on $SIMWIFI_IFACE"
	dhcpcd -k "$SIMWIFI_IFACE"
	;;
esac
```

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi \
  --exec-up /usr/local/sbin/simwifi-hook --exec-down /usr/local/sbin/simwifi-hook
```

### 7.5 EAP-AKA' や WPA3-Enterprise で接続する

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --method akap
```

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --method akap --wpa3
```

EAP-AKA' では、サーバーが送る AUTN の AMF で分離ビット（0x8000）が立っていないと、wpa_supplicant が認証を拒否します（[11](#11-トラブルシュート)）。

### 7.6 realm を指定する

認証サーバーが 3GPP 形式以外の realm を求める場合や、SIM から MCC/MNC を読めない場合に使います。

```bash
sudo simwifi identity --method aka --realm wlan.example.net
```

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --realm wlan.example.net
```

`status` にも同じ `--realm` を付ければ、その realm で ID を生成できるかを確かめられます。

### 7.7 モデムが複数あるとき

モデムが複数あるのに `--modem` を付けないと、`status` は `multiple modems found; specify --modem (0: EG25-G, 1: PIX-MT200e)` のように番号と機種を表示します。`mmcli -L` でも確かめられます。

```bash
mmcli -L
```

```bash
sudo simwifi status --iface wlan0 --modem 1
```

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --modem 1
```

### 7.8 2 つ目の SIM スロットを使う

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --sim-slot 2 --switch-slot
```

切り替えるとモバイルデータ通信が切れます。モデムによってはスロットの切り替えで不安定になることがあるので、[docs/COMPAT.md](COMPAT.md) で機種ごとの癖を確かめてください。

### 7.9 スクリプトから前提条件を確かめる

`status` は、致命的な項目があれば終了コード 2 を返します。

```bash
sudo simwifi status --iface wlan0 --json | jq -r '.checks[] | select(.fatal) | "\(.name): \(.detail)"'
```

```bash
if sudo simwifi status --iface wlan0 >/dev/null; then echo ready; fi
```

### 7.10 うまくいかないときの調べ方

デバッグログをファイルにも残して接続します。

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi -v --log-file /tmp/simwifi.log
```

EAP のやり取りの中身は wpa_supplicant 側のログでわかります。たとえば、EAP-AKA' の AMF の問題は wpa_supplicant のログにだけ出ます。

```bash
sudo journalctl -u wpa_supplicant -f
```

モデムとのやり取りまで見たいときは `-vv` を使います。他人にログを渡すときは、`--log-imsi` を付けずに取ってください（IMSI がマスクされます）。

## 8. systemd で常駐させる

tar.gz に同梱の `contrib/` のファイルを使います。

| ファイル | 置き場所 |
|---|---|
| `simwifi@.service` | `/etc/systemd/system/simwifi@.service` |
| `simwifi.env.example` | `/etc/simwifi/<iface>.env` として編集する |
| `simwifi.logrotate` | `/etc/logrotate.d/simwifi` |

`/etc/simwifi/wlan0.env` の例:

```sh
SSID=corp-wifi
METHOD=akap
EXEC_UP=dhcpcd -b wlan0
EXEC_DOWN=dhcpcd -k wlan0
SIMWIFI_OPTS=--wpa3 --timeout 120
```

- `SSID`、`EXEC_UP`、`EXEC_DOWN` は 1 語として渡されるので、空白を含めて構いません（引用符は付けません）
- `SIMWIFI_OPTS` は空白で区切られます。値に空白を含むオプションはここに書けません

```bash
sudo systemctl enable --now simwifi@wlan0
```

```bash
journalctl -u simwifi@wlan0 -f
```

- インスタンス名（`@` の後ろ）がインターフェース名になります。インターフェースごとに env ファイルを用意すれば、複数を同時に動かせます（モデムはそれぞれ別にし、`--modem` で指定してください）
- ログは journald に加えて、`/var/log/simwifi/<iface>.log`（JSON）にも出ます
- `Restart=on-failure` なので、認証の失敗やモデムの抜去などで終わったときは 10 秒後に起動し直します。`systemctl stop` による終了（終了コード 0）では起動し直しません
- `ProtectSystem=strict` で、ファイルシステムは `/run/simwifi` と `/var/log/simwifi` 以外が読み取り専用になります。フックがファイルを書く場合（DHCP のリース、`/etc/resolv.conf` の書き換えなど）は、`sudo systemctl edit simwifi@wlan0` で `[Service]` に `ReadWritePaths=` を追加してください。必要な場所は DHCP クライアントや DNS の設定によって変わります
- フックで起動したプロセス（dhcpcd など）は、サービスの停止時に一緒に止まります

## 9. 終了コード

| コード | 意味 |
|---|---|
| 0 | 成功（`connect` 中の Ctrl-C / SIGTERM による終了を含む） |
| 1 | 使い方の誤り、内部エラー、wpa_supplicant の消失 |
| 2 | 前提条件を満たしていない、同じインターフェースで別の simwifi が動いている、モデムが抜かれた |
| 3 | 認証に失敗した |
| 4 | タイムアウト |

## 10. ログ

- ログは標準エラーに出します。標準出力は、`status` / `probe` / `identity` の結果だけに使います
- `--log-file` を付けると、同じ内容を JSON 形式でもファイルに追記します。ローテーションには `contrib/simwifi.logrotate`（`copytruncate`）を使います
- 各行には `component`（`cli` / `modem` / `mbim` / `simauth` / `supplicant`）が付きます
- メッセージは英語です

| 値 | 扱い |
|---|---|
| RES / CK / IK / AUTS | 出力しない（`-vv` でも長さだけ） |
| RAND / AUTN | `-v` 以上で出力する（秘密の値ではない） |
| IMSI | マスクする（`44010…3421`）。`--log-imsi` で全桁 |
| ICCID | 先頭 4 桁と末尾 4 桁だけ |
| NAI | IMSI の部分をマスクする |

## 11. トラブルシュート

README の「[トラブルシュート](../README.ja.md#トラブルシュート)」に加えて:

| 症状 | 確認すること |
|---|---|
| `multiple modems found; specify --modem (…)` | `--modem` でモデムを指定する（[7.7](#77-モデムが複数あるとき)） |
| `modem has no MBIM port (is the modem in MBIM mode?)` | モデムを MBIM モードに切り替える（方法は機種ごとに異なる） |
| `another simwifi instance is already running on this interface` | 同じインターフェースで simwifi が動いていないか（systemd のサービスを含む） |
| `no SIM in the active slot` | SIM が挿さっているか。デュアル SIM のモデムなら、アクティブなスロットはどれか（`status` の `SIM slots`） |
| `IMSI not available (SIM not ready?)` | モデムを挿した直後は SIM の読み込みに時間がかかる。少し待ってからやり直す |
| `not connected within 1m0s` | SSID は正しいか、電波は届くか。認証サーバーまでの経路で応答が遅れていないか。必要なら `--timeout` を延ばす |
| `authentication failed 3 times in a row` | ID は登録されている加入者と一致するか（`simwifi identity`）。EAP-AKA' なら AMF の分離ビット。wpa_supplicant のログ（[7.10](#710-うまくいかないときの調べ方)） |
| `USIM rejected AUTN (network authentication failed)` | SIM が網を正しいと認めなかった。サーバー側の鍵（K / OPc）が SIM と一致しているか |
| `USIM synchronization failure; sending AUTS` | SIM とサーバーの SQN がずれている。simwifi は AUTS を返すので、サーバーが再同期に対応していれば続けて接続できる |
| `SIM slot N is not active (active: M); add --switch-slot to switch` | `--switch-slot` を付けるか、`--sim-slot` を外す |
| `SIM slot N is empty according to ModemManager; not switching` | スロット N に SIM が挿さっているか |
| `modem was removed` | モデムが抜けた、またはモデムが再起動した。systemd で動かしていれば自動で起動し直す |
