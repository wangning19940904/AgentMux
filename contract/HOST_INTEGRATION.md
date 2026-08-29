# AgentMux 宿主应用接入规范

本规范定义 Homebook、rookie-trade 以及后续宿主应用如何在自己的产品界面内接入
AgentMux。它与 [`CONTRACT.md`](CONTRACT.md) 一起构成对外接入的事实来源：
`CONTRACT.md` 规定 HTTP/权限契约，本文规定宿主架构和产品行为。

## 1. 标准架构

```text
宿主前端 → 宿主后端（BFF）→ 官方 agentmux-sdk → AgentMux
                              └→ tenant token（仅服务端）
```

必须遵守：

1. 浏览器不得持有 bridge token 或 tenant token；注册响应中的 token 必须由 BFF
   立即安全持久化，不得回传浏览器、记录日志或发送到其他目的地。
2. 每个宿主应用使用独立 tenant；不得把实例级 admin token 分发给业务应用。
3. 宿主后端必须通过官方 SDK 调用 stable/beta 能力，不得手写内部 `/api/v1/*`
   请求或依赖 Console 私有接口。
4. 管理型宿主优先使用一次性 Console session 将完整 AgentMux UI 嵌入 iframe，既不
   跳页也不重复实现管理表单；只有强场景化动作才使用 SDK 构建原生组件。

## 2. 启动与身份

应用首次部署由宿主 BFF 调用 `client.tenancy.register(name, kind=...)` 自助注册，
并把返回的 tenant token 以 `0600` 文件、Secret Manager 或等价安全存储持久化。
每次启动按以下顺序执行：

1. `client.health()`：判断 ready / unauthorized / incompatible / unreachable / missing。
2. `client.tenancy.self()`：确认返回的 tenant 名称与本应用一致。
3. `client.integration.snapshot()`：加载当前 tenant 可见的 Agent、渠道、触发器和编排。

若身份不匹配，必须停止业务调用并向运维报告；不得自动回退到 admin token。

## 3. 宿主原生能力面

推荐由宿主后端暴露一个只含安全摘要的 BFF endpoint，例如
`GET /integrations/agentmux/workspace`。数据源统一使用：

```python
async with AsyncAgentMuxClient(base_url=base_url, token=tenant_token) as client:
    snapshot = await client.integration.snapshot(orchestration_limit=8)
```

宿主页面至少应呈现：

- 当前 tenant、AgentMux/contract 版本和 feature flags；
- tenant 可见的 Agent 及启用状态；
- 渠道与绑定状态；
- 触发器及“立即运行”操作；
- 最近编排状态；
- 选择 Agent 后直接执行场景化任务。

不采用嵌入式 Console、选择自行实现管理面的宿主，必须通过 SDK 暴露：

- tenant 自助注册（token 由 BFF 持久化，绝不回传浏览器）；
- `client.agents.upsert/delete` 对 Agent 进行创建、编辑和删除；
- `client.channels.upsert/delete/restart` 管理渠道及其运行状态；
- 渠道敏感配置使用服务端返回的 `<redacted>` 占位回填，不在宿主日志中展开。

写操作仍由 AgentMux 服务端按 tenant 归属和 grant 级别强制鉴权，宿主前端的按钮禁用
不是安全边界。

## 4. Agent 调用

宿主后端使用 `invoke()` 或 `invoke_stream()`，前端只访问自己的 BFF：

```python
async for event in client.invoke_stream(
    agent_id=agent_id,
    input=prompt,
    conversation_id=conversation_id,
):
    yield event
```

- `agent_id` 必须来自 `integration.snapshot().agents`，不得接受任意跨 tenant ID。
- `thinking` / `output` 的 `text` 是全量快照，前端应替换显示，不得追加。
- 复用对话时保存并传回 `conversation_id`；新任务不应误用旧会话。
- 保留 `tool_use`、`permission`、`error`、`completed`，不要把事件流压扁成只有文本。

## 5. 嵌入式 Console

需要完整管理能力时，推荐宿主后端签发 session，宿主前端直接嵌入：

- 后端调用 `client.console.create_session(landing="tenants")`；
- 前端把一次性 `enter_url` 作为 iframe `src`，不得把 bridge/tenant token 放进 URL；
- iframe 使用 `sandbox`，至少允许 scripts、same-origin、forms、popups；
- 本地开发必须统一 `localhost` / `127.0.0.1` 主机名，避免 SameSite cookie 被当作第三方；
- 生产环境应把宿主和 Console 部署在同一 schemeful site，或通过宿主反向代理 Console。

Console session 继承 tenant 范围，嵌入后可直接完成 tenant、Agent、渠道、Provider、
Skills、MCP、Trace 等完整操作。可保留“新窗口打开”作为可访问性/故障排查后备，但
不得作为主要交互。

## 6. 降级和版本策略

- 只以 `capabilities` 和 feature flags 判断能力，不通过版本号猜测端点。
- contract major 不兼容时禁止调用；minor 新增字段必须向前兼容。
- workspace 某个 beta 资源失败时，可降级隐藏该区块，但 Agent 调用失败必须显式展示。
- GitHub Release 查询或升级失败不得影响已运行的 AgentMux 业务能力。
- SDK、二进制和本文档随同一个 AgentMux release 发布；宿主应用不得复制 SDK 代码。

## 7. 接入验收清单

- [ ] tenant token 只存在宿主后端；浏览器请求和日志均不包含它。
- [ ] `health()` 与 `tenancy.self()` 在启动/状态页可观测。
- [ ] 页面嵌入 tenant-scoped Console，或通过 `integration.snapshot()` 实现原生管理面。
- [ ] 不跳页即可完成 tenant、Agent 和渠道管理。
- [ ] 至少支持一个 SDK 原生动作（Agent 调用或触发器运行）。
- [ ] SSE 按快照语义渲染，错误与权限事件可见。
- [ ] Console 使用一次性 session，token 不进入浏览器；iframe 具备 sandbox 与同站部署策略。
- [ ] unauthorized / incompatible / unreachable / missing 均有明确降级 UI。
- [ ] 后端和前端都有 tenant 隔离、SDK 快照与调用流程测试。
