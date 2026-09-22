# 动态模型目录

插件使用已登录账号的 access token、用户、企业和域信息请求
`https://copilot.tencent.com/v3/config`，再按 `agents` 中所选渠道的模型 ID 筛选。
默认渠道为 `cli`，请求头沿用插件现有的 CLI 身份。

只发布渠道允许且未被禁用的模型。渠道缺失、鉴权失败或无法获得可靠目录时，
不会回退到旧的固定模型表，也不会把整个产品模型池当作账号权限。
上下文窗口等数值仅在上游提供时填入，不再沿用固定表中的估计值。

## 模型列表字段

标准 OpenAI `/v1/models` 的 `id`、`object`、`owned_by` 保持不变。
仅为 WorkBuddy 模型补充以下可得字段：

| 字段 | 含义 |
| --- | --- |
| `name` / `display_name` | 上游显示名称 |
| `credits` | 积分倍率原文 |
| `reasoning` | 经过字段筛选的原生思考配置 |
| `supported_reasoning_efforts` | 仅在上游明确枚举时出现 |
| `default_reasoning_effort` | 默认档位，不代表只支持这一档 |
| `pricing` | 倍率显示值、来源，以及可得的优惠和档位规则 |
| `catalog` | 渠道、来源、拉取时间、新鲜度和目录状态 |
| `supports_images` / `supports_reasoning` | 上游声明的能力 |
| `context_length` / `max_completion_tokens` | 上游提供时才出现 |
| `catalog_variants` | 多账号存在对应元数据时分别列出，不合并成一个价格 |

结构示例（数值仅作说明，不代表实时价格）：

```json
{
  "id": "example-model",
  "object": "model",
  "owned_by": "workbuddy",
  "display_name": "Example Model",
  "credits": "x0.06 credits",
  "supported_reasoning_efforts": ["low", "high", "max"],
  "default_reasoning_effort": "high",
  "pricing": {
    "display": "x0.06 credits",
    "unit": "credit_multiplier",
    "source": "account_channel_catalog"
  },
  "catalog": {
    "provider": "workbuddy",
    "channel": "cli",
    "source": "https://copilot.tencent.com/v3/config",
    "fetched_at": "2026-09-22T00:00:00Z",
    "stale": false,
    "availability": "listed_by_upstream"
  }
}
```

倍率不是美元单价，也不是实测账单。优惠条件保留供客户端判断，不自行套用。
模型被列出只表示上游配置包含该 ID，推理能否成功仍需实际请求验证。
桌面 IDE 和 CLI 的模型集合、思考档位或活动规则可能不同。

模型列表扩展已在原版 CPA v7.3.10 验证，使用宿主的响应拦截接口，
无需修改 CPA 主程序。插件 SDK 仍依赖 v7.2.30 的 C ABI。
Codex 特有的 `client_version` 目录格式不在此扩展范围内。

## 缓存与思考档位

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    workbuddy:
      enabled: true
      priority: 100
      catalog:
        channel: cli
        ttl-seconds: 300
        max-stale-seconds: 86400
```

缓存按用户、企业、域、渠道和令牌隔离。同一缓存条目的并发请求合并拉取。
临时错误可使用距离成功拉取时间不超过 `max-stale-seconds` 的目录，
并标记 `stale: true`；失败后有 30 秒重试退避。HTTP 401/403 会清除对应缓存。
若宿主仍保留模型注册但插件已无可靠元数据，则返回
`catalog.availability: metadata_unavailable`，不附带旧价格。

显式 `reasoning_effort` 会保留；上游提供 `supportedEfforts` 时才校验枚举。
仅提供默认档位不意味着只能使用该档位。省略参数时使用上游默认值，
关闭思考的请求按 `canDisableThinking` 处理。请使用真实模型 ID 和单独的
`reasoning_effort` 字段。

## 定期刷新

TTL 控制插件的按需发现缓存。CPA 的模型注册还需要宿主触发重新发现。
可选脚本 `scripts/refresh_models.py` 通过管理 API 更新插件配置中的
`_catalog_refresh` 标记，触发 CPA 的配置热重载与模型重新注册；随后检查
`/v1/models` 的拉取时间是否更新，不重启 CPA，也不修改账号令牌。

当前脚本面向单账号目录，要求已经登录、存在可用模型且插件版本为
`0.2.0-catalog`。多账号 `catalog_variants` 的刷新验证尚未支持。
Python 3 标准库即可运行：

```bash
python3 scripts/refresh_models.py \
  --base-url http://127.0.0.1:8317 \
  --credentials-file /etc/cliproxyapi/model-refresh.json
```

凭据文件使用如下结构，填写现有 CPA 管理密钥和 API key，并限制文件读取权限：

```json
{
  "management_key": "<CPA management key>",
  "api_key": "<CPA API key>"
}
```

`deploy/` 提供可选 systemd 模板，默认 CPA 服务和运行用户均为 `cliproxyapi`，
访问地址为 `http://127.0.0.1:8317`。安装前按实际环境修改服务名、用户、
`LoadCredential` 文件路径，以及 `ExecStart` 中的脚本路径和 `--base-url`。
默认安装路径如下：

```bash
install -d -m 0755 /opt/workbuddy-cliproxy
install -m 0755 scripts/refresh_models.py /opt/workbuddy-cliproxy/refresh_models.py
install -m 0644 deploy/codebuddy-model-refresh.service /etc/systemd/system/
install -m 0644 deploy/codebuddy-model-refresh.timer /etc/systemd/system/
systemctl daemon-reload
systemctl start codebuddy-model-refresh.service
systemctl enable --now codebuddy-model-refresh.timer
```

systemd 通过 `LoadCredential` 向服务提供密钥，不把密钥放进命令行或日志。
模板在启动两分钟后首次执行，之后每五分钟执行；CPA 已停止时跳过。
可通过 `journalctl -u codebuddy-model-refresh.service` 检查执行结果。
