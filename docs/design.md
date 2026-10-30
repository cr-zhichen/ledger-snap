# 接口说明

ledger-snap 提供截图识别、逐笔确认、修改和删除接口。除登录外，请求均需携带 `Authorization: Bearer <token>`。

## 登录

`POST /v1/login` 接收 JSON：`account`、`password_md5`、`invite_code`，成功返回 `token`。

## 识别

`POST /v1/recognize` 接收截图作为请求体，或 multipart 的 `image` 字段。multipart 可同时提供 `client_time`（Unix 秒）。

单笔和多笔使用同一返回格式：

```json
{
  "batch_id": "b_example",
  "records": [
    {
      "record_id": "r_example",
      "status": "written",
      "kind": "支出",
      "summary": "支出 50.68 CNY",
      "money": "50.68",
      "currency": "CNY",
      "time": "2026-10-30 17:37"
    }
  ]
}
```

- `records` 按截图顺序排列；没有可记账交易时为空数组，`reason` 说明原因。
- `written` 表示已记账；`needs_confirm` 表示尚待确认，`reason` 说明原因；`rejected` 表示该笔不支持记账。
- `deleted`、`discarded` 分别表示已删除和已放弃。
- `GET /v1/batches/{id}` 返回同样格式，查询该次识别中各笔账单的当前状态。
- 一笔写入失败不会隐藏其他笔的结果。请查看每笔的 `status` 和 `reason`；`write_unconfirmed: true` 表示写入结果未确认，可以重新确认或删除。

## 逐笔操作

以下接口的 `{id}` 使用 `record_id`，返回该笔结果：

| 接口 | 请求与作用 |
| --- | --- |
| `POST /v1/records/{id}/confirm` | 确认一笔待确认账单 |
| `POST /v1/records/{id}/update` | JSON，按名称设置 `kind`、`category`、`asset`、`target_asset`，或设置 `money`、`other`、`discount`、`fee`、`time`、`remark` |
| `POST /v1/records/{id}/correct` | JSON `text`，用自然语言修改 |
| `POST /v1/records/{id}/delete` | 删除已记账记录，或放弃待确认记录；也支持 `DELETE /v1/records/{id}` |

时间格式为 `YYYY-MM-DD HH:MM`。优惠、手续费填 `0` 表示清除。`other` 表示外币消费的入账金额，或跨币种转账的转入金额。

## 账户和分类

- `GET /v1/options`：读取可选择的账户、分类和类型名称。
- `POST /v1/catalog/refresh`：刷新账户和分类。

## 快捷指令

快捷指令接口及操作说明见 [快捷指令](shortcut.md)。
