# AgentMux

> **One control plane for chat-driven coding agents.**
>
> 一个连接消息、Agent、用量、记忆、Skills 与 MCP 的智能体中枢。

**AgentMux** 是一个单二进制的 Go 控制平面，把开发者本来要在多个工具间来回切换的能力统一到一处：从 IM 聊天驱动本地编码 Agent、在多 Agent 与多 LLM Provider 间路由、统计 Token 用量，并统一管理记忆、Skills、MCP 与权限审批。

CLI 名称:`agentmux`(短别名 `amux`)。Linux 上推荐直接使用
`amux` 作为无图形化客户端:它可以以前台进程或 systemd 服务运行,
同时按需暴露 Web Console。

## 模块总览

| 功能 | 模块名 | 代码包 |
| --- | --- | --- |
| IM 连接 | **AgentMux Connect** | `platform/` |
| Agent 路由 | **AgentMux Router** | `agent/` |
| Token 统计 | **AgentMux Ledger** | `usage/` |
| Trace 与优化建议 | **AgentMux Observability** | `observability/` |
| 统一 Memory | **AgentMux Memory** | `memory/` |
| Skills 管理 | **AgentMux Skills** | `skills/` |
| MCP 管理 | **AgentMux MCP Registry** | `mcp/` |
| 权限审批 | **AgentMux Guard** | `guard/` |
| Web 控制台 | **AgentMux Console** | `web/` + `server/` |

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
- **SSH 远程控制** — 从 SSH Config 一键导入远程机器，验证 SSH 与 AgentMux 服务；服务缺失时自动上传匹配系统架构的 CLI 并启动，再通过 SSH 隧道复用 Console 管理其 Agent、Provider、渠道、Skills、MCP 等配置。

## 架构

```
clients (CLI / WebUI / Wails / menubar)
        |  HTTP /api/v1 (+ WS bridge)
   Go daemon
   ├── bootstrap/   shared daemon wiring for CLI serve/web and the desktop shell
   ├── core/        interfaces + plugin registry + Engine + hooks + bridge
   ├── platform/    Connect:IM adapters (feishu, telegram, webhook, ...) + settingsui
   ├── agent/       Router:agent adapters (claudecode, codex, cursor, gemini, ...)
   ├── provider/    provider mgmt + presets + failover proxy + live-config writer
   ├── usage/       Ledger:parsers + pricing + aggregation + SSH collector
   ├── observability/ encrypted recorder + OTLP + transcript + insights
   ├── integrations/ Claude/Codex native observer plugins + ownership doctor
   ├── memory/      Memory:统一记忆 store 与检索
   ├── skills/      Skills:Agent Skills 发现与管理
   ├── mcp/         MCP Registry:MCP server 注册与下发
   ├── guard/       Guard:权限审批与策略
   ├── store/       PostgreSQL SSOT + asynchronous observation batches
   └── server/      Console API + embedded WebUI (go:embed)
```

`core` 不导入任何适配器包(`platform/`、`agent/`、`provider/`、`usage/`、`tools/`、`memory/`、`skills/`、`mcp/`、`guard/`):各适配器在自身 `init()` 中通过 registry 自注册,CLI 目录探测等能力由 `bootstrap/` 以接口注入。

Memory/Skills/MCP/Guard 四个模块当前为 **Console 管理层实现**(PostgreSQL 记忆层、SKILL.md 磁盘发现、MCP server 注册表、策略闸门),通过 `/api/v1` 提供 CRUD 与评估接口,但尚未接入 Agent 对话回路:Memory 不会自动检索注入上下文、Guard 不拦截运行中的工具调用、MCP 配置不会下发生成到各 Agent 的原生配置文件、Skills 的启用状态仅保存在内存中。

## 快速开始

```bash
# 1. 构建(仅 CLI,占位 WebUI)
make build

# 2. 初始化本机 PostgreSQL
./amux database setup

# 3. 立即看 Token 用量(读取本地 Agent 日志)
./amux usage daily --since 7d

# 4. Provider 管理
./amux provider presets
./amux provider import anthropic-official
./amux provider switch anthropic-official --tool claudecode

# 5. 启动守护进程 + WebUI(嵌入式构建)
make release
./amux web        # 打开 http://127.0.0.1:8765

# Linux/headless: 初始化配置并启动客户端
./amux config init
./amux client --web
```

## 配置

复制 `config.example.toml` 为 `config.toml`,或直接运行
`amux config init` 写入默认用户配置。CLI 查找顺序:

1. `--config/-c`
2. `AMUX_CONFIG`
3. 当前目录 `config.toml`
4. `$XDG_CONFIG_HOME/agentmux/config.toml`
5. `/etc/agentmux/config.toml`(Linux/systemd)

要点:

- `[[projects]]` 把一个 `agent` 与一个或多个 `[[projects.platforms]]` 配对。
- `[bridge]` 暴露 HTTP send API;**启用时必须设置 token**。
- `[remote]` 配置 SSH 连接超时和本机远程档案路径；具体机器在 Console 的 **系统 → 远程机器** 中管理。
- `[usage]` 选择数据源,可选 `[[usage.ssh]]` 远程目标。
- `[observability]` 默认启用；完整内容先脱敏、分块压缩并以 AES-256-GCM 加密，默认保留 30 天，详细元数据保留 180 天。每个 `[[observability.exporters]]` 独立配置 OTLP 队列，`include_content` 默认关闭。
- `${ENV_VAR}` 占位符从环境变量展开。

Console 的 **Observability → Integrations** 可预览、安装、修复或卸载原生 `agentmux-observer`。安装始终通过 Claude/Codex 自身的 plugin CLI；Codex 安装后保持 `pending_trust`，需在 `/hooks` 手动审核。AgentMux 不覆盖 Flux Island、CC Switch 或其他同名/漂移资源。

## CLI

```
amux client [--web] [--open] [--addr 127.0.0.1:8765]
amux serve                       # IM gateway + management API
amux web [--no-open]             # serve + open Console
amux config init|path            # create or inspect config.toml
amux tools list|check|install|update <id>
amux usage [daily|weekly|monthly|session|blocks] [--since 7d] [--json] [--ssh]
amux usage statusline            # compact one-liner for status bars/hooks
amux provider list|presets|import <id>|switch <id> --tool <tool>
amux send --text "..." --project <name> [--token <bridge-token>]
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

GET    /api/v1/remote/hosts                    # 本机保存的 SSH 机器（敏感字段脱敏）
GET    /api/v1/remote/discovered-hosts         # 从 ~/.ssh/config 发现可导入的 Host 别名
POST   /api/v1/remote/hosts                    # 新建/更新 SSH 机器
POST   /api/v1/remote/hosts/import             # 验证 SSH、探测/安装 AgentMux 后导入
DELETE /api/v1/remote/hosts?id=                # 删除 SSH 机器
POST   /api/v1/remote/hosts/test?id=           # 测试连接并确认主机指纹

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

### 渠道命令

在绑定 Agent 的渠道中发送 `/help`，会收到包含 Agent 自我介绍、完整命令列表和快捷操作按钮的帮助卡片。飞书/Lark、Slack、Discord 和 Telegram 支持直接点击 `/model`、`/clear`、`/approval` 等按钮；其他渠道自动回退为文本帮助。

| 命令 | 效果 |
| --- | --- |
| `/help` | 查看 Agent 介绍、支持的命令和快捷按钮 |
| `/clear`、`/new`、`/reset` | 清除上下文并开始新会话 |
| `/model` | 查看或切换当前会话模型 |
| `/effort` | 查看或切换思考强度 |
| `/fast` | 查看或切换快速模式 |
| `/approval` | 查看或切换审批模式 |

启用 Codex 远程控制的渠道还会展示 `/status`、`/stop`、`/queue`、`/sessions`、`/bind`、`/open` 和 `/takeover`。

### 渠道审批命令

在飞书/Lark 等绑定 Agent 的渠道中，可以直接切换当前会话的审批模式；命令由 AgentMux 处理，不会转发给 Agent：

| 命令 | 效果 |
| --- | --- |
| `/approval` | 打开运行时设置卡或查看当前状态 |
| `/approval manual` | 切换为手动审批 |
| `/approval auto_edit` | 自动批准文件编辑 |
| `/approval auto` | 使用运行时的智能自动审批 |
| `/approval plan` | 切换为只读规划 |
| `/approval yolo`、`/yolo on` | 当前会话完全免审批 |
| `/yolo off` | 恢复当前会话的手动审批 |
| `/approval reset` | 恢复 Agent/运行时默认审批模式 |

不同 Agent runtime 支持的模式不同；不支持的命令会返回该 runtime 的可用模式列表。

运行时设置卡里的“当前会话”会立即生效；“Agent 默认”只影响之后创建的新会话，不会反向修改已经存在的会话。Codex app-server 能原生挂起权限请求，启用渠道 Codex 远程控制后会发送带“允许一次 / 本会话允许 / 拒绝”的审批卡片。Cursor、Claude Code 等 print-mode CLI 不能在渠道中暂停后继续接收逐次审批；它们的手动模式会直接拦截工具，请改用运行时支持的自动审批或 `/yolo on`。

## 构建

| 目标 | 命令 | 说明 |
| --- | --- | --- |
| CLI (host) | `make build` | 同时构建 `amux` 与 fail-open 的 `agentmux-hook` |
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
- SSH 远程控制优先使用私钥/`ssh-agent`；从 SSH Config 导入的主机也可回退到系统 OpenSSH 的非交互认证（例如 GSSAPI），且不保存 SSH 密码。首次连接需确认主机指纹，后续指纹变化会阻断。远程档案以 `0600` 保存，隧道目标限制为远端回环地址上的 AgentMux API。
- 自动安装的远程 AgentMux 使用独立的 `~/.agentmux/agentmux.db` SQLite 存储，无需在目标机安装 PostgreSQL；本机与常规服务端仍默认使用 PostgreSQL。
- Provider API key 在 PostgreSQL 中只保存 **环境变量名**(`api_key_env`),不明文落库;macOS 上保存时写入 Keychain,启动/读取 provider 时自动恢复到进程环境。非 macOS 环境仍可直接提供对应环境变量。
- SSH 采集器为本地工具便利使用 `InsecureIgnoreHostKey`;在不可信网络中使用前请固定 host key。
- Observability 内容不会明文写入 PostgreSQL：已知 Secret、Authorization、Cookie、API Key 与隐藏 reasoning 在持久化前删除；macOS 主密钥位于 Keychain，其他平台未显式配置安全密钥时自动退化为 metadata-only。
- Console 的敏感 Trace API 使用 loopback 一次性 nonce 换取 SameSite HttpOnly 会话；原生 Hook/OTLP ingest 使用独立随机本地 token。OTLP Exporter 默认只发送元数据。

## License

MIT.
