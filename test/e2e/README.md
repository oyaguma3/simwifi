# E2E テスト（SIM・ドングル・実 AP 不要）

`mac80211_hwsim` の 2 つの仮想無線で AP と STA を作り、wpa_supplicant との連携を実機の Debian 13 上で確かめる（DESIGN §12.2）。

```
[hwsim radio 0] hostapd（EAP サーバー内蔵）── eap_sim_db ── hlrgw（Milenage、hlr_auc_gw 代替）
      ⇅ 仮想の無線
[hwsim radio 1] wpa_supplicant ── D-Bus ── simwifi-e2e（--auth-backend milenage）
```

## 必要なもの

- Debian 13 の実機（WSL2 のカーネルには `mac80211_hwsim` が無い）
- パッケージ: `hostapd`、`wpasupplicant`（`wpa_supplicant.service` が動いていること）、`iw`
- WSL2 でビルドしたバイナリ: `make build-e2e` → `bin/simwifi-e2e`、`bin/hlrgw`

## 実行

```bash
sudo SIMWIFI=/path/to/simwifi-e2e HLRGW=/path/to/hlrgw ./run.sh
```

## シナリオ

| 名前 | 内容 | 期待する終了コード |
|---|---|---|
| aka | EAP-AKA で接続 | 0 |
| akap | EAP-AKA' で接続（加入者の AMF は分離ビット ON） | 0 |
| resync | USIM 側の SQN が網より進んでいる → AUTS で再同期して接続 | 0 |
| wrong-key | K が違う → Authentication-Reject が続く | 3 |
| after-crash | `kill -9` された simwifi の残骸 Interface を回収して接続 | 0 |
| wpa3 | `WPA-EAP-SHA256` + PMF 必須で接続 | 0 |

接続できたら `--exec-up 'kill -TERM $PPID'` で simwifi 自身に SIGTERM を送り、終了コード 0 で終わらせている。ログは `/tmp/simwifi-e2e.*` に残る。

## 注意

- 実行中は `mac80211_hwsim` を読み込み、終了時に外す。既に読み込まれている場合は中止する
- NetworkManager が動いていれば、hwsim の 2 つのインターフェースを unmanaged にする
- 加入者データは 3GPP TS 35.208 Test Set 19（hostap の `hlr_auc_gw.milenage_db` と同じ）
