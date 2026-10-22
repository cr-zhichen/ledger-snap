# ledger-snap

iPhone 上轻点背面截图，服务端识别截图里的交易并写入钱迹。设计见 [docs/design.md](docs/design.md)，快捷指令的生成、安装和使用见 [docs/shortcut.md](docs/shortcut.md)。

## 安装快捷指令

在 iPhone 上用 Safari 打开下面的链接，下载后点开文件，按提示添加；第一次运行时填写服务器地址、邀请码和钱迹账号：

**[下载快捷指令（ledger-snap.shortcut）](https://github.com/cr-zhichen/ledger-snap/releases/latest/download/ledger-snap.shortcut)**

GitHub 的附件名不能用中文，所以添加后快捷指令叫“ledger-snap”，可以在快捷指令 App 里改名为“截图记账”。触发方式和使用方法见 [docs/shortcut.md](docs/shortcut.md)。

## 流程

1. 快捷指令截屏，上传到服务端。
2. 服务端调 OCR（火山多语种 OCR、谷歌或百度），再用一个快的文本模型（默认 Cerebras 上的 Qwen3.8 27B）把文字行整理成“标签 + 值”：标签和值不在一行、标签在值下面、值折成几行时，都合成一行。模型只给出行号，不改文字；出错或超过 `rows.timeout` 就按文字框的位置分行。然后从整理好的文字里列出金额和时间候选。
3. 一次决策模型请求同时判断：是不是要记账的交易（已完成或待支付）、收支类型、金额及币种、时间、分类、账户、商户。外币消费和换汇按钱迹的多币种格式记账，两边金额以截图为准，截图里没有的按钱迹汇率估算并先请用户确认。默认经 OpenRouter 调用 Jev 1.14，也可以在 `config.toml` 里把 `decision.provider` 改成 `typesafe`，直接调用 TypeSafe 官方接口。
4. 判断不准时，交给视觉 LLM 兜底（默认 Claude Haiku 5.5）。
5. 写入钱迹，返回摘要；快捷指令弹出「完成 / 修改 / 从钱迹删除」，修改时可以直接选账户、收支类型、分类，改金额、时间，或用一句话改。有拿不准的字段时先不写入，标明“还没记账”，弹出「确认记账 / 修改 / 不记了」，等用户确认（或修改后确认）再入库。

服务端会把钱迹里的账单同步到本地，包括在 App 里手动记的和改过的：识别时查重复会一并比对；修改已记的账之前先同步一次，以钱迹里现在的样子为准，只改这次要改的项，不会覆盖在 App 里做的改动。识别从不等同步，修改最多等 `qianji.sync_wait`（默认 5 秒），没同步完就不改。

分类会参考同一商户以前的账（按备注和商户名对应，在 App 里改过的分类也算）：最近几笔至少 2 笔且分类全一样就直接用；不一样就把这些账交给决策模型，按时间段、金额最接近的几笔判断，拿不准时照常让你确认。

快捷指令里的每个对话框都由服务端决定，快捷指令本身只是一个循环，改流程不用重装。

转给别人和发红包记为支出，收到别人的转账和红包记为收入；只有自己账户之间的转账（提现、充值、转入余额宝等）记为转账，还信用卡、花呗记为信用卡还款。退款暂不自动记录。

## 配置

模型、OCR 服务、阈值等都在 [config.toml](config.toml) 里。密钥不写在文件里，配置项填的是环境变量名：

| 环境变量 | 用途 |
| --- | --- |
| `OPENROUTER_API_KEY` | 整理文字行的模型、决策模型（经 OpenRouter 时）和视觉 LLM |
| `TYPESAFE_AI_API_KEY` | 决策模型直接走 TypeSafe 官方接口时 |
| `VOLCANO_OCR_ACCESS_KEY`、`VOLCANO_OCR_SECRET_KEY` | 火山 OCR |
| `GOOGLE_OCR_API_KEY` | 谷歌 Cloud Vision |
| `BAIDU_OCR_API_KEY`、`BAIDU_OCR_SECRET_KEY` | 百度 OCR |
| `LEDGER_SNAP_INVITE_CODES` | 逗号分隔的邀请码，没有邀请码无法登录 |

本机的密钥由 Harmonia 管理，命令通过 `harmonia exec --` 运行。

`config.toml` 里的每一项都可以用环境变量覆盖，优先于文件。变量名是 `LEDGER_SNAP_` 加上配置项路径的大写，点换成下划线，例如：

| 配置项 | 环境变量 |
| --- | --- |
| `ocr.provider` | `LEDGER_SNAP_OCR_PROVIDER` |
| `decision.model` | `LEDGER_SNAP_DECISION_MODEL` |
| `decision.thresholds.category` | `LEDGER_SNAP_DECISION_THRESHOLDS_CATEGORY` |
| `fallback.enabled` | `LEDGER_SNAP_FALLBACK_ENABLED` |

- 时长写成 `10m`、`6h` 这样；列表用逗号分隔；布尔值写 `true` 或 `false`。
- 值为空的变量当作没设置。
- 拼错的 `LEDGER_SNAP_` 变量会让服务拒绝启动，和配置文件里的未知字段一样，免得改了却没生效。
- 启动日志会列出被环境变量覆盖的配置项。

`ledger-snap config` 列出全部配置项、对应的环境变量和当前值；在容器里运行 `docker compose exec ledger-snap /app/ledger-snap config`。

## 命令

```bash
mise run check
```

```bash
mise run serve
```

| 命令 | 作用 |
| --- | --- |
| `ledger-snap serve` | 启动 HTTP 服务 |
| `ledger-snap config` | 列出全部配置项、覆盖它的环境变量和当前值 |
| `ledger-snap sync-bills` | 立即从钱迹同步账单缓存；`--full` 清空重拉，`--account` 只同步一个账号。服务运行时也可以用 |
| `ledger-snap ocr --provider volcano,google,baidu <图片>` | 对比 OCR 结果 |
| `ledger-snap recognize <图片>...` | 用测试账号的分类和资产识别截图，不写入钱迹 |
| `ledger-snap eval` | 按 `testdata/screens/labels.json` 评测准确率、耗时和费用 |
| `ledger-snap shortcut` | 生成并签名快捷指令到 `dist/截图记账.shortcut`（需要 macOS），也可以用 `mise run shortcut` |
| `ledger-snap version` | 打印版本号（发布镜像时由标签注入） |
| `ledger-snap qianji-smoke` | 测试账号登录，全量拉取账单，新增、修改、删除一笔 0.01 元账单，并确认增量拉取能拿到修改和删除 |
| `ledger-snap seed-assets` | 在测试账号里新建一批容易混淆的账户（已存在的跳过） |

测试截图在 `testdata/screens`，共 50 张：普通支付、待支付、容易混淆的账户和商户、红包和转账、自己账户之间的转账和还款、复杂界面（费用明细、电商订单、账单列表、深色模式、带广告、短信）、外币（换汇、境外消费、港元账户消费）、优惠和手续费（还款优惠、提现服务费、多项优惠）、支付失败、与支付无关的页面，以及标签和值不在一行的排版（标签在值上方、两列网格、值折成两行、金额下面才是说明、日期和时刻分成两项）。截图由 `src/` 里的网页生成，修改后运行 `testdata/screens/render.sh` 重新生成，只重新生成某几张时把文件名跟在后面；标注在 `labels.json`。

`recognize` 和 `eval` 支持 `--provider`、`--rows-model`、`--no-rows`、`--decision-provider`、`--decision-model`、`--fallback-model`、`--no-image`、`--no-fallback`、`--force-fallback`，可以不改配置直接对比。`eval` 还可以加 `--ocr-cache <目录>`，把每张截图的 OCR 结果存到这个目录、下次直接用，对比决策模型时 OCR 只跑一次，各模型拿到的文字也完全相同。用上缓存的截图不计 OCR 耗时；缓存按 OCR 服务和截图文件名区分，重新生成截图后要删掉对应的缓存。

测试账号写在不提交的 `.env.local` 里：

```
QIANJI_TEST_ACCOUNT=...
QIANJI_TEST_PASSWORD_MD5=...
```

## Docker 部署

镜像发布在 `ghcr.io/cr-zhichen/ledger-snap`，支持 amd64 和 arm64，可以直接拉取，不用登录。镜像里自带同版本的 `config.toml`，所以服务器上只需要 [compose.yaml](compose.yaml) 和 `.env`，数据直接映射到 `./data`（数据库 `app.sqlite` 和截图都在里面）。

在服务器上建一个目录，下载这两个文件：

```bash
curl -fsSLO https://raw.githubusercontent.com/cr-zhichen/ledger-snap/main/compose.yaml
```

```bash
curl -fsSL -o .env https://raw.githubusercontent.com/cr-zhichen/ledger-snap/main/.env.example
```

在 `.env` 里填好密钥和邀请码，再改两处：

- `LEDGER_SNAP_UID`、`LEDGER_SNAP_GID` 改成当前用户的 `id -u`、`id -g`；
- `LEDGER_SNAP_IMAGE` 建议固定版本，例如 `ghcr.io/cr-zhichen/ledger-snap:0.2.1`，留空则用 `latest`。

然后启动：

```bash
docker compose up -d
```

`data` 目录不用事先建。容器先以 root 启动，把 `./data` 及其中的文件交给 `LEDGER_SNAP_UID:LEDGER_SNAP_GID`（留空时为 65532），再降到这个用户运行。填了自己的 `id -u`、`id -g`，宿主机上就能直接读写 `data`；填 `0` 表示一直以 root 运行。

v0.2.0 及更早的 `compose.yaml` 里有一行 `user:`，会让容器跳过这一步。从旧版本升级时，请重新下载 `compose.yaml`，或删掉那一行。

**升级**：改 `.env` 里的 `LEDGER_SNAP_IMAGE`，然后：

```bash
docker compose pull
```

```bash
docker compose up -d
```

**改配置**：在 `.env` 里加对应的 `LEDGER_SNAP_` 变量（见上面的“配置”一节，`.env.example` 里列了常用的几项），然后 `docker compose up -d` 重建容器即可生效（只 `restart` 不会重新读取 `.env`）。要改的项很多时，也可以下载与镜像同一版本的 `config.toml`（例如 `https://raw.githubusercontent.com/cr-zhichen/ledger-snap/v0.1.0/config.toml`）放在 `compose.yaml` 旁边，取消 `compose.yaml` 里挂载它的那行注释；配置和镜像要用同一个版本，程序遇到不认识的配置项会拒绝启动。

**反向代理**：容器只提供 HTTP，HTTPS 由外部的反向代理负责。反向代理和容器在同一台机器上时，建议把 `LEDGER_SNAP_PORT` 设为 `127.0.0.1:8080`，服务端口只对本机开放。上传截图的请求体可能超过 1 MB，反向代理的请求体上限要放宽到 10 MB 左右（Nginx 默认只有 1 MB，对应 `client_max_body_size`）。

**备份**：停掉服务，直接复制 `./data` 目录即可。

**用本地源码构建**：在项目目录里运行 `mise run docker:up`。它叠加 [compose.build.yaml](compose.build.yaml)，用当前源码构建出 `ledger-snap:dev` 镜像，并挂载本地的 `config.toml`。

## 发布镜像

推送 `v*` 标签时，[.github/workflows/release.yml](.github/workflows/release.yml) 会自动运行，平时推代码不会触发：

1. 核对标签：格式必须是 `v1.2.3` 或 `v1.2.3-rc.1`（也可以是 `beta`、`alpha`），并且标签指向的提交在默认分支上；
2. 运行 `mise run check`；
3. 构建 `linux/amd64` 和 `linux/arm64` 两个架构的镜像，推送到 `ghcr.io/cr-zhichen/ledger-snap`。

| 标签 | 镜像标签 |
| --- | --- |
| `v1.2.0` | `1.2.0`、`1.2`、`latest` |
| `v1.2.0-rc.1` | `1.2.0-rc.1`、`rc`（`beta`、`alpha` 同理） |

```bash
git tag v0.1.0
```

```bash
git push origin v0.1.0
```

快捷指令要在 macOS 上签名，工作流做不了。镜像发布后，在 Mac 上把签名好的快捷指令传到这个版本的 Release（没有 Release 时会先创建），README 里的下载链接始终指向最新版本：

```bash
mise run release:shortcut v0.1.0
```

## HTTP 接口

快捷指令只用 `/v1/flow/` 下的三个接口，说明见 [docs/shortcut.md](docs/shortcut.md#工作原理)。下面这些接口按状态码返回结果，适合脚本或自己搭的快捷指令调用：

| 接口 | 说明 |
| --- | --- |
| `POST /v1/login` | JSON `account`、`password_md5`、`invite_code`，返回 `token` |
| `POST /v1/recognize` | 请求体是截图本身，或 multipart 的 `image` 字段；可选 `client_time`（Unix 秒） |
| `GET /v1/options` | 选择器用的账户、分类、类型名称 |
| `POST /v1/catalog/refresh` | 立即从钱迹重新拉取分类和账户；修改时名字找不到也会自动刷新一次 |
| `POST /v1/records/{id}/update` | JSON，按名称修改 `kind`、`category`、`asset`、`target_asset`、`money`、`time`、`remark` |
| `POST /v1/records/{id}/confirm` | 写入一条 `needs_confirm` 的记录 |
| `POST /v1/records/{id}/correct` | JSON `text`，用一句话修改 |
| `POST /v1/records/{id}/delete` | 撤销；也可以用 `DELETE /v1/records/{id}` |

除登录外都要带 `Authorization: Bearer <token>`。

## 数据

`data/` 下有 `app.sqlite`（用户、钱迹会话、目录缓存、账单缓存、识别记录、记忆规则）和 `images/`（截图，默认保留 30 天）。数据库里有钱迹密码 MD5 和 Token，文件权限为 0600。

## 许可证

[MIT](LICENSE)
