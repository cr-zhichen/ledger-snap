# ledger-snap

iPhone 上轻点背面截图，服务端识别截图里的交易并写入钱迹。设计见 [docs/design.md](docs/design.md)，快捷指令的生成、安装和使用见 [docs/shortcut.md](docs/shortcut.md)。

## 流程

1. 快捷指令截屏，上传到服务端。
2. 服务端调 OCR（火山多语种 OCR、谷歌或百度），从文字里列出金额和时间候选。
3. 一次决策模型请求同时判断：是否成功交易、收支类型、金额、时间、分类、账户、商户。默认经 OpenRouter 调用 Jev 1.14，也可以在 `config.toml` 里把 `decision.provider` 改成 `typesafe`，直接调用 TypeSafe 官方接口。
4. 判断不准时，交给视觉 LLM 兜底（默认 Claude Haiku 5.5）。
5. 写入钱迹，返回摘要；快捷指令弹出「确认 / 修改 / 删除」，修改时可以直接选账户、收支类型、分类，改金额、时间，或用一句话改。有拿不准的字段时先不写入，等用户确认（或修改后确认）再入库。

快捷指令里的每个对话框都由服务端决定，快捷指令本身只是一个循环，改流程不用重装。

转给别人和发红包记为支出，收到别人的转账和红包记为收入；只有自己账户之间的转账（提现、充值、转入余额宝等）记为转账，还信用卡、花呗记为信用卡还款。退款暂不自动记录。

## 配置

模型、OCR 服务、阈值等都在 [config.toml](config.toml) 里。密钥不写在文件里，配置项填的是环境变量名：

| 环境变量 | 用途 |
| --- | --- |
| `OPENROUTER_API_KEY` | 决策模型（经 OpenRouter 时）和视觉 LLM |
| `TYPESAFE_AI_API_KEY` | 决策模型直接走 TypeSafe 官方接口时 |
| `VOLCANO_OCR_ACCESS_KEY`、`VOLCANO_OCR_SECRET_KEY` | 火山 OCR |
| `GOOGLE_OCR_API_KEY` | 谷歌 Cloud Vision |
| `BAIDU_OCR_API_KEY`、`BAIDU_OCR_SECRET_KEY` | 百度 OCR |
| `LEDGER_SNAP_INVITE_CODES` | 逗号分隔的邀请码，没有邀请码无法登录 |

本机的密钥由 Harmonia 管理，命令通过 `harmonia exec --` 运行。

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
| `ledger-snap ocr --provider volcano,google,baidu <图片>` | 对比 OCR 结果 |
| `ledger-snap recognize <图片>...` | 用测试账号的分类和资产识别截图，不写入钱迹 |
| `ledger-snap eval` | 按 `testdata/screens/labels.json` 评测准确率、耗时和费用 |
| `ledger-snap shortcut` | 生成并签名快捷指令到 `dist/截图记账.shortcut`（需要 macOS），也可以用 `mise run shortcut` |
| `ledger-snap version` | 打印版本号（发布镜像时由标签注入） |
| `ledger-snap qianji-smoke` | 测试账号登录，并新增、修改、删除一笔 0.01 元账单 |
| `ledger-snap seed-assets` | 在测试账号里新建一批容易混淆的账户（已存在的跳过） |

测试截图在 `testdata/screens`，共 32 张：普通支付、容易混淆的账户和商户、红包和转账、自己账户之间的转账和还款、复杂界面（费用明细、电商订单、账单列表、深色模式、带广告、短信），以及与支付无关的页面。截图由 `src/` 里的网页生成，修改后运行 `testdata/screens/render.sh` 重新生成；标注在 `labels.json`。

`recognize` 和 `eval` 支持 `--provider`、`--decision-provider`、`--decision-model`、`--fallback-model`、`--no-image`、`--no-fallback`、`--force-fallback`，可以不改配置直接对比。

测试账号写在不提交的 `.env.local` 里：

```
QIANJI_TEST_ACCOUNT=...
QIANJI_TEST_PASSWORD_MD5=...
```

## Docker 部署

镜像是两阶段构建：用 `golang:1.27.2-alpine` 编译静态二进制，运行在 `distroless/static` 上，约 25 MB。数据直接映射到项目下的 `./data`（数据库 `app.sqlite` 和截图都在里面）。`config.toml` 以只读方式挂载，改完配置运行 `docker compose restart ledger-snap` 即可生效，不用重新构建。

compose 直接读取同目录的 `.env`（没有 `.env` 时会报错），把里面的变量全部传给服务：

```bash
cp .env.example .env
```

在 `.env` 里填好密钥和邀请码，并把 `LEDGER_SNAP_UID`、`LEDGER_SNAP_GID` 改成当前用户的 `id -u`、`id -g`：容器以这个身份运行，才能写入 `./data`。然后先建好数据目录再启动：

```bash
mkdir -p data
```

```bash
docker compose up -d --build
```

如果不先建 `data`，Docker 会以 root 身份创建它，容器就写不进去，启动时会报无法创建数据库。

容器只提供 HTTP，HTTPS 由外部的反向代理负责。反向代理和容器在同一台机器上时，建议把 `LEDGER_SNAP_PORT` 设为 `127.0.0.1:8080`，服务端口只对本机开放。上传截图的请求体可能超过 1 MB，反向代理的请求体上限要放宽到 10 MB 左右（Nginx 默认只有 1 MB，对应 `client_max_body_size`）。

`config.toml` 和镜像要用同一个版本：程序遇到不认识的配置项会拒绝启动，以免拼错的配置被悄悄忽略。

备份时停掉服务，直接复制 `./data` 目录即可。

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

GHCR 上的包第一次发布时默认是私有的。服务器要拉取它，有两种办法：在 GitHub 的 Packages 设置里把它改为公开；或者在服务器上用带 `read:packages` 权限的令牌执行 `docker login ghcr.io`。

服务器上使用发布好的镜像时，在 `.env` 里设置 `LEDGER_SNAP_IMAGE=ghcr.io/cr-zhichen/ledger-snap:0.1.0`（或 `latest`），然后：

```bash
docker compose pull
```

```bash
docker compose up -d
```

这时不要加 `--build`，否则会改用本地源码构建。服务器上只需要 `compose.yaml`、`config.toml`、`.env` 和 `data` 目录。

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

`data/` 下有 `app.sqlite`（用户、钱迹会话、目录缓存、识别记录、记忆规则）和 `images/`（截图，默认保留 30 天）。数据库里有钱迹密码 MD5 和 Token，文件权限为 0600。
