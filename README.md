# AgentNexus · 智枢

> **One control plane for chat-driven coding agents.**
>
> 一个连接消息、Agent、用量、记忆、Skills 与 MCP 的智能体中枢。

**AgentNexus**(中文名 *智枢 / 智能体中枢*)是一个单二进制的 Go 控制平面,把开发者本来要在多个工具间来回切换的能力统一到一处:从 IM 聊天驱动本地编码 Agent、在多 Agent 与多 LLM Provider 间路由、统计 Token 用量,并统一管理记忆、Skills、MCP 与权限审批。

CLI 名称:`agent-nexus`(短别名 `anx`)。

## 模块总览

| 功能 | 模块名 | 代码包 |
| --- | --- | --- |
| IM 连接 | **AgentNexus Connect** | `platform/` |
| Agent 路由 | **AgentNexus Router** | `agent/` |
| Token 统计 | **AgentNexus Ledger** | `usage/` |
| Trace 与优化建议 | **AgentNexus Observability** | `observability/` |
| 统一 Memory | **AgentNexus Memory** | `memory/` |
| Skills 管理 | **AgentNexus Skills** | `skills/` |
| MCP 管理 | **AgentNexus MCP Registry** | `mcp/` |
| 权限审批 | **AgentNexus Guard** | `guard/` |
| Web 控制台 | **AgentNexus Console** | `web/` + `server/` |

- **Connect** — 从消息平台(Feishu/Lark、Telegram、钉钉、Slack、Discord、通用 webhook;插件式扩展)与本地 AI 编码 Agent 对话;**渠道 & 触发**面板统一管理动态渠道、定时任务(cron)、入站 Webhook 与事件回调。
- **Router** — 支持 Claude Code、Codex、Cursor、Gemini、Qoder、OpenCode、iFlow、Kimi(插件式扩展),并在多 LLM Provider 间切换/故障转移。
- **渠道 & 触发** — 渠道是绑定 Agent 的实时 IM 连接(飞书/Telegram/钉钉/Slack/Discord/Webhook),控制台可增删改与启停/重启并显示运行状态;触发统一承载三类自动化:定时任务(robfig/cron,标准 5 段表达式)、入站 Webhook(`POST /hook/{id}`,自带 token 鉴权)、生命周期事件回调(`message.received`/`cron.triggered`/`error` 等 → Shell 或 HTTP)。定时/Webhook 触发把 Prompt 发给绑定 Agent 并将结果推回渠道会话,支持 `reuse`/`new_per_run` 会话模式。
- **Ledger** — 读取 Claude/Codex/Cursor/Gemini 的本地会话日志,基于 LiteLLM 价格数据计费,按天/周/月/会话/5 小时块出账,并能通过 SSH 采集远程机器用量。
- **Observability** — 用统一 Trace 串联 Agent Turn、模型请求、重试、工具、Hook 与渠道回复；融合内部事件、Claude/Codex 原生 OTel、Proxy 和增量 Transcript，并生成只读优化建议。
- **Memory** — 跨 Agent 与跨会话的统一记忆层(检索、写入、共享上下文)。
- **Skills** — 统一发现、安装与管理 Agent Skills。
- **MCP Registry** — 注册、编排与下发 MCP Server 配置。
- **Guard** — 工具调用的权限审批与策略闸门。
- **Console** — React Web 控制台,内嵌进二进制,统一观测与操作以上模块。

## 架构

```
clients (CLI / WebUI / Wails / menubar)
        |  HTTP /api/v1 (+ WS bridge)
   Go daemon
   ├── core/        interfaces + plugin registry + Engine + hooks + bridge
   ├── platform/    Connect:IM adapters (feishu, telegram, webhook, ...)
   ├── agent/       Router:agent adapters (claudecode, codex, cursor, gemini, ...)
   ├── provider/    provider mgmt + presets + failover proxy + live-config writer
   ├── usage/       Ledger:parsers + pricing + aggregation + SSH collector
   ├── observability/ encrypted recorder + OTLP + transcript + insights
   ├── integrations/ Claude/Codex native observer plugins + ownership doctor
   ├── memory/      Memory:统一记忆 store 与检索
   ├── skills/      Skills:Agent Skills 发现与管理
   ├── mcp/         MCP Registry:MCP server 注册与下发
   ├── guard/       Guard:权限审批与策略
   ├── store/       SQLite SSOT (atomic writes)
   └── server/      Console API + embedded WebUI (go:embed)
```

`core` 永不导入 `platform/`、`agent/`、`provider/`、`usage/`、`memory/`、`skills/`、`mcp/`、`guard/`;各适配器在自身 `init()` 中通过 registry 自注册。Memory/Skills/MCP/Guard 四个模块已落地骨架实现(SQLite 记忆层、SKILL.md 磁盘发现、MCP server 注册表、策略闸门),并通过 `/api/v1` 暴露给 Console。

## 快速开始

```bash
# 1. 构建(仅 CLI,占位 WebUI)
make build

# 2. 立即看 Token 用量(读取本地 Agent 日志)
./anx usage daily --since 7d

# 3. Provider 管理
./anx provider presets
./anx provider import anthropic-official
./anx provider switch anthropic-official --tool claudecode

# 4. 启动守护进程 + WebUI(嵌入式构建)
make release
./anx web        # 打开 http://127.0.0.1:8765
```

## 配置

复制 `config.example.toml` 为 `config.toml`。要点:

- `[[projects]]` 把一个 `agent` 与一个或多个 `[[projects.platforms]]` 配对。
- `[bridge]` 暴露 HTTP send API;**启用时必须设置 token**。
- `[usage]` 选择数据源,可选 `[[usage.ssh]]` 远程目标。
- `[observability]` 默认启用；完整内容先脱敏、分块压缩并以 AES-256-GCM 加密，默认保留 30 天，详细元数据保留 180 天。每个 `[[observability.exporters]]` 独立配置 OTLP 队列，`include_content` 默认关闭。
- `${ENV_VAR}` 占位符从环境变量展开。

Console 的 **Observability → Integrations** 可预览、安装、修复或卸载原生 `agentnexus-observer`。安装始终通过 Claude/Codex 自身的 plugin CLI；Codex 安装后保持 `pending_trust`，需在 `/hooks` 手动审核。AgentNexus 不覆盖 Flux Island、CC Switch 或其他同名/漂移资源。

## CLI

```
anx serve                       # IM gateway + management API
anx web [--no-open]             # serve + open Console
anx usage [daily|weekly|monthly|session|blocks] [--since 7d] [--json] [--ssh]
anx usage statusline            # compact one-liner for status bars/hooks
anx provider list|presets|import <id>|switch <id> --tool <tool>
anx send --text "..." --project <name> [--token <bridge-token>]
```

## HTTP API(节选)

Console 与各客户端共用一套 `/api/v1`:

```
GET  /api/v1/modules                 # 各模块注册/激活状态
GET  /api/v1/memory?scope=&q=&limit= # Memory 检索
POST /api/v1/memory                  # 写入记忆 {scope,content,tags}
DELETE /api/v1/memory?id=            # 删除记忆
GET  /api/v1/skills                  # 已发现的 Skills
POST /api/v1/skills/toggle           # 启用/禁用 {name,enabled}
GET  /api/v1/mcp                      # MCP server 列表
POST /api/v1/mcp                      # 注册/更新 MCP server
DELETE /api/v1/mcp?name=             # 删除 MCP server
GET  /api/v1/guard/policies          # Guard 策略列表
POST /api/v1/guard/evaluate          # 评估一次工具调用 {tool,action}

GET  /api/v1/channels                # 渠道列表(含运行状态)
POST /api/v1/channels                # 新建/更新渠道 {name,type,agent_id,config,enabled}
DELETE /api/v1/channels?id=          # 删除渠道
POST /api/v1/channels/restart?id=    # 重启渠道连接
GET  /api/v1/triggers                # 触发列表(含 last_run/last_status)
POST /api/v1/triggers                # 新建/更新触发(校验 cron 表达式/kind)
DELETE /api/v1/triggers?id=          # 删除触发
POST /api/v1/triggers/run?id=        # 立即执行一次触发
POST /hook/{id}                      # 入站 Webhook 触发端点(token 鉴权,payload 附加到 prompt)

GET  /api/v1/observability/traces              # Trace 列表与筛选
GET  /api/v1/observability/traces/{trace_id}   # Span/Event 时间线与授权解密内容
GET  /api/v1/observability/insights            # 仅建议的优化洞察
GET  /api/v1/observability/settings            # 保留期、密钥与 Exporter 状态
GET  /api/v1/observability/integrations        # Plugin/OTel/Transcript/Proxy Doctor
POST /api/v1/observability/integrations/{host}/{preview|install|repair|uninstall|doctor}
```

## 构建

| 目标 | 命令 | 说明 |
| --- | --- | --- |
| CLI (host) | `make build` | 同时构建 `anx` 与 fail-open 的 `agentnexus-hook` |
| CLI + WebUI | `make release` | `-tags embedweb`,嵌入 `web/dist` |
| 全平台 | `make cross` | Linux/macOS/Windows, amd64/arm64 |
| 桌面应用 | `make desktop` | 需要 Wails v2 工具链 |
| macOS 菜单栏 | `make menubar` | SwiftUI, 仅 macOS |
| 签名 macOS | `make sign-macos` | 需要 Developer ID + notarytool profile |

详见 [INSTALL.md](INSTALL.md)。

## 致谢

本项目整合了以下项目的思路:
[cc-connect](https://github.com/chenhg5/cc-connect)、
[cc-switch](https://github.com/farion1231/cc-switch)、
[ccusage](https://github.com/ryoppippi/ccusage)、
[CodeBurn](https://github.com/getagentseal/codeburn)、
[cc-statistics](https://github.com/androidZzT/cc-statistics)。

## 安全说明

- `[bridge].enabled` 时,管理/桥接 API 强制 bearer token。
- Provider API key 在 SQLite 中只保存 **环境变量名**(`api_key_env`),不明文落库;macOS 上保存时写入 Keychain,启动/读取 provider 时自动恢复到进程环境。非 macOS 环境仍可直接提供对应环境变量。
- SSH 采集器为本地工具便利使用 `InsecureIgnoreHostKey`;在不可信网络中使用前请固定 host key。
- Observability 内容不会明文写入 SQLite：已知 Secret、Authorization、Cookie、API Key 与隐藏 reasoning 在持久化前删除；macOS 主密钥位于 Keychain，其他平台未显式配置安全密钥时自动退化为 metadata-only。
- Console 的敏感 Trace API 使用 loopback 一次性 nonce 换取 SameSite HttpOnly 会话；原生 Hook/OTLP ingest 使用独立随机本地 token。OTLP Exporter 默认只发送元数据。

## License

MIT.
