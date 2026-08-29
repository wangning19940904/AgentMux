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
| 多 Agent 编排 | **AgentMux Orchestrations** | `server/orchestrations.go` + `store/orchestrations.go` |

- **Connect** — 从消息平台(Feishu/Lark、Telegram、钉钉、Slack、Discord、通用 webhook;插件式扩展)与本地 AI 编码 Agent 对话;**渠道 & 触发**面板统一管理动态渠道、定时任务(cron)、入站 Webhook 与事件回调；飞书会议语音支持为每个渠道配置多个自定义唤醒词。
- **Router** — 支持 Claude Code、Codex CLI、Codex Desktop Thread、Cursor、Gemini、Qoder、OpenCode、TRAE CLI、iFlow、Kimi(插件式扩展),并在多 LLM Provider 间切换/故障转移；Codex Desktop Agent 会校验并固定恢复由 Desktop 创建的原生 Thread。TRAE CLI、bytedcli 与 CIS CLI 可通过 Console 或 `amux tools bundle install bytedance-internal` 一键安装，且仅适用于字节内部环境。
- **渠道 & 触发** — 渠道是绑定 Agent 的实时 IM 连接(飞书/Telegram/钉钉/Slack/Discord/Webhook),控制台可增删改与启停/重启并显示运行状态;触发统一承载三类自动化:定时任务(robfig/cron,标准 5 段表达式)、入站 Webhook(`POST /hook/{id}`,自带 token 鉴权)、生命周期事件回调(`message.received`/`cron.triggered`/`error` 等 → Shell 或 HTTP)。定时/Webhook 触发把 Prompt 发给绑定 Agent 并将结果推回渠道会话,支持 `reuse`/`new_per_run` 会话模式。
- **Ledger** — 读取 Claude/Codex/Cursor/Gemini 的本地会话日志,基于 LiteLLM 价格数据计费,按天/周/月/会话/5 小时块出账,并能通过 SSH 采集远程机器用量。
- **Observability** — 用统一 Trace 串联 Agent Turn、模型请求、重试、工具、Hook 与渠道回复；融合内部事件、Claude/Codex 原生 OTel、Proxy 和增量 Transcript，并生成只读优化建议。
- **Memory** — 跨 Agent 与跨会话的统一记忆层(检索、写入、共享上下文)。
- **Skills** — 统一发现、安装与管理 Agent Skills。
- **MCP Registry** — 注册、编排与下发 MCP Server 配置。
- **Guard** — 工具调用的权限审批与策略闸门。
- **Console** — React Web 控制台,内嵌进二进制,统一观测与操作以上模块。
- **SSH 远程控制** — 从 SSH Config 一键导入远程机器，验证 SSH 与 AgentMux 服务；服务缺失时自动上传匹配系统架构的 CLI 并启动，再通过 SSH 隧道复用 Console 管理其 Agent、Provider、渠道、Skills、MCP 等配置。
- **Interactive Sessions** — Agent 可选择结构化事件流或持久 tmux 后端；tmux 会话跨 daemon 重启存活，可在 Console 查看实时终端快照、输入命令或复制本地 attach 命令。
- **Worktree Isolation** — Agent/Project 可选择每 conversation 独立 Git worktree；渠道话题与 API conversation 使用确定性的 `agentmux/*` 分支，适合多个 Agent 并行改同一仓库。
- **Reliable Delivery & Feedback** — 所有渠道 turn 都写入持久任务状态，最终消息交付重试并记录 pending/sent/failed；成功的飞书最终卡支持三态答案反馈，Console 可聚合并补充负向原因。
- **Orchestrations** — 通过持久 DAG 跨 Agent/Project 分派任务，支持依赖输出传递、并发上限、失败阻断、取消和 daemon 重启恢复。

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

### 安装发布版

首个 Release 发布后，macOS/Linux 推荐通过 Homebrew 安装完整版本
（包含 Web Console 与原生 Observer Hook）：

```bash
brew install wangning19940904/tap/agentmux
```

也可以使用会校验 SHA-256 的通用安装脚本。默认安装到
`~/.local/bin`，可通过 `AMUX_INSTALL_DIR` 修改：

```bash
curl -fsSL https://raw.githubusercontent.com/wangning19940904/AgentMux/main/install.sh | sh

# 安装指定版本
curl -fsSL https://raw.githubusercontent.com/wangning19940904/AgentMux/main/install.sh \
  | sh -s -- v0.1.0
```

已经安装 Go 1.25+ 的开发者也可以安装精简 CLI。该方式不包含 React
Web Console 和 `agentmux-hook`，完整版本请使用 Homebrew 或 GitHub Release：

```bash
go install github.com/wangning19940904/AgentMux/cmd/amux@latest
```

所有平台的预编译包和 `checksums.txt` 位于
[GitHub Releases](https://github.com/wangning19940904/AgentMux/releases)。
发布维护说明见 [RELEASING.md](RELEASING.md)。运行时仍需 PostgreSQL 16+。

### 从源码构建

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
- `workspace_mode = "worktree"` 会为每个渠道话题/API conversation 创建确定性的同级 Git worktree 与 `agentmux/*` 分支；默认 `shared` 继续让所有会话使用 `work_dir`。可用 `worktree_base_ref` 指定新 worktree 的基准引用。
- `session_backend = "tmux"` 启用持久交互式 CLI，会话使用真实 tmux 进程并可跨 daemon 重启恢复；默认 `structured` 保留当前原生 JSON/app-server 事件流。
- 飞书渠道扫码取得 App ID/Secret 后，Console 可继续执行“补全开放平台配置”：二次扫码建立仅内存 Web session，自动导入消息/CardKit/会议权限、配置长连接事件与卡片回调，并按明确选择的可见范围发布版本。该能力使用飞书开放平台 Console 接口，接口变化时会 fail closed 并返回具体失败步骤。
- 开启 `[bridge]` 可用 Bearer Token 保护 HTTP send 与 Agent Invocation API；**启用时必须设置 token**。
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
amux tools bundle list
amux tools bundle install bytedance-internal [--yes]
amux usage [daily|weekly|monthly|session|blocks] [--since 7d] [--json] [--ssh]
amux usage statusline            # compact one-liner for status bars/hooks
amux provider list|presets|import <id>|switch <id> --tool <tool>
amux send --text "..." --project <name> [--token <bridge-token>]
```

## HTTP API(节选)

Console 与各客户端共用 `/api/v1`，其他服务也可以使用 OpenAI Responses 兼容入口：

```
POST /v1/responses                    # OpenAI Responses：同步或流式运行 Agent
GET  /v1/responses/{id}               # 查询/轮询已存储或 background Response
POST /v1/responses/{id}/cancel        # 取消 background Response
DELETE /v1/responses/{id}             # 删除已存储 Response
POST /v1/files                        # OpenAI Files multipart 上传
GET  /v1/files/{id}/content           # 下载上传文件
POST /api/v1/invocations             # 直接运行 Agent，并同步返回最终答案
POST /api/v1/invocations/stream      # 直接运行 Agent，并以 SSE 返回实时事件
GET  /api/v1/agent-instances         # 查询可用于 agent_id 的全部 Agent

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

POST /api/v1/orchestrations          # 创建并运行多 Agent DAG
GET  /api/v1/orchestrations?id=      # 查询编排与各任务输出
POST /api/v1/orchestrations/cancel   # 取消活动编排
GET  /api/v1/feedback                # 查询最终答案反馈与三态汇总
POST /api/v1/feedback/detail         # 本地 Console 补充反馈原因/备注
GET  /api/v1/sessions/terminal       # 读取托管 tmux 会话快照
POST /api/v1/sessions/terminal/input # 向托管终端写入/提交输入

GET  /api/v1/observability/traces              # Trace 列表与筛选
GET  /api/v1/observability/traces/{trace_id}   # Span/Event 时间线与授权解密内容
GET  /api/v1/observability/insights            # 仅建议的优化洞察
GET  /api/v1/observability/settings            # 保留期、密钥与 Exporter 状态
GET  /api/v1/observability/integrations        # Plugin/OTel/Transcript/Proxy Doctor
POST /api/v1/observability/integrations/{host}/{preview|install|repair|uninstall|doctor}
```

### OpenAI Responses 兼容 API

其他服务可以把 AgentMux 直接配置成 OpenAI SDK 的 `base_url`，然后通过标准 `responses.create` 调用。`model` 默认作为 Agent ID；如果没有同名 Agent，服务端会再尝试把它作为 `config.toml` 的 project 名称：

```python
import os
from openai import OpenAI

client = OpenAI(
    api_key=os.environ["BRIDGE_TOKEN"],
    base_url="http://127.0.0.1:8765/v1",
)

response = client.responses.create(
    model="agent-abc123",
    input="检查当前项目并运行测试",
)
print(response.output_text)
```

流式响应使用 Responses API 的标准类型化 SSE 事件，不使用 Chat Completions 的 `[DONE]`：

```python
stream = client.responses.create(
    model="agent-abc123",
    input="检查当前项目并运行测试",
    stream=True,
)

for event in stream:
    if event.type == "response.output_text.delta":
        print(event.delta, end="", flush=True)
    elif event.type == "response.completed":
        print()
```

继续同一段 Agent 上下文时，传入上次返回的 Responses ID：

```python
next_response = client.responses.create(
    model="agent-abc123",
    previous_response_id=response.id,
    input="根据刚才的结果修复失败项",
)
```

如果希望 `model` 保持业务自己的名称，可通过额外 Header 显式选择 AgentMux 目标；两个 Header 不能同时设置：

```python
# 指定 Agent ID
client.responses.create(
    model="my-service-agent",
    input="...",
    extra_headers={"X-AgentMux-Agent-ID": "agent-abc123"},
)

# 指定 config.toml project
client.responses.create(
    model="my-service-agent",
    input="...",
    extra_headers={"X-AgentMux-Project": "demo"},
)
```

支持请求级 function tools，并返回标准 `function_call` output item；调用方执行函数后，再提交 `function_call_output`：

```python
response = client.responses.create(
    model="agent-abc123",
    input="巴黎天气怎么样？",
    tools=[{
        "type": "function",
        "name": "get_weather",
        "description": "查询城市天气",
        "parameters": {
            "type": "object",
            "properties": {"city": {"type": "string"}},
            "required": ["city"],
            "additionalProperties": False,
        },
        "strict": True,
    }],
    tool_choice="required",
)

call = response.output[0]
response = client.responses.create(
    model="agent-abc123",
    previous_response_id=response.id,
    input=[{
        "type": "function_call_output",
        "call_id": call.call_id,
        "output": '{"temperature": 22, "unit": "celsius"}',
    }],
)
```

多模态输入支持 `input_image` 的 HTTP(S)/data URL，以及 `input_file` 的 `file_url`、`file_data` 和 AgentMux Files API 的 `file_id`。Codex app-server Agent 原生接收图片和逐次 output schema；其他 Agent 会收到安全临时文件的路径：

```python
uploaded = client.files.create(file=open("report.pdf", "rb"), purpose="user_data")
response = client.responses.create(
    model="agent-abc123",
    input=[{
        "role": "user",
        "content": [
            {"type": "input_text", "text": "总结报告"},
            {"type": "input_file", "file_id": uploaded.id},
        ],
    }],
)
```

结构化输出支持 `json_object` 和 `json_schema`；完成前会解析并校验最终 JSON，不符合 schema 时返回 `output_validation_failed`：

```python
response = client.responses.create(
    model="agent-abc123",
    input="提取姓名和年龄",
    text={"format": {
        "type": "json_schema",
        "name": "person",
        "strict": True,
        "schema": {
            "type": "object",
            "properties": {"name": {"type": "string"}, "age": {"type": "integer"}},
            "required": ["name", "age"],
            "additionalProperties": False,
        },
    }},
)
```

长任务可用 background mode 创建后轮询、取消；`background=True, stream=True` 也会记录 `sequence_number`，断线后可用 `GET /v1/responses/{id}?stream=true&starting_after=N` 续传：

```python
import time

response = client.responses.create(
    model="agent-abc123",
    input="执行完整代码审计",
    background=True,
)
while response.status in {"queued", "in_progress"}:
    time.sleep(1)
    response = client.responses.retrieve(response.id)
```

兼容范围还包括：字符串/消息数组输入、`instructions`、同步 Response、标准类型化 SSE、`previous_response_id`/`conversation`、stored Response 的 retrieve/delete/input-items、Files create/list/retrieve/content/delete、usage 和 OpenAI 格式错误。请求体上限 32 MiB，单个内联或上传附件上限 25 MiB。

请求中的 hosted tools（如 `web_search`、`file_search`、`code_interpreter`、`image_generation`、`mcp`）会按原结构接收、回显，并要求 Agent 使用自身已配置的等价工具。AgentMux 不会凭 OpenAI 的 vector store ID 或 hosted container ID 自动获得 OpenAI 云端资源；要实现同等语义，需要给目标 Agent 配置对应的 Web、文件检索、代码执行或 MCP 工具。`temperature`、`top_p`、`reasoning` 和 `max_output_tokens` 仍按协议接收和回显，实际模型参数由 AgentMux Agent 配置决定。

当前 stored Response、background 事件日志和 Files 对象保存在 AgentMux 进程内存中，客户端断线不会中止 background 任务，但 AgentMux 进程重启后这些 API 对象不会保留；AgentMux 自身的 Agent conversation/session 仍按原机制持久化。

`/v1/responses` 与 `/api/v1` 使用同一套 `[bridge]` Bearer Token。对外提供服务时务必开启 Token，并配合防火墙或反向代理限制来源。

### Agent Invocation API

其他本机服务不需要伪装成消息渠道，也不需要先配置 IM/Webhook Channel。直接调用独立的 Invocation API 即可复用 AgentMux 的 Agent 配置、工作目录、Provider 路由、系统提示词、会话持久化和 Observability：

`/api/v1/send` 仍只负责向已有渠道发送出站消息，不会运行 Agent；需要 Agent 产出结果时使用 `/api/v1/invocations`。即使只监听 `127.0.0.1`，服务间调用也建议开启 `[bridge]` Bearer Token 鉴权。

```bash
# 任意 Agent（含 config.toml 项目）：agent_id 来自 GET /api/v1/agent-instances
curl -sS http://127.0.0.1:8765/api/v1/invocations \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $BRIDGE_TOKEN" \
  -d '{
    "agent_id": "agent-abc123",
    "input": "检查当前项目并运行测试"
  }'

# config.toml 中的 [[projects]]：使用 project 名称
curl -sS http://127.0.0.1:8765/api/v1/invocations \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $BRIDGE_TOKEN" \
  -d '{"project":"demo","input":"总结最近的改动"}'
```

成功响应：

```json
{
  "id": "inv_...",
  "agent_id": "agent-abc123",
  "conversation_id": "conv_...",
  "session_id": "...",
  "answer": "...",
  "duration_ms": 1234
}
```

流式调用使用相同请求体，响应格式为 Server-Sent Events：

```bash
curl -N http://127.0.0.1:8765/api/v1/invocations/stream \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -H "Authorization: Bearer $BRIDGE_TOKEN" \
  -d '{"agent_id":"agent-abc123","input":"检查当前项目并运行测试"}'
```

```text
event: started
data: {"type":"started","invocation_id":"inv_...","conversation_id":"conv_..."}

event: output
data: {"type":"output","text":"当前完整答案快照..."}

event: completed
data: {"type":"completed","final":true,"result":{"answer":"最终答案..."}}
```

事件类型包括 `started`、`thinking`、`tool_use`、`output`、`final`、`permission`、`model_request`、`model_response`、`compaction`、`completed` 和 `error`。`output`/`thinking` 的 `text` 是当前完整快照，客户端应替换已有显示内容，不要逐条追加；`completed.result` 与同步接口响应结构一致。服务端每 15 秒发送 SSE keepalive 注释，客户端应忽略以 `:` 开头的行。

`agent_id` 与 `project` 必须且只能提供一个。首次调用可省略 `conversation_id`；要继续同一段上下文，把 `started` 事件或最终响应中的 `conversation_id` 原样传给后续请求。同步调用请让上游 HTTP 客户端设置足够长的超时；同一目标、同一 `conversation_id` 同时只能运行一个请求，冲突返回 `409`。请求体上限为 1 MiB。

Invocation API 没有消息渠道可承载交互式审批；如果 Agent 在执行中请求人工审批，当前调用会安全地拒绝该请求。服务化 Agent 应按自身安全策略配置适合无人值守执行的 approval mode。

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

## 集成（契约与 SDK）

对外集成契约的唯一事实来源在 [`contract/`](contract/)：OpenAPI 3.1 规范
（[openapi.yaml](contract/openapi.yaml)）、版本策略与稳定性分级
（[CONTRACT.md](contract/CONTRACT.md)），以及由 Go 类型自动生成、CI 防漂移的
golden schema。当前 `contract_version` 为 `1.2`，由
`GET /api/v1/capabilities`（推荐的唯一握手/探活端点）返回。

四类接入角色，各有一条推荐路径：

| 角色 | 方式 |
| --- | --- |
| 后端服务 | Python SDK [`agentmux-sdk`](sdk/python/)（import `agentmux_sdk`），`invoke()` / `invoke_stream()` |
| Web UI | TypeScript SDK [`agentmux-sdk`](sdk/typescript/)（npm），经自家 BFF 转发或用 Console 会话 cookie |
| 已有 OpenAI 生态 | 不装 SDK，`base_url` 指向 `http://<host>:8765/v1`，直接用 `/v1/responses` |
| 宿主 App（装/升/拉起 AgentMux） | `python -m agentmux_sdk.bootstrap`，或 release asset 里的 [`ensure-agentmux.sh`](scripts/ensure-agentmux.sh) |

```python
from agentmux_sdk import AgentMuxClient

client = AgentMuxClient(token="<bridge token>")
print(client.health().state)   # ready | unauthorized | incompatible | unreachable | missing
for event in client.invoke_stream(agent_id="agent-abc", input="分析这份数据"):
    ...
```

宿主应用嵌入 Console 时，不要把 bridge token 交给浏览器：由宿主后端调用
`POST /api/v1/console/sessions` 换取一次性 `enter_url`（约 60 秒有效、单次使用），
浏览器访问后获得 HttpOnly 会话 cookie 并落在 Console 首页。

宿主页面不应只提供 Console 跳转。统一使用
[`contract/HOST_INTEGRATION.md`](contract/HOST_INTEGRATION.md) 定义的 BFF + tenant
token 架构：管理面优先把一次性 Console session 嵌入 sandboxed iframe，场景化动作
再通过 `client.integration.snapshot()` / `invoke_stream()` 原生实现。

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
