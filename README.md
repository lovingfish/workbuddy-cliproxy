# workbuddy-cliproxy

把**腾讯 CodeBuddy**（`copilot.tencent.com`）封装成 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)(CPA)插件,任何支持 OpenAI / Anthropic 协议的客户端(Claude Code、Cursor、Cline、SDK……)都能直接调用 CodeBuddy 背后的模型。

对 [Sliverkiss/cpa-plugin](https://github.com/Sliverkiss/cpa-plugin) 公开 `workbuddy.so` 的 clean-room 逆向重写,补齐了源码与 x86_64 支持;workbuddy 的原始设计归属 Sliverkiss。

## 工作原理

在 CPA 里注册为 `workbuddy` provider:负责 CodeBuddy 扫码登录、token 刷新,并把请求转发到 `copilot.tencent.com/v2/chat/completions`。登录后凭据存为 `workbuddy.json`。

## 模型

`glm-5.2` · `glm-5.1` · `glm-5v-turbo` · `kimi-k2.7` · `minimax-m3-pay` · `hy3` · `hy3-preview` · `hy3-preview-agent` · `deepseek-v4-pro` · `deepseek-v4-flash`

具体可用性以 CodeBuddy 账号权限为准。

## 安装

**前置**:运行中的 CLIProxyAPI v7.2.x(带 CGO / 插件支持)、CodeBuddy 账号、Go 1.26+ 与 gcc;编译架构需与 CPA 实例一致(amd64 / arm64)。

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
    workbuddy: { enabled: true, priority: 100 }
```

重启 CPA,日志出现 `plugin loaded ... plugin_id=workbuddy` 即成功,`GET /v1/models` 也能看到上面的模型。然后到 CPA 面板添加 workbuddy 凭据,扫码登录 CodeBuddy。

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
export ANTHROPIC_MODEL=hy3-preview-agent
claude
```

```bash
# curl / OpenAI
curl http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" -H "Content-Type: application/json" \
  -d '{"model":"hy3-preview-agent","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

流式 / 非流式都支持;非流式请求会被内部转成流式再聚合(CodeBuddy 上游 `code 11101` 拒绝非流式)。

## Claude Code 兼容性

腾讯 CodeBuddy 的内容审核把 Claude Code 的两句固定 system 模板逐字加进了黑名单,命中即回"敏感内容"拒答:

- `You are Claude Code, Anthropic's official CLI for Claude.`(身份句)
- `Main branch (you will usually use this for PRs)`(git 注入句)

任何一字改动都绕过(精确匹配,非语义审核)。workbuddy 转发前会自动把这两句做最小改写(`CLI`→`CLI tool`、`Main branch`→`Default branch`),语义不变,Claude Code 照常工作。

属于 cat-and-mouse:腾讯哪天多加模板句,得跟着改 `sanitizeBlockedTemplates`。

## 思考模式

hy3 系列(`hy3` / `hy3-preview` / `hy3-preview-agent`)自动开最大思考:workbuddy 转发前强制 `reasoning_effort=high`,覆盖客户端任何设置。CodeBuddy 只对 `high` 真正开深度思考(`medium` / `max` / `xhigh` 等档位它直接忽略),所以这已是 hy3 能用的最高档。思考内容走 SSE 的 `delta.reasoning_content`,客户端要支持渲染思考块才看得到。

## License

MIT。
