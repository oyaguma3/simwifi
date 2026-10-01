# 他の Linux ディストリビューションへの展開: 課題と検討ポイント

simwifi は Debian 13 で開発・実機検証した（v0.1.2 時点）。本書は、他のディストリビューションで動かすときの課題と検討ポイントをまとめる。

- 調査日: 2026-10-02
- 「確認済み」は、各ディストリビューションのパッケージ定義（spec / PKGBUILD / APKBUILD / Debian の設定ファイル）を実際に取得して確かめた事実。「要確認」は推測や記憶に基づくもので、実機での確認が必要

---

## 1. simwifi が前提にしているもの

simwifi 本体は静的リンクの Go バイナリで、C ライブラリにも特定のディストリビューションにも依存しない。動作の前提は、すべて外部のサービスとその設定にある。

| 前提 | 内容 | 関係する章 |
|---|---|---|
| wpa_supplicant | D-Bus（`fi.w1.wpa_supplicant1`）で動いていること。EAP-AKA / AKA' が有効なビルドであること。Interface ごとの設定ファイルで `external_sim=1` を与えられること | §3.1 |
| ModemManager | D-Bus でモデムと SIM の情報を取れること。MBIM モデムを mbim-proxy 経由で開いていること | §3.3 |
| mbim-proxy（libmbim） | 抽象 unix ソケット `@mbim-proxy` で待ち受けていること。既定では root からの接続だけを受け付ける | §3.3 |
| 無線 LAN | nl80211 ドライバで、他のソフト（NetworkManager、iwd など）が管理していないこと | §3.2 |
| 権限 | root で実行すること | §3.3、§3.6 |
| サービス化 | 付属のユニットは systemd 用。本体は systemd に依存しない（`/run/simwifi` も自分で作る） | §3.4 |
| フック | `--exec-up` / `--exec-down` は `/bin/sh -c` で実行する。例として書いた `dhclient` は環境によって無い | §3.5 |

---

## 2. 主要ディストリビューションの調査結果

### 2.1 wpa_supplicant

| ディストリビューション | 版 | EAP-AKA | EAP-AKA' | D-Bus | 起動方法 | 確認 |
|---|---|---|---|---|---|---|
| Debian 13 | 2.10 | 有効 | 有効 | 有効 | `-u -s -O "DIR=/run/wpa_supplicant GROUP=netdev"` | **実機で確認済み** |
| Ubuntu 24.04 / 22.04 | （未確認） | 有効 | 有効 | 有効 | Debian と同じ設定ファイルを使用 | 設定は確認済み |
| Fedora（rawhide） | 2.11 | 有効 | **無効** | 有効 | `-c /etc/wpa_supplicant/wpa_supplicant.conf -u`（`Type=dbus`） | 確認済み |
| CentOS Stream 9 / 10（RHEL 9 / 10 系） | 2.11 | 有効 | **無効** | 有効 | Fedora と同系統 | 確認済み |
| Arch Linux | 2.12 | 有効 | 有効 | 有効 | D-Bus activation 用のサービスファイルあり | 確認済み |
| Alpine Linux（edge） | 2.11 | 有効 | 有効 | 有効 | D-Bus activation 用のサービスファイルあり | 確認済み |
| openSUSE（Factory） | （未確認） | 有効 | 有効 | 有効 | （未確認） | 設定は確認済み |

上流の `defconfig` では EAP-AKA / AKA' はどちらも無効（コメントアウト）で、各ディストリビューションが独自に有効にしている。**Fedora と RHEL 系は EAP-AKA だけを有効にしていて、EAP-AKA' は使えない。**

### 2.2 ModemManager と libmbim

| ディストリビューション | ModemManager | libmbim | mbim-proxy の非 root 許可 | 確認 |
|---|---|---|---|---|
| Debian 13 | 1.24.0 | 1.32.0 | なし（root のみ） | **実機で確認済み** |
| Ubuntu 24.04 | 1.23.4 | 1.31.2 | （未確認） | 版は確認済み |
| Ubuntu 22.04 | 1.18.6 | 1.26.2 | （未確認） | 版は確認済み（changelog の先頭） |
| Fedora（rawhide） | 1.24.2 | 1.32.0 | なし | 確認済み |
| CentOS Stream 9 / 10 | 1.22.0 | 1.32.0 | なし | 確認済み |
| Arch Linux | 1.24.2 | 1.34.0 | なし | 確認済み |
| Alpine Linux（edge） | 1.25.95（git 版） | 1.34.0 | **`dialout` グループに許可**（`mbim_groupname=dialout`） | 確認済み |

調べた範囲では、すべて simwifi の前提（ModemManager 1.18 以降、libmbim 1.32 以降）を満たすか、それに近い。Ubuntu 22.04 の libmbim 1.26 は前提より古いが、simwifi が使う Proxy Control と AKA の仕組みは 1.10 から変わっていないので、動く見込みは高い（要確認）。

---

## 3. 課題と検討ポイント

### 3.1 wpa_supplicant

**(1) EAP-AKA' が無効なビルド（Fedora、RHEL 系）**

- 影響: `--method akap` が使えない。`simwifi status` は wpa_supplicant の `EapMethods` を調べ、AKA' が無ければ Fatal にするので、誤動作はしない
- 対応案:
  - 当面は EAP-AKA だけを使うと README に明記する
  - AKA' が必要なら、wpa_supplicant を独自にビルドする手順を用意する（COPR などで配布する案もある）

**(2) wpa_supplicant の起動方法の違い**

- simwifi は、wpa_supplicant が D-Bus で動いていれば（または D-Bus activation で起動できれば）、起動方法を問わない。Debian、Fedora、Arch、Alpine はいずれもこの条件を満たす
- 注意が必要なのは、**D-Bus を使わない、インターフェースごとの wpa_supplicant**（Arch の `wpa_supplicant@wlan0.service`、netctl、OpenRC の `/etc/init.d/wpa_supplicant` に `-i` を渡す設定など）が同じ無線 LAN を管理している場合。simwifi は D-Bus からその存在を検出できず、`CreateInterface` の失敗や、無線 LAN の取り合いになる
- 対応案: `status` に「D-Bus 以外の wpa_supplicant が同じインターフェースを使っていないか」の確認を加える（`/run/wpa_supplicant/<iface>` の制御ソケットや、プロセスの引数を調べる）

**(3) wpa_supplicant 2.11 / 2.12**

- 実機で確認したのは 2.10 だけ。外部 SIM の仕組み（`NetworkRequest` / `NetworkReply`、`UMTS-FAIL` の扱い）と D-Bus API は互換のはずだが、2.11 / 2.12 で E2E テストを通して確認する必要がある
- 2.10 の D-Bus API には `Reauthenticate` が無かった。試験の手順で使う場合は、版ごとに確認する

### 3.2 NetworkManager と iwd

**(1) NetworkManager が無線 LAN を管理しているディストリビューション**

- デスクトップ向けのほとんど（Fedora Workstation、Ubuntu Desktop、openSUSE など）は、NetworkManager がすべての無線 LAN を管理している。Raspberry Pi OS も、Bookworm 以降は NetworkManager が既定（要確認）
- 現状は `nmcli device set <iface> managed no` で管理対象から外す必要がある（`status` が検出して Fatal にする）。ノート PC の利用者にとっては不便
- 対応案: 設計書 §14.1 の「NetworkManager が所有する wpa_supplicant の Interface に相乗りし、SIM 要求だけに答える」方式。`external_sim` を有効にするために制御ソケットの `SET external_sim 1` を併用する必要があり、実現性の調査から始める

**(2) iwd を使っている環境**

- iwd 単体の環境や、NetworkManager の無線のバックエンドを iwd にしている環境（Arch で多い）では、wpa_supplicant が使われない
- iwd の EAP-SIM / AKA は oFono の SIM 機能が前提で、simwifi の仕組み（wpa_supplicant の外部 SIM）とは互換性がない（要確認）
- 対応案: 当面は「対象の無線 LAN を iwd の管理から外し、wpa_supplicant を使う」と案内する。iwd 対応は別の設計になる

### 3.3 ModemManager、libmbim、権限

**(1) mbim-proxy の接続許可**

- 既定のビルドでは root だけが接続できる。Alpine は `dialout` グループにも許可している
- simwifi は非 root も想定していない（wpa_supplicant の D-Bus も、多くのディストリビューションで root か特定のグループだけに許可されている）。Alpine で非 root 化を試す価値はあるが、wpa_supplicant 側の権限設計と合わせて考える必要がある（設計書 §14.3）

**(2) ModemManager を使わない環境**

- OpenWrt などの組み込み向けは、ModemManager ではなく `umbim` / `uqmi` を使うことが多い
- simwifi は ModemManager を必須にしている（設計書 D16）。対応するには、mbim-proxy の起動と管理、IMSI の取得（`SUBSCRIBER_READY_STATUS`）を自分で持つ必要がある（設計書 §14.4）。wpa_supplicant が OpenWrt では D-Bus 無しでビルドされることも多く、別途の検討が要る

**(3) モデム側の注意点（ディストリビューションに依らない）**

- Qualcomm 系の MBIM AKA は値を逆順で扱う。simwifi は自動で判別する
- Lenovo 向けの EM7455 などは FCC ロックがかかっている。EAP-AKA には無線が不要なので、認証には影響しないことを確認済み

### 3.4 init システム

- 付属のユニット（`contrib/simwifi@.service`）は systemd 用
- Alpine（OpenRC）、Void（runit）、Devuan（sysvinit）、OpenRC を選んだ Gentoo などでは、それぞれの起動スクリプトが必要
- simwifi 本体は systemd に依存しない（`/run/simwifi` を自分で作り、ログは標準エラーとファイルに出す）ので、スクリプトを用意すれば動く見込み
- 対応案: OpenRC 用の起動スクリプトを `contrib/` に追加する。logrotate の有無もディストリビューションで異なるので、案内を分ける

### 3.5 フックの例と周辺コマンド

- README の `--exec-up "dhclient -1 -nw wlan0"` は、ISC dhclient がある環境（Debian など）向けの例
- ディストリビューションによって、DHCP クライアントは `dhcpcd`（Arch など）、`udhcpc`（Alpine、BusyBox）、systemd-networkd、NetworkManager の内蔵クライアントなどに分かれる。RHEL 系では ISC dhclient が非推奨か削除済み（要確認）
- 対応案: README に、主要なディストリビューションごとのフックの例を載せる

### 3.6 セキュリティ機構（SELinux、AppArmor）

- **SELinux（Fedora、RHEL 系）**: simwifi が `/run/simwifi/` に書いた設定ファイルを、wpa_supplicant が SELinux の制限で読めない可能性がある（要確認）。読めない場合は `CreateInterface` が失敗する
  - 対応案: 実機で確かめたうえで、設定ファイルの置き場所を wpa_supplicant が読める場所に変える、ファイルのラベルを付け替える、SELinux のポリシーモジュールを用意する、のいずれかを選ぶ
- **AppArmor（Ubuntu、openSUSE）**: wpa_supplicant にプロファイルが適用されている場合、同様の問題が起きうる（要確認）。Debian 13 では問題がなかった

### 3.7 CPU アーキテクチャとビルド

- 配布しているのは amd64 と arm64 の静的バイナリ。32 ビットの Raspberry Pi OS（armv7）などには配布物が無い
  - 対応案: リリースに `linux/arm`（GOARM=7）を加える。`GOARCH` を足すだけで済む
- ソースからのビルドには Go 1.27 以降が必要（標準ライブラリの `uuid` と `encoding/json/v2` を使っているため）。ディストリビューションの Go は古いことが多い（Debian 13 は 1.24 系など。要確認）
  - 対応案: ディストリビューションのパッケージとして配布するなら、必要な Go の版を下げる（`uuid` と `json/v2` を置き換える）ことを検討する。静的バイナリを配布する限りは問題にならない

### 3.8 パッケージ化

- 現在の配布は tar.gz のみ
- 対応案: `.deb`（Debian / Ubuntu）、`.rpm`（Fedora / RHEL 系）、AUR（Arch）、APK（Alpine）を用意する。サービスのユニット、logrotate の設定、`/etc/simwifi/` の例を、各形式の流儀に合わせて同梱する

### 3.9 テスト

- E2E テスト（`test/e2e/run.sh`）は Debian の hostapd を前提にしている。他のディストリビューションでは、hostapd が EAP-AKA のサーバー機能と `eap_sim_db` を持つビルドかを確かめる必要がある（要確認）
- 対応案: VM で各ディストリビューションを用意し、まず SIM 無しの E2E テストを通す。そのうえで、実機（モデムと SIM）で確かめる

---

## 4. 進め方の提案

ディストリビューションごとの難しさと、確かめる順番の提案。

| 優先 | ディストリビューション | 見込み | 主な作業 |
|---|---|---|---|
| 1 | Ubuntu 24.04 | Debian とほぼ同じ構成で、容易 | NetworkManager の unmanaged 化の案内。AppArmor の確認 |
| 2 | Arch Linux | パッケージの条件は揃っている | wpa_supplicant 2.12 での確認。iwd を併用している場合の案内 |
| 3 | Fedora / RHEL 系 | EAP-AKA は動く見込み。AKA' は不可 | SELinux の確認と対応。AKA' の制限の明記 |
| 4 | Alpine Linux | パッケージの条件は揃っているが、init が OpenRC | OpenRC の起動スクリプト。`dialout` グループでの非 root 化の検討 |

まずは、各ディストリビューションの VM で `status` と SIM 無しの E2E テストを通すことから始めるのがよい。モデムを使う確認は、手元のミニ PC で OS を入れ替えるか、USB ブートで行う。

simwifi 側の改善候補（優先度の高い順）:

1. `status` に、D-Bus 以外の wpa_supplicant や iwd が同じインターフェースを使っていないかの確認を加える
2. README に、ディストリビューションごとの前提と、フックの例を載せる
3. リリースに armv7 のバイナリを加える
4. OpenRC の起動スクリプトを加える
5. SELinux への対応（実機で確かめてから）
