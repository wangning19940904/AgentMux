# TraeX 与豆包工作本地接入

AgentMux 统一管理客户端路由；两个独立适配器处理平台鉴权和私有协议。

| 供应商 | 本地上游 | Codex | Claude Code |
| --- | --- | --- | --- |
| TraeX | `http://127.0.0.1:8317/v1` | Responses 原样转发 | Messages 原样转发 |
| 豆包工作 | `http://127.0.0.1:8787/v1` | Responses 原样转发 | Messages → Responses 转换 |

客户端连接 AgentMux 的 `127.0.0.1:15733`。在 AgentMux「LLM Provider 与路由」中编辑 Codex 或 Claude Code 的路由，选择 **TraeX** 或 **豆包工作**，使用「本地接管」并保存。切换应在当前生成和工具回传结束后进行。

Codex 切换供应商时同步默认模型和模型目录，下一次启动读取新配置；正在打开的客户端可重新启动以刷新模型选择器。Claude Code 可通过 `/model` 选择网关模型，代理把 `claude-` 前缀的展示别名映射回真实模型 ID。取消本地接管恢复接管前的连接配置。

也可通过 CLI 切换已接管的客户端；供应商 ID 分别为 `agentmux-traex` 和 `agentmux-doubao`：

```bash
amux --config "$HOME/Library/Application Support/agentmux/config.toml" provider switch agentmux-traex --tool codex
amux --config "$HOME/Library/Application Support/agentmux/config.toml" provider switch agentmux-traex --tool claudecode
```

## 当前安装

- 适配器目录：`~/Library/Application Support/agentmux/adapters/`。
- TraeX：`traex-bridge` 0.7.2，源提交 `82b7b2e5a55175579e0cd1998ebcdf97519cfdf0`，使用 Node 运行。
- 豆包工作：Switchboard 0.2.1，`2026-10-20-r2` 快照，使用独立 Python 虚拟环境。
- Switchboard 源包 SHA-256：`b5cd900fd3c77c7d09b8ef06b17a5c58d7e64c9631ed72d21ed107b0c7d73934`。
- 服务：`com.agentmux.adapter.traex`、`com.agentmux.adapter.doubao`，用户登录后由 LaunchAgent 启动，异常退出后自动重启。
- `state/traex/`、`state/doubao/` 保存独立状态。访问适配器的 API key 使用 0600 文件，并通过 AgentMux 的系统密钥存储保存；不要把这些目录提交到仓库。
- `state/verification/before/` 保存接入前的客户端配置和路由快照；实际接管恢复由 AgentMux 的所有权记录完成。

豆包会话已通过本机客户端导入，日常推理不需要客户端或调试端口常驻。注销、撤销或上游要求重新登录时，需要重新导入。Switchboard 不接管 Codex 配置，连接配置统一由 AgentMux 管理。

## 2026-10-26：豆包完整模型目录与按请求选路

上游 Switchboard 0.2.1 默认只在 `/v1/models` 返回控制台选中的模型，且推理也固定使用该选择。AgentMux 的目录同步会因此把其他模型移出列表。现已应用 [兼容补丁](patches/switchboard-doubao-catalog-routing.patch)：

- 豆包工作目录返回当前账号的完整模型列表；网络失败返回 502，避免用单模型列表覆盖已知目录。
- 每个请求独立使用 `model` 字段；未指定时才使用控制台默认模型。不会修改共享配置；未知模型由上游目录校验拒绝。
- 模型日志、响应和用量归属使用实际请求的模型。
- 支持显式设置 `DOUBAO_HTTPS_PROXY` 为本机 HTTP(S) 代理。本机当前设为 `http://127.0.0.1:7897`，与系统代理一致；仍启用 TLS 证书校验。代理端口变化时更新 `adapters/services.json` 中豆包服务的此变量并重启适配器。

当日账号目录返回 8 个模型，包含 `gpt-6.1-sol`、`orange-5.5`、`gpt-6-astra`。三个模型均完成独立真实请求，原生任务记录中的模型与请求一致。回归测试 174 项通过。

重新解压原版代码包后，在 Switchboard 目录应用补丁，再重启服务：

```bash
patch -p1 < /你的AgentMux仓库/integrations/patches/switchboard-doubao-catalog-routing.patch
```

后续上游版本如已包含同类修复，先核对代码，不要重复应用补丁。

## 服务维护

查看状态：

```bash
launchctl print "gui/$(id -u)/com.agentmux.adapter.traex"
launchctl print "gui/$(id -u)/com.agentmux.adapter.doubao"
```

重启单个适配器：

```bash
launchctl kickstart -k "gui/$(id -u)/com.agentmux.adapter.traex"
launchctl kickstart -k "gui/$(id -u)/com.agentmux.adapter.doubao"
```

升级前先结束正在生成的任务。TraeX 更新源码、安装锁定依赖并执行 `npm run build` 后重启；Switchboard 更新完整代码包并执行 `uv sync --locked --no-dev` 后重启。保留 `state/`、`services.json`、启动器和 LaunchAgent 配置。

日志位于 `state/traex/stdout.log` 与 `state/doubao/switchboard.log`。健康检查只证明服务或认证可用；验证接入需完成真实工具调用、工具结果回传和第二轮会话。

## 接入边界

- 模型目录以当前账号实际可用能力为准；调用使用该平台账号的权限和额度。
- TraeX 同时声明 Messages 与 Responses 时，AgentMux 选择客户端原生协议，保留工具、思考和图片字段。
- 豆包工作是有状态的任务适配。Claude 会话标识被散列后通过 Responses `prompt_cache_key` 传递，防止同一账号的不同会话混用远端任务。
- 此轮实测覆盖文本、文件工具和两轮会话。豆包工作经 Claude 转换的图片/二进制工具结果，以及超长上下文和长时间审批恢复，尚未做端到端验证。
- 当前通用 Codex 本地目录采用保守的 128k 窗口；这不代表上游模型的最大容量。扩大窗口需结合相应模型的实时能力及传输限制验证。

## 来源

- [TraeX bridge 接入指南](https://bytedance.sg.larkoffice.com/docx/A9hTdt31qodfaXxH2fwlv1PzgMb)
- [TraeX bridge 源码](https://code.byted.org/hepengcheng/traex_bridge)
- [Switchboard 说明与源码附件](https://bytedance.larkoffice.com/wiki/FTYowKaAai9GchkjttRcrR5GnRd)
