# workbuddy-cliproxy

把**腾讯 CodeBuddy**（`copilot.tencent.com`）封装成 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)(CPA) 插件的动态库,提供 **OpenAI / Anthropic 双协议**兼容接口。装上之后,任何支持 OpenAI 或 Anthropic API 的客户端(Claude Code、Cursor、Cline、openai/anthropic SDK……)都能直接调用 CodeBuddy 背后的模型。

## 致谢与来源

> 本仓库是对 [**Sliverkiss/cpa-plugin**](https://github.com/Sliverkiss/cpa-plugin) 公开 `workbuddy.so` 二进制的 **clean-room 逆向重写**。
>
> 原作者 **Sliverkiss** 只发布了 ARM64 的二进制、未开源。本仓库从那个二进制的符号表、字符串常量与 RPC 契约出发,重新编写了一份等价的 Go 源码,并补上了 x86_64 支持、非流式聚合、以及跨协议流式翻译的兼容修复。
>
> **workbuddy 这个插件的全部功劳归属于 Sliverkiss。** 本仓库只是在它工作的基础上补齐源码与文档,方便更多架构 / 协议的接入。

## 工作原理

workbuddy 在 CPA 里注册为一个 provider(`provider: workbuddy`),实现三类能力:

| 能力 | 做什么 |
|------|--------|
| `AuthProvider` | CodeBuddy 网页扫码登录、轮询拿 token、token 自动刷新 |
| `ModelProvider` | 声明可用的模型列表 |
| `ProviderExecutor` | 把请求转发到 `https://copilot.tencent.com/v2/chat/completions` |

登录后 token 以 `workbuddy.json` 持久化,后续请求自动带上 `Authorization: Bearer <accessToken>` 以及 `X-User-Id` / `X-Enterprise-Id` 等 CodeBuddy 要求的 header。

## 支持的模型

| model id | 显示名 |
|----------|--------|
| `glm-5.2` | GLM-5.2 |
| `glm-5.1` | GLM-5.1 |
| `glm-5v-turbo` | GLM-5V Turbo |
| `kimi-k2.7` | Kimi K2.7 |
| `minimax-m3-pay` | MiniMax M3 |
| `hy3-preview-agent` | Hy3 Preview Agent |
| `deepseek-v4-pro` | DeepSeek V4 Pro |
| `deepseek-v4-flash` | DeepSeek V4 Flash |

## 前置条件

- **CLIProxyAPI v7.2.x**(运行中的 CPA 实例,容器或裸机均可,需带 CGO / 插件支持)
- **CodeBuddy 账号**(腾讯云,用于扫码登录)
- 编译需要:**Go 1.26+** + **gcc**(CGO)+ 和你 CPA 实例**同架构**(amd64 / arm64)

## 安装

### 1. 编译插件

```bash
git clone https://github.com/lovingfish/workbuddy-cliproxy.git
cd workbuddy-cliproxy

# 编译成动态库,架构要和你的 CPA 一致
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build -buildmode=c-shared -o workbuddy.so .
```

产物是 `workbuddy.so`(Linux)。macOS 是 `workbuddy.dylib`,Windows 是 `workbuddy.dll`。

### 2. 放到 CPA 的插件目录

把 `workbuddy.so` 放到 CPA 的 plugins 目录(下面的 `dir` 要对应):

```
<cpa 工作目录>/plugins/workbuddy.so
```

### 3. 在 `config.yaml` 启用插件

```yaml
plugins:
  enabled: true
  dir: "plugins"          # 上一步放 .so 的目录
  configs:
    workbuddy:
      enabled: true
      priority: 100
```

### 4. 重启 CPA

```bash
docker restart cli-proxy-api    # 或你启动 CPA 的方式
```

启动日志里看到这两行就说明加载成功:

```
pluginhost: plugin loaded     plugin_id=workbuddy path=.../workbuddy.so
pluginhost: plugin registered plugin_id=workbuddy version=0.1.0
```

`GET /v1/models` 也能看到上面那 8 个模型。

### 5. 登录 CodeBuddy

打开 CPA 管理面板 → 添加 workbuddy provider 的凭据 → 会拿到一个 `https://copilot.tencent.com/login?...` 链接 → 浏览器扫码登录你的 CodeBuddy 账号 → 登录成功后 token 自动存为 `workbuddy.json`。

## 使用

CPA 默认端口 `8317`,API key 用你在 `config.yaml` 的 `api-keys`。

### OpenAI 协议(`/v1/chat/completions`)

```
Base URL:  http://<host>:8317/v1
API Key:   <你的 cpa api-key>
Model:     hy3-preview-agent   (或上表任意一个)
```

```bash
curl http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"hy3-preview-agent","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

流式和非流式都支持。

### Anthropic 协议(`/v1/messages`)

```
Base URL:  http://<host>:8317        # 注意不带 /v1
API Key:   <你的 cpa api-key>        # 走 x-api-key
Model:     hy3-preview-agent
```

### 当 Claude Code 后端

```bash
export ANTHROPIC_BASE_URL=http://localhost:8317
export ANTHROPIC_API_KEY=<你的 cpa api-key>
export ANTHROPIC_MODEL=hy3-preview-agent
export ANTHROPIC_SMALL_FAST_MODEL=hy3-preview-agent
claude
```

## 已知行为

- **上游只支持流式**:CodeBuddy 的 `/v2/chat/completions` 拒绝非流式请求(`code 11101`)。所以即便客户端发非流式请求,插件内部也会强制 `stream:true` 调上游,再把结果聚合成一个标准 `chat.completion` 返回。
- **跨协议流式**:CodeBuddy 返回的是 OpenAI 格式的 SSE。CPA 的 chat-completions 直通路径和跨格式翻译路径(Anthropic/Gemini…)对 chunk 的 `data:` 前缀要求不同,插件按请求入口路径(`/v1/chat/completions` vs `/v1/messages` 等)自动适配,见 `clientNeedsSSEFrame`。
- **token 刷新**:`AuthProvider` 实现了 `RefreshAuth`,token 过期会自动用 refresh token 换新的。

## License

MIT。但请留意:本仓库是对 **Sliverkiss/cpa-plugin** 公开二进制的 clean-room 重写,原项目未附带开源协议,workbuddy 的原始设计归属于 Sliverkiss。
