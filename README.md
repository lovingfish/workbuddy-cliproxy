# workbuddy-cliproxy

把**腾讯 CodeBuddy**（`copilot.tencent.com`）封装成 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)(CPA)插件,任何支持 OpenAI / Anthropic 协议的客户端(Claude Code、Cursor、Cline、SDK……)都能直接调用 CodeBuddy 背后的模型。

对 [Sliverkiss/cpa-plugin](https://github.com/Sliverkiss/cpa-plugin) 公开 `workbuddy.so` 的 clean-room 逆向重写,补齐了源码与 x86_64 支持;workbuddy 的原始设计归属 Sliverkiss。

## 工作原理

在 CPA 里注册为 `workbuddy` provider:负责 CodeBuddy 扫码登录、token 刷新,并把请求转发到 `copilot.tencent.com/v2/chat/completions`。登录后凭据存为 `workbuddy.json`。

## 模型

登录后，插件使用当前账号请求 CodeBuddy `GET /v3/config`，按 `agents.cli.models` 动态注册模型，不再维护固定列表。通过 `catalog.channel` 可以选择其他上游公布的渠道；渠道缺失时不会回退到整个产品模型池。

模型 ID、默认思考档位和积分倍率来自该账号、渠道的配置，可能与 CodeBuddy 桌面客户端不同。目录列出模型不代表已经验证推理可用性，实际调用仍受账号权限和额度约束。旧 ID 如果已不在上游目录中，将不再注册；客户端需要刷新模型列表，不会自动重写成另一模型。

标准 `GET /v1/models` 保留 `id`、`object`、`owned_by`，并补充上游公布的显示名称、思考档位、积分倍率和目录更新时间。字段与缓存行为见 [动态模型目录](docs/dynamic-models.md)。

## 安装

**前置**:运行中的 CLIProxyAPI(带 CGO / 插件支持；动态目录与模型列表扩展已在 v7.3.10 验证)、CodeBuddy 账号、Go 1.26+ 与 gcc;编译架构需与 CPA 实例一致(amd64 / arm64)。

```bash
git clone https://github.com/lovingfish/workbuddy-cliproxy.git
cd workbuddy-cliproxy
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build -buildmode=c-shared -o workbuddy.so .
```

产物:`.so`(Linux)/ `.dylib`(macOS)/ `.dll`(Windows)。放到 CPA 的 `plugins/` 目录,在 `config.yaml` 启用:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    workbuddy:
      enabled: true
      priority: 100
      catalog:
        channel: cli
        ttl-seconds: 300
        max-stale-seconds: 86400
```

重启 CPA,日志出现 `plugin loaded ... plugin_id=workbuddy` 即成功。然后到 CPA 面板添加 workbuddy 凭据,扫码登录 CodeBuddy；`GET /v1/models` 将显示该账号当前渠道的模型。未登录时不发布静态模型。

## 使用

CPA 默认端口 `8317`,API key 见 `config.yaml` 的 `api-keys`。

| 协议 | Base URL |
|------|----------|
| OpenAI | `http://<host>:8317/v1` |
| Anthropic | `http://<host>:8317`(不带 `/v1`,走 `x-api-key`) |

```bash
# Claude Code
export ANTHROPIC_BASE_URL=http://localhost:8317
export ANTHROPIC_API_KEY=<api-key>
export ANTHROPIC_MODEL=hy3
claude
```

```bash
# curl / OpenAI
curl http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" -H "Content-Type: application/json" \
  -d '{"model":"hy3","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

流式 / 非流式都支持;非流式请求会被内部转成流式再聚合(CodeBuddy 上游 `code 11101` 拒绝非流式)。

## Claude Code 兼容性

腾讯 CodeBuddy 的内容审核把 Claude Code 的两句固定 system 模板逐字加进了黑名单,命中即回"敏感内容"拒答:

- `You are Claude Code, Anthropic's official CLI for Claude.`(身份句)
- `Main branch (you will usually use this for PRs)`(git 注入句)

任何一字改动都绕过(精确匹配,非语义审核)。workbuddy 转发前会自动把这两句做最小改写(`CLI`→`CLI tool`、`Main branch`→`Default branch`),语义不变,Claude Code 照常工作。

属于 cat-and-mouse:腾讯哪天多加模板句,得跟着改 `sanitizeBlockedTemplates`。

## 思考模式

客户端显式传入的 `reasoning_effort` 会保留；省略时使用上游模型配置的默认值，不再将所有 Hy3 请求强制改成 `high`。只有上游明确提供 `supportedEfforts` 时才按该列表校验，不能把默认档位推断成唯一可用档位。上游不允许的显式档位会返回 HTTP 400。

`model` 使用目录中的真实 ID，思考档位通过单独的 `reasoning_effort` 字段传入。思考内容仍走 SSE 的 `delta.reasoning_content`。

## 流式

真流式(async):转发上游时边读边通过 `host.stream.emit` 把每个 chunk 实时推给 CPA,客户端逐字收到(不是等收齐了一股脑)。hy3 几千字的思考过程也是实时流出的,不是憋半天再刷出来。

## 测试与定期刷新

Linux 下运行 `sh scripts/build-linux.sh`，脚本会执行 `go test -race ./...` 并构建共享库；可用 `GO_BIN` 指定 Go 编译器。Windows 可运行纯 Go 模块测试 `go test ./internal/catalog`。

目录按需缓存 5 分钟。要定期更新 CPA 的模型注册，可选用 `scripts/refresh_models.py` 和 `deploy/` 中的 systemd 模板；它们通过管理 API 触发配置热重载，不重启 CPA。安装步骤、凭据格式和限制见 [定期刷新](docs/dynamic-models.md#定期刷新)。

## License

MIT。
