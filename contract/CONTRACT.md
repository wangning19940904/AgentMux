# AgentMux Public Integration Contract

本目录是 AgentMux 对外集成的**唯一事实来源**（single source of truth）：

| 文件 | 内容 |
| --- | --- |
| `openapi.yaml` | 对外 HTTP 契约（OpenAPI 3.1） |
| `schemas/*.json` | 由 Go 类型自动生成的 canonical JSON schema golden 文件 |
| `contract_golden_test.go` | 防漂移测试：Go 结构体改动必须同步更新 golden |
| `CONTRACT.md` | 本文档：版本策略、稳定性分级、接入路径 |
| `HOST_INTEGRATION.md` | 宿主应用的 BFF、SDK 与产品接入规范 |

官方 SDK 以本契约为准生成/校验：

- Python: [`sdk/python`](../sdk/python) — PyPI 包 `agentmux-sdk`（import `agentmux_sdk`）
- TypeScript: [`sdk/typescript`](../sdk/typescript) — npm 包 `agentmux-sdk`

## 契约版本（contract_version）

当前契约版本：**`3.0`**（常量定义于 [`contract.go`](contract.go)，由
`GET /api/v1/capabilities` 与 `GET /api/v1/status` 返回）。

`1.1` 新增多租户：Agent 实例与渠道新增可选归属字段、`/api/v1/tenancy/*`
端点、`tenancy` feature 项。全部为向后兼容新增，`>=1.0` 的 SDK 继续可用。

`1.2` 移除一次性注册码，`POST /api/v1/tenancy/register` 改为无需已有凭证的
tenant 自助注册。新 tenant 默认没有任何既有资源，访问权由管理员后续授予。

`1.3` 新增 Provider 资源授权：租户只能列出管理员显式授权的
Provider 与其活跃路由；`use` 及以上权限才能将 Provider 绑定到 Agent 或调用。

`2.0` 将 PostgreSQL 设为运行资源唯一事实来源：Invocation 与 Orchestration Task
只接受 `agent_id`，移除 `project` 字段、`X-AgentMux-Project` 和 model→project fallback；
`/api/v1/send` 只发送到明确的 Channel conversation。旧 config 项目先通过
`amux database import-config --dry-run|--apply` 迁移。

`2.1` 为 Agent 实例新增可选 `private_chat_mode`、`group_chat_mode`。
私聊支持 `chat`、`thread`、`group`；群聊支持 `chat-topic`、`new-topic`、`chat`。
旧 Agent 的空值继续使用渠道默认值，新 Agent 默认 `chat` / `chat-topic`。
会话内已设置的模式优先于 Agent 默认。旧客户端省略字段时保留已有设置。


`2.2` 新增本机事件订阅：`event_subscriptions` feature、事件源目录、订阅 CRUD、测试回调、签名密钥轮换和投递历史/重试 API。新增 `event_source` 授权与普通资源授权独立；共享或公开渠道不自动开放事件读取。详见 [事件接入指南](EVENT_SUBSCRIPTIONS.md)。

`2.3` 新增账号：`GET /api/v1/tenancy/self` 在 Console 账号会话下多返回可选
`account` 字段。Bearer 凭证（bridge token、tenant token）的行为不变，也不会带
`account`。

`3.0` 两项不兼容变更：

- 移除 LLM Provider 管理：Agent 实例不再有 `provider_tool`、`provider_id`、
  `provider_name` 字段，Provider、路由与本机代理相关端点全部移除。Provider 与路由
  由各 Agent 框架或用户自己的路由工具负责；AgentMux 通过
  `POST /api/v1/frameworks/test` 检查框架可用性。
- 移除多租户：`/api/v1/tenancy/*`、tenant token（`amxt_`）、资源授权、归属与
  公开/私有可见性全部移除；Agent 实例、渠道、触发器、编排与事件订阅不再有
  `owner_tenant_id`、`owner_tenant_name`、`visibility` 字段，`capabilities` 不再
  报告 `tenancy` feature，`auth.scope` 取值为 `admin` 或 `member`。第三方统一使用
  bridge token。

`contract_version` 与二进制版本**相互独立**：

- **minor 递增**（`2.0 → 2.1`）：向后兼容的新增——新端点、响应新增字段、新的
  `features` 项。旧 SDK 继续工作。
- **major 递增**（`2.x → 3.0`）：破坏性变更——删除/改名字段、修改语义、移除端点。
  必须提前一个 minor 版本在本文档声明弃用。

SDK 声明其支持的契约区间（当前 `>=3.0,<4.0`）。握手时服务端 major 不在区间内，
SDK 报告 `incompatible` 状态并拒绝继续。

## 稳定性分级

### stable — 破坏性变更必须升 contract major

| 端点 | 用途 |
| --- | --- |
| `GET /api/v1/capabilities` | 能力发现与握手（推荐的唯一健康探测端点） |
| `GET /api/v1/status` | 轻量存活检查 |
| `POST /api/v1/invocations` | 同步运行 Agent |
| `POST /api/v1/invocations/stream` | SSE 流式运行 Agent |
| `POST /api/v1/send` | 向活动 Channel conversation 发送出站消息（不运行 Agent） |
| `GET/POST/DELETE /api/v1/agent-instances` | Agent 实例读写 |
| `GET/POST/DELETE /api/v1/channels` | 渠道读写 |
| `POST /api/v1/console/sessions` + `GET /console/enter` | Console 会话嵌入 |

### beta — minor 内可调整，变更会记录在 Release Notes

| 端点 | 用途 |
| --- | --- |
| `GET/POST /api/v1/orchestrations`、`POST /api/v1/orchestrations/cancel` | 多 Agent DAG |
| `GET/POST/DELETE /api/v1/triggers`、`POST /api/v1/triggers/run` | 定时/Webhook/事件触发 |
| `GET /api/v1/event-sources`、`GET/POST/DELETE /api/v1/event-subscriptions` | 本机事件源与订阅 |
| `POST /api/v1/event-subscriptions/test`、`POST /api/v1/event-subscriptions/rotate-secret` | 回调测试与密钥轮换 |
| `GET /api/v1/event-deliveries`、`POST /api/v1/event-deliveries/retry` | 投递历史与重试 |
| `GET /api/v1/usage` | Token 用量报表 |
| `POST /hook/{id}` | 入站 Webhook（独立 token 鉴权） |

### OpenAI 兼容层

`/v1/responses` 与 `/v1/files` 遵循 [OpenAI Responses API](https://platform.openai.com/docs/api-reference/responses)
语义，差异（额外 Header `X-AgentMux-Agent-ID`、内存存储限制等）
记录在仓库 README「OpenAI Responses 兼容 API」章节。本契约不重复定义其 schema；
兼容层的稳定性跟随 OpenAI 规范 + README 声明。

### internal — 不对外承诺，随时可变

其余全部 `/api/v1/*` 路由（observability、remote proxy、tools、
frameworks、sessions、meetings、setup 自动化、tts、menubar、memory、skills、
mcp、guard、devices、accounts 等）是 Console 专用管理面。第三方
**不应**依赖它们；如确有需要，请提 issue 将其提升到 beta/stable。
`/api/v1/usage/sources*` 同样属于 Console internal API，不出现在公开 OpenAPI。

第三方使用 bridge token，等同管理员。

## 鉴权

| 主体 | 凭证 | 可见范围 |
| --- | --- | --- |
| 管理员 | `config.toml` 的 `[bridge].token`，或管理员账号的 Console 会话 | 全部路由、全部资源 |
| 成员账号 | Console 登录得到的 `agentmux_console` cookie（`amxs_` 会话） | 仅自己配对的设备、自己的私信与自己的密码，外加 `status`、`capabilities`；其余路由返回 `403` |

- `[bridge].enabled = true` 时，`/api/*` 与 `/v1/*` 要求
  `Authorization: Bearer <token>`；对外提供服务时必须开启。
- `[bridge].enabled = false` 时继续兼容无凭证的本机管理员访问；但请求一旦显式携带
  Console cookie，仍按该主体鉴权，不会回退成管理员。
- Console 会话 cookie（`agentmux_console`）是 Bearer 的等价凭证，通过
  `POST /api/v1/console/sessions`（服务端间调用，只接受 bridge token）签发，
  得到管理员 Console。
- `POST /hook/{id}` 使用触发器自带 token，不走 bridge。
- 账号（Console 专用）：账号登录、登出与管理端点（`/api/v1/auth/*`、
  `/api/v1/accounts*`）属于 Console 管理面，不在稳定性分级内。成员可见的路由见
  [`server/auth_policy.go`](../server/auth_policy.go) 的 `memberRoutePolicy`；
  未登记的路由默认拒绝，因此新增端点天然只对管理员开放。尚无账号时一切行为与以前相同。

## 统一状态机（SDK 语义）

SDK 的 `health()` 把探测结果收敛为 5 态：

| 状态 | 含义 | 判定 |
| --- | --- | --- |
| `ready` | 可用 | capabilities 200 且 contract major 兼容 |
| `unauthorized` | 服务在但凭证错/缺 | HTTP 401 |
| `incompatible` | 服务在但契约/版本不兼容 | contract major 不匹配，或版本低于消费方要求 |
| `unreachable` | 连不上，但本机检测到安装 | 连接失败 + 安装位置存在 |
| `missing` | 连不上且未安装 | 连接失败 + 未检测到安装 |

## 四类接入角色

1. **后端服务** — 装 `agentmux-sdk`（Python），用 `invoke()` / `invoke_stream()`。
2. **Web UI** — 装 `agentmux-sdk`（TypeScript/npm），经自家 BFF 转发，或在 Console
   会话 cookie 下直连。
3. **已有 OpenAI 生态的服务** — 不装 SDK，`base_url` 指向 `http://<host>:8765/v1`，
   `api_key` 填 bridge token，用标准 `responses.create`。
4. **宿主 App（负责安装/升级/拉起 AgentMux）** — 用
   `python -m agentmux_sdk.bootstrap`（或等价的 `scripts/ensure-agentmux.sh`）。

宿主应用的强制架构、原生能力面与验收清单见
[`HOST_INTEGRATION.md`](HOST_INTEGRATION.md)。

## SSE 流语义（invocations/stream）

- 事件类型：`started`、`thinking`、`tool_use`、`output`、`final`、`permission`、
  `model_request`、`model_response`、`compaction`、`completed`、`error`。
- **`output` / `thinking` 的 `text` 是全量快照**：客户端应替换已显示内容，
  不要逐条追加。
- 服务端每 15 秒发送 `: keepalive` 注释行，客户端必须忽略以 `:` 开头的行。
- `completed.result` 与同步接口响应结构一致。
- 同一目标 + 同一 `conversation_id` 并发调用返回 `409`。
- 请求体上限 1 MiB；附件最多 16 个、单个 25 MiB。

## 发布与安装约定

- 二进制、Python SDK、TypeScript SDK 共用 git tag 版本（`vX.Y.Z`）。
- Release assets：`agentmux_{os}_{arch}.tar.gz`（内含 `amux` + `agentmux-hook`）、
  `checksums.txt`（SHA-256）、`ensure-agentmux.sh`。
- 消费方校验下载完整性时使用 `checksums.txt`，**不要**自行维护 release manifest。

## 变更流程

1. 改 Go 类型 / 端点 → `go test ./contract/`（golden 漂移测试）会失败。
2. 运行 `go test ./contract/ -run TestContractGolden -update` 重新生成 golden。
3. 同步更新 `openapi.yaml`、SDK 模型、本文档的分级表。
4. 判断是否需要递增 `contract_version`（见上），在 `contract.go` 修改常量。
