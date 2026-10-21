# 快捷指令搭建步骤

在 iPhone「快捷指令」App 里新建一个快捷指令，命名为「截图记账」，按下面的顺序添加动作。动作名称以 iOS 中文界面为准。

运行结束后的效果：弹出识别结果，选项「确认 / 修改 / 删除」。「修改」里可以直接选账户、收支类型、分类，改金额，或者用一句话改；改完再弹一次结果，直到点确认或删除。

有拿不准的字段时，服务端先不记账，返回 `needs_confirm`，结果里会说明哪几项拿不准（带问号）。点「确认」才入库；修改后也要再点一次「确认」。

## 安装时要填的两项

在快捷指令的「详细信息 › 设置导入问题」里添加两个问题，分享给朋友后，他们安装时会被要求填写：

| 问题 | 对应的动作 |
| --- | --- |
| 服务器地址，例如 `https://snap.example.com` | 第 1 步的「文本」 |
| 邀请码 | 第 2 步的「文本」 |

## 约定

下文的“调用接口”都指「获取 URL 内容」，方法 POST，头部 `Authorization` = `Bearer ` + `令牌`；需要带参数时，请求体用 JSON。
每次调用后，用「获取词典值」从结果取出 `status`、`summary`、`reason`、`kind`、`record_id`、`message`，覆盖同名变量。
`status` 是 `error` 时，「显示提醒」`message`，不覆盖变量。

## 动作

### 读取配置和登录凭据

1. 「文本」：服务器地址 → 「设置变量」`服务器`
2. 「文本」：邀请码 → 「设置变量」`邀请码`
3. 「获取文件」：从 iCloud 云盘的 `Shortcuts/ledger-snap-token.txt` 获取，关闭「找不到文件时报错」 → 「设置变量」`令牌`

### 首次运行时登录

4. 「如果」`令牌`「没有任何值」：
   1. 「要求输入」文本，提示“钱迹账号” → 「设置变量」`账号`
   2. 「要求输入」文本，提示“钱迹密码” → 「生成哈希值」，类型 MD5 → 「设置变量」`密码MD5`
   3. 「获取 URL 内容」：POST `服务器/v1/login`，JSON：`account` = `账号`、`password_md5` = `密码MD5`、`invite_code` = `邀请码`
   4. 「获取词典值」`token`。如果没有值：「获取词典值」`message` →「显示提醒」→「停止此快捷指令」
   5. 「存储文件」：把 `token` 存到 iCloud 云盘 `Shortcuts/ledger-snap-token.txt`，打开「覆盖」 → 「设置变量」`令牌`
5. 「结束如果」

### 截图并识别

6. 「截屏」
7. 「转换图像」为 JPEG
8. 调用接口 `服务器/v1/recognize`。请求体改用**表单**，字段 `image`，类型为文件，值为上一步的图像。
9. 取出结果变量（见“约定”）。另外取出 `code`：如果是 `auth_required`，先「删除文件」`Shortcuts/ledger-snap-token.txt`（关闭“确认删除”），再「显示提醒」`message`，然后「停止此快捷指令」
10. 「如果」`status` 是 `rejected`：「显示通知」`reason` →「停止此快捷指令」

### 确认、修改、删除

11. 「重复」10 次，每次：
    1. 「文本」：`status` 是 `needs_confirm` 时为 `reason` + 换行 + `summary`，否则为 `summary`（用「如果」拼出）→ 「设置变量」`提示`
    2. 「从菜单中选取」，提示为 `提示`，选项：
       - **确认**：
         - 「如果」`status` 是 `needs_confirm`：调用接口 `服务器/v1/records/` + `record_id` + `/confirm`，取出结果变量；「如果」`status` 是 `written`：「显示通知」`summary` →「停止此快捷指令」
         - 「否则」：「停止此快捷指令」
       - **修改**：见第 12 步
       - **删除**：调用接口 `服务器/v1/records/` + `record_id` + `/delete` →「显示通知」“已删除” →「停止此快捷指令」

12. 「修改」分支里：
    1. 调用接口 `服务器/v1/options`，方法改成 **GET**，结果存为 `选项`
    2. 「从菜单中选取」，提示“改什么？”，选项：
       - **账户**：
         - 「如果」`kind` 是 `转账` 或 `信用卡还款`：从 `选项` 的 `assets` 里「从列表中选取」两次，提示“转出账户”“转入账户”；调用接口 `…/records/` + `record_id` + `/update`，JSON：`asset` = 转出账户，`target_asset` = 转入账户
         - 「否则」：从 `选项` 的 `assets_or_none` 里「从列表中选取」，提示“账户”；调用 `/update`，JSON：`asset` = 所选账户
       - **收支类型**：从 `选项` 的 `kinds` 里「从列表中选取」，然后：
         - 选了 `支出`：从 `expense_categories` 里选分类，调用 `/update`，JSON：`kind`、`category`
         - 选了 `收入`：从 `income_categories` 里选分类，调用 `/update`，JSON：`kind`、`category`
         - 选了 `转账` 或 `信用卡还款`：从 `assets` 里选转出、转入账户，调用 `/update`，JSON：`kind`、`asset`、`target_asset`
       - **分类**：
         - `kind` 是 `支出` 时从 `expense_categories` 里选，是 `收入` 时从 `income_categories` 里选，调用 `/update`，JSON：`category`
         - 其他情况：「显示提醒」“转账和还款没有分类”
       - **金额**：「要求输入」数字，提示“金额” → 调用 `/update`，JSON：`money`
       - **用一句话改**：「要求输入」文本，提示“怎么改？例如：改成打车，用支付宝，昨天的” → 调用 `…/records/` + `record_id` + `/correct`，JSON：`text`
       - **刷新账户和分类**：在钱迹里新建了账户或分类后用。调用 `服务器/v1/catalog/refresh` →「显示通知」结果里的 `message`，然后回到这一步重新取 `选项`
    3. 取出结果变量，回到第 11 步的循环，显示新的结果

## 触发方式

打开「设置 › 辅助功能 › 触控 › 轻点背面 › 轻点两下」，选择「截图记账」。iPhone 15 Pro 及以上也可以在「设置 › 操作按钮」里绑定。

必须停在支付结果页时触发：「截屏」截的是当时的屏幕。在快捷指令 App 里点运行，截到的是快捷指令自己的界面。

## 接口

| 接口 | 用途 |
| --- | --- |
| `POST /v1/login` | 登录，返回 `token` |
| `POST /v1/recognize` | 上传截图并识别、写入 |
| `GET /v1/options` | 选择器用的名称：`kinds`、`expense_categories`、`income_categories`、`assets`、`assets_or_none` |
| `POST /v1/catalog/refresh` | 立即从钱迹重新拉取账户和分类，返回 `message` |
| `POST /v1/records/{id}/update` | 按名称修改：`kind`、`category`、`asset`、`target_asset`、`money`、`time`（`2006-01-02 15:04`）、`remark` |
| `POST /v1/records/{id}/correct` | 用一句话修改：`text` |
| `POST /v1/records/{id}/confirm` | 写入一条 `needs_confirm` 的记录 |
| `POST /v1/records/{id}/delete` | 删除 |

返回字段：

| 字段 | 说明 |
| --- | --- |
| `status` | `written` 已写入；`needs_confirm` 未写入，等你确认（有拿不准的字段、疑似重复，或转账缺账户）；`rejected` 不记账；`error` 出错 |
| `kind` | `支出`、`收入`、`转账`、`信用卡还款` |
| `summary` | 例如“支出 25.00 · 三餐 · 招商银行信用卡(1234) · 10-21 12:31 · 瑞幸咖啡”，转账为“转账 1000.00 · 微信零钱 → 招商银行储蓄卡(5678) · …”；拿不准的字段后面带问号 |
| `reason` | 未写入或不记账的原因 |
| `record_id` | 修改、确认、删除时用 |
| `code` | 出错时可能出现；`auth_required` 表示需要重新登录 |
| `message` | 出错时给人看的说明 |

改成转账或还款时，必须同时给出转出和转入账户，否则接口返回错误，原账单保持不变。
