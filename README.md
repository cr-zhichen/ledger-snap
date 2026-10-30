# ledger-snap

ledger-snap 将 iPhone 截图中的交易记入钱迹，支持单笔支付详情和多笔账单列表。适合使用钱迹、希望通过截图完成记账的用户。需要部署服务端，并在 iPhone 安装配套快捷指令。

## 部署

下载 [compose.yaml](compose.yaml) 和 [.env.example](.env.example)，把 `.env.example` 复制为 `.env`，填写 OpenRouter 密钥和邀请码后启动：

```bash
docker compose up -d
```

通过 HTTPS 反向代理访问服务。默认端口为 `8080`，上传大小上限为 `10 MB`，反向代理应允许同等大小的请求。

## 快速开始

在 iPhone 上使用 Safari [下载快捷指令](https://github.com/cr-zhichen/ledger-snap/releases/latest/download/ledger-snap.shortcut)，打开并添加。首次运行填写服务器地址、邀请码和钱迹账号。

在支付详情或账单列表页触发快捷指令。多笔识别会显示账单列表，每笔可以分别确认、修改或删除；只识别到一笔时直接显示该笔结果。安装、触发方式和操作说明见 [快捷指令](docs/shortcut.md)。

## 配置

配置项、作用和默认值见 [config.toml](config.toml)。`ledger-snap config` 可查看全部配置及对应环境变量；容器内使用：

```bash
docker compose exec ledger-snap /app/ledger-snap config
```

密钥和部署配置填写在 [.env](.env.example)：

| 名称 | 作用 | 默认值 |
| --- | --- | --- |
| `OPENROUTER_API_KEY` | 图片识别和账户、分类匹配 | 必填 |
| `LEDGER_SNAP_INVITE_CODES` | 允许注册的邀请码，多个用逗号分隔 | 必填 |
| `TYPESAFE_AI_API_KEY` | 选择 TypeSafe 直连接口时使用 | 空 |
| `LEDGER_SNAP_PORT` | 宿主机端口 | `8080` |
| `LEDGER_SNAP_IMAGE` | 使用的镜像版本 | `ghcr.io/cr-zhichen/ledger-snap:latest` |
| `LEDGER_SNAP_UID`、`LEDGER_SNAP_GID` | 容器运行用户和组 | `65532` |

配置项可以通过 `LEDGER_SNAP_` 加路径大写覆盖，点换成下划线，例如 `extraction.model` 对应 `LEDGER_SNAP_EXTRACTION_MODEL`。值为空时使用配置文件默认值。

## 更新

```bash
docker compose pull
docker compose up -d
```

如果挂载了自定义 `config.toml`，请同时换成新版本的配置文件。本次识别流程使用 `extraction` 配置，旧的 `ocr`、`rows`、`fallback` 和 `correction` 配置及对应环境变量需要删除。

## 备份

停止服务后复制 `./data` 目录。恢复时把备份目录放回原位置再启动服务。

## 接口

脚本调用方式见 [接口说明](docs/design.md)。

## 许可证

[MIT](LICENSE)
