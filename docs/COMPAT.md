# 実機互換表

`simwifi probe` と実機テストの結果を記録する（DESIGN §12.3）。

| 製品 | モジュール | FW | MBIM AKA | UICC Low-Level Access | APPLICATION_LIST | MAC 不正時の応答 | 実 SIM での接続 | 確認日 |
|---|---|---|---|---|---|---|---|---|
| Soracom Onyx | Quectel EG25-G | EG25GGBR07A08M2G | 対応 | 対応（部分 AID で選択） | 非対応 | Status `AUTH_INCORRECT_AUTN` (35) | 未確認（AP 準備中） | 2026-10-02 |

## 機種ごとのメモ

### Quectel EG25-G（Soracom Onyx）

- ModemManager の plugin は `quectel`、ドライバは `cdc_mbim` + `option`。MBIM ポートは `cdc-wdm0`
- SIM スロットは 1 つ（MM の `PrimarySimSlot` / `SimSlots` は空）
- ATR: `3b9f96801fc78031e073fe2117574a3382033339001e`
- 同期失敗（AUTS）がどちらの形式で返るか（Status `AUTH_SYNC_FAILURE` か、成功 + `ResLen 0`）は、AP を使う実機テストで確認する
