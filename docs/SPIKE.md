# 実機での前提検証（M0 スパイク）

開発プラン（docs/PLAN.md）M0 の S1〜S6 の手順と結果の記録。ミニ PC（Debian 13）で実行する。

| # | 内容 | 状態 |
|---|---|---|
| S1 | D-Bus の `NetworkRequest(path, "SIM", "UMTS-AUTH:…")` | ソース確認済み（DESIGN §16.3）。実機は E2E（test/e2e/run.sh）で確認する |
| S2 | Debian 13 の wpa_supplicant が AKA / AKA' に対応 | **未実施** |
| S3 | mbim-proxy 経由の疎通、非 root の拒否 | **未実施** |
| S4 | ドングルの `AKA` / UICC Low-Level Access の対応状況 | **未実施**（`simwifi probe` で確認） |
| S5 | UUID 定数と Proxy Control の形式 | 完了（libmbim ソース。DESIGN §16.4） |
| S6 | 外部 SIM 応答 `UMTS-FAIL` で Authentication-Reject | 完了（hostap ソース。DESIGN §16.3） |

## 事前準備

```bash
sudo apt install modemmanager libmbim-utils wpasupplicant hostapd iw rfkill
```

WSL2 側でビルドして転送する（ミニ PC が arm64 なら `GOARCH=arm64` を付ける）。

```bash
make build build-e2e && scp bin/simwifi bin/simwifi-e2e bin/hlrgw simwifi-dut:
```

## S2: wpa_supplicant の EAP メソッド

```bash
busctl get-property fi.w1.wpa_supplicant1 /fi/w1/wpa_supplicant1 fi.w1.wpa_supplicant1 EapMethods
```

`AKA` と `AKA'` が含まれていれば OK。含まれていなければ、wpa_supplicant を自前でビルドする必要がある。

結果:

## S3: mbim-proxy

```bash
mmcli -L
mmcli -m 0 | grep -i -E "ports|primary port"
sudo mbimcli -p -d /dev/cdc-wdm0 --query-device-caps
mbimcli -p -d /dev/cdc-wdm0 --query-device-caps   # 非 root: 応答なしで失敗するはず
sudo ./simwifi status
```

結果:

## S4: ドングルの能力

```bash
sudo mbimcli -p -d /dev/cdc-wdm0 --ms-query-uicc-application-list
sudo ./simwifi probe -vv
```

`probe` は AKA を 1 回ダミー値で実行する（SIM の認証を 1 回使う）。結果は `/run/simwifi/probe-<IMEI>.json` に保存され、`docs/COMPAT.md` に転記する。

結果:

## E2E（SIM なし）

```bash
sudo SIMWIFI=./simwifi-e2e HLRGW=./hlrgw ./test/e2e/run.sh
```

結果:
