# AgentMux Public Integration Contract

本目录是 AgentMux 对外集成的**唯一事实来源**（single source of truth）：

| 文件 | 内容 |
| --- | --- |
| `openapi.yaml` | 对外 HTTP 契约（OpenAPI 3.1） |
| `schemas/*.json` | 由 Go 类型自动生成的 canonical JSON schema golden 文件 |
| `contract_golden_test.go` | 防漂移测试：Go 结构体改动必须同步更新 golden |
| `CONTRACT.md` | 本文档：版本策略、稳定性分级、接入路径 |
| `HOST_INTEGRATION.md` | 宿主应用的 BFF、SDK、tenant 与产品接入规范 |

官方 SDK 以本契约为准生成/校验：

- Python: [`sdk/python`](../sdk/python) — PyPI 包 `agentmux-sdk`（import `agentmux_sdk`）
- TypeScript: [`sdk/typescript`](../sdk/typescript) — npm 包 `agentmux-sdk`

## 契约版本（contract_version）

当前契约版本：**`1.3`**（常量定义于 [`contract.go`](contract.go)，由
`GET /api/v1/capabilities` 与 `GET /api/v1/status` 返回）。

`1.1` 新增多租户：Agent 实例与渠道新增可选归属字段、`/api/v1/tenancy/*`
端点、`tenancy` feature 项。全部为向后兼容新增，`>=1.0` 的 SDK 继续可用。

`1.2` 移除一次性注册码，`POST /api/v1/tenancy/register` 改为无需已有凭证的
tenant 自助注册。新 tenant 默认没有任何既有资源，访问权由管理员后续授予。

`1.3` 新增 Provider 资源授权：租户只能列出管理员显式授权的
Provider 与其活跃路由；`use` 及以上权限才能将 Provider 绑定到 Agent 或调用。

`contract_version` 与二进制版本**相互独立**：

- **minor 递增**（`1.0 → 1.1`）：向后兼容的新增——新端点、响应新增字段、新的
  `features` 项。旧 SDK 继续工作。
- **major 递增**（`1.x → 2.0`）：破坏性变更——删除/改名字段、修改语义、移除端点。
  必须提前一个 minor 版本在本文档声明弃用。

SDK 声明其支持的契约区间（当前 `>=1.0,<2.0`）。握手时服务端 major 不在区间内，
SDK 报告 `incompatible` 状态并拒绝继续。

## 稳定性分级

### stable — 破坏性变更必须升 contract major

| 端点 | 用途 |
| --- | --- |
| `GET /api/v1/capabilities` | 能力发现与握手（推荐的唯一健康探测端点） |
| `GET /api/v1/status` | 轻量存活检查 |
| `POST /api/v1/invocations` | 同步运行 Agent |
| `POST /api/v1/invocations/stream` | SSE 流式运行 Agent |
| `POST /api/v1/send` | 向渠道/项目发送出站消息（不运行 Agent） |
| `GET/POST/DELETE /api/v1/agent-instances` | Agent 实例读写 |
| `GET/POST/DELETE /api/v1/channels` | 渠道读写 |
| `POST /api/v1/console/sessions` + `GET /console/enter` | Console 会话嵌入 |

### beta — minor 内可调整，变更会记录在 Release Notes

| 端点 | 用途 |
| --- | --- |
| `GET/POST /api/v1/orchestrations`、`POST /api/v1/orchestrations/cancel` | 多 Agent DAG |
| `GET/POST/DELETE /api/v1/triggers`、`POST /api/v1/triggers/run` | 定时/Webhook/事件触发 |
| `GET /api/v1/usage` | Token 用量报表 |
| `POST /hook/{id}` | 入站 Webhook（独立 token 鉴权） |
| `POST /api/v1/tenancy/register`、`GET /api/v1/tenancy/self` | 租户自助注册与自查（见「多租户」） |

### OpenAI 兼容层

`/v1/responses` 与 `/v1/files` 遵循 [OpenAI Responses API](https://platform.openai.com/docs/api-reference/responses)
语义，差异（额外 Header `X-AgentMux-Agent-ID` / `X-AgentMux-Project`、内存存储限制等）
记录在仓库 README「OpenAI Responses 兼容 API」章节。本契约不重复定义其 schema；
兼容层的稳定性跟随 OpenAI 规范 + README 声明。

### internal — 不对外承诺，随时可变

其余全部 `/api/v1/*` 路由（observability、remote proxy、providers、tools、
frameworks、sessions、meetings、setup 自动化、tts、menubar、memory、skills、
mcp、guard、`/api/v1/tenancy/*` 的管理端点等）是 Console 专用管理面。第三方
**不应**依赖它们；如确有需要，请提 issue 将其提升到 beta/stable。

**自 `1.1` 起这一分级是强制执行的**：租户凭证只能访问上面 stable / beta 两层
（外加若干只读目录：按授权过滤的 `providers`、`providers/active`，
以及全局能力目录 `tools`、`frameworks`），访问 internal 层返回 `403`。策略表见
[`server/tenancy_auth.go`](../server/tenancy_auth.go) 的 `tenantRoutePolicy`；
未登记的路由默认拒绝，因此新增 internal 端点天然对租户关闭。

## 鉴权

两类主体：

| 主体 | 凭证 | 可见范围 |
| --- | --- | --- |
| 管理员 | `config.toml` 的 `[bridge].token` | 全部路由、全部资源（并带归属标注） |
| 租户 | `/api/v1/tenancy/*` 签发的 token（`amxt_` 前缀） | stable/beta 路由；自己的 ∪ 公共的 ∪ 被授权的资源 |

- `[bridge].enabled = true` 时，`/api/*` 与 `/v1/*` 要求
  `Authorization: Bearer <token>`；对外提供服务时必须开启。
- `[bridge].enabled = false` 时继续兼容无凭证的本机管理员访问；但请求一旦显式携带
  tenant token 或 Console cookie，仍会按该主体做权限隔离，不会回退成管理员。
- Console 会话 cookie（`agentmux_console`）是 Bearer 的等价凭证，通过
  `POST /api/v1/console/sessions`（服务端间调用）签发，**并继承签发者的主体**：
  用租户 token 签发的会话，其 Console 只能看到该租户的资源；租户签发不要求
  全局 `[bridge]` 鉴权已开启。
- `POST /hook/{id}` 使用触发器自带 token，不走 bridge。

## 多租户

一个 AgentMux 实例可被多个宿主应用共享。每个应用是一个**租户**，它创建的
Agent 实例与渠道归它所有，同侪不可见。

**接入流程**

新应用无需已有凭证即可调用 `POST /api/v1/tenancy/register`（请求体
`{"name": "...", "kind": "app|web|service"}`），立即得到 tenant 与首个 token。
tenant 名唯一，重名返回 `409` 而不是轮换既有凭证，因此无法接管已有 tenant。

新 tenant 默认只有自己的空私有空间，不会继承任何管理员或其他 tenant 的资源。
管理员随后通过 grant 或 ownership 明确授予 Agent、渠道、触发器和
Provider 访问权。Provider 始终是实例级凭证资源，不属于任何 tenant，只能通过
显式 grant 访问。

任一方式接入后，应用可用 `GET /api/v1/tenancy/self` 读回自己的身份。

**可见性**

租户可见集合 = `owner_tenant_id` 为自己 ∪ `visibility = "public"` ∪ 被显式授权。
Provider 没有 ownership/public 捷径，其可见集合只是被显式授权的 Provider。
归属为空的资源（早于多租户创建的）视为未分配，**仅管理员可见**，可用
`amux tenants assign` 或 `POST /api/v1/tenancy/ownership` 移交。

**授权级别**（`read` < `use` < `manage`）

| 级别 | 含义 |
| --- | --- |
| `read` | 出现在列表中、可读取 |
| `use` | 可调用 / 可发送（`invocations`、`send`、`triggers/run`） |
| `manage` | 可修改、可删除 |

资源所有者对自己的资源隐含 `manage`；`visibility = "public"` 隐含 `use`。
租户写入时归属字段会被强制改写为调用方自身，也不能把资源改为 public。

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
