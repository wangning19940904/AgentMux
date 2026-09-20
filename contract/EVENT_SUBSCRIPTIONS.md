# 本机事件订阅（Contract 2.2，beta）

AgentMux 维护飞书/Lark 长连接，将原始平台事件和 `HookEvent` 生命周期事件存入 PostgreSQL，再向本机服务发出 HTTP POST。`GET /api/v1/capabilities` 的 `event_subscriptions` 表示接口可用。

## 注册

每个服务使用自己的租户 Token。管理员通过现有 `/api/v1/tenancy/grants` 为共享资源授予 `resource_type=event_source`、`resource_id=channel:<id>`、`level=use`。拥有资源的租户自动拥有其事件读取权。普通渠道授权和 public 可见性不开放事件。`system` 只对管理员开放。

```json
{
  "key": "homebook-bitable",
  "name": "Homebook 表格事件",
  "source": "feishu",
  "source_refs": ["channel:ch_example"],
  "event_types": ["drive.file.bitable_record_changed_v1"],
  "filters": {"file_token": ["bascn_example"], "table_id": ["tbl_example"]},
  "callback_url": "http://127.0.0.1:8000/api/v1/integrations/agentmux/events",
  "paused": false
}
```

向 `POST /api/v1/event-subscriptions` 发送此配置。`key` 在租户内唯一，重复提交更新同一订阅。返回 `{subscription, signing_secret?}`；密钥仅创建时返回。ID、归属和 key 创建后不能更改；删除后 key 保留，需要新 key 才能创建新订阅。

生命周期事件的唯一授权来源依次取渠道、触发器、Agent，无法确定时归入管理员专属 `system`；绑定渠道的 Agent 事件应订阅渠道来源。

来源为 `feishu/lark/agentmux`，事件类型使用原生名称，首期使用精确匹配。`source_refs` 和事件类型各自按 OR 匹配；`filters` 支持 `chat_id/file_token/table_id/agent_id`，字段之间 AND、每个字段内的值 OR。无属性不匹配。

配置平台事件后，连接处理器集合会在约 3 秒内更新；只重建 WebSocket，不重建 Agent 会话。平台权限、开放平台事件配置及目标多维表格的云文档事件订阅仍需单独完成。`platform_verified=false` 不能当作失败，也不能当作已开通。未到达 AgentMux 的事件无法由本地转发保证。

## 接收与签名

请求为 `Content-Type: application/json`，载荷为 `RelayEvent`：`schema_version/id/source/type/source_ref/source_event_id/occurred_at/received_at/attributes/data`。飞书 `data` 保留原始事件结构但移除认证 token；生命周期 `data` 是原有 Hook 字段。飞书 card.action.trigger 的卡片更新 token 也会被移除。

请求头：

- `X-AgentMux-Event-ID`：事件 ID，接收方以此幂等。
- `X-AgentMux-Delivery-ID`：订阅内投递 ID，自动及手动重试保持相同。
- `X-AgentMux-Timestamp`：本次请求的 Unix 秒。
- `X-AgentMux-Signature`：`v1=` 加 HMAC-SHA256 十六进制小写。

签名使用返回的密钥字符串作为 UTF-8 字节，输入为 `timestamp + "." + delivery_id + "." + 原始请求体`。验证 5 分钟时钟窗口，然后验证签名，最后才解析 JSON。Python SDK 提供 `verify_event_signature`，TypeScript 提供 `verifyEventSignature`（Web Crypto）。重试会使用新的时间戳和签名。

接收服务应在本地事务中写入带事件 ID 唯一键的 inbox，提交后返回 `202`；重复事件直接返回 `2xx`。异步处理业务，不能先返回成功再只存入内存。原始字段值可能包含个人数据，接收服务自行控制日志。

回调只支持同机同网络空间的 HTTP(S) 回环地址，禁止 URL 凭证、重定向和代理。签名密钥在 AgentMux 的订阅表中按已有渠道凭证模式保存，API 列表、日志和前端持久存储不会返回/保存密钥；应限制数据库访问及备份权限。

## 投递状态与故障

`pending → delivering → sent/retry/dead`；无事件读取权时进入 `blocked`，删除订阅后未完成投递变为 `cancelled`。暂停保留新事件但停止投递。已经在途的 HTTP 请求不能撤回；撤权、停用及轮换作用于后续请求。

投递并发为全局 8、每订阅 1，超时 5 秒，租约 30 秒。失败采用指数退避（约 1 秒到 5 分钟），首次尝试满 24 小时进入死信；失败等待期间其他事件可以先投递，不保证业务顺序。手动重试重开 24 小时窗口并保留尝试历史。投递详情最多返回最近 200 次尝试。

已提交的事件与投递记录可跨重启恢复，语义是至少一次。飞书按平台、应用、事件类型、上游事件 ID 在记录保留期间去重；没有上游 ID 的通知不能保证上游重推去重。新订阅不补已存在事件。

普通飞书通知落库失败会返回错误请求上游重推；生命周期事件、同步卡片转发失败不阻断已有任务/卡片响应。接入最多等待 500 毫秒，缺口通过日志和事件源 `ingestion` 的进程内失败计数/时间暴露。该计数重启后重置；这不是业务状态与事件的事务 outbox。

成功记录保留 7 天，死信/取消记录保留 30 天；尚未完成记录不按年龄清理。每小时清理失去全部投递引用的事件。长期暂停可能持续积压，应监控数据库空间。

## 管理 API

- `GET /api/v1/event-sources`：当前主体可订阅来源与健康状态。
- `GET/POST/DELETE /api/v1/event-subscriptions`：列表、完整配置 upsert、`?id=` 删除。
- `POST /api/v1/event-subscriptions/test`：`{"id":"..."}`，返回 202 仅代表测试事件已入队。测试类型为 `agentmux.subscription.test`，`data.test=true`，接收服务应直接确认，不作业务处理。
- `POST /api/v1/event-subscriptions/rotate-secret`：`{"id":"..."}`，新密钥只返回一次；接收端应同步更新。
- `GET /api/v1/event-deliveries?subscription_id=&status=&limit=25&offset=0`：分页 `{items,limit,offset,has_more}`；传 `id` 返回含载荷及尝试记录的单条详情。撤权后列表可查看状态，但载荷隐藏，详情返回 403。
- `POST /api/v1/event-deliveries/retry`：`{"id":"..."}`，重试 dead/retry/blocked 记录。

SDK 使用 `client.events` 提供上述方法。完整接收样例见 [homebook 示例](../integrations/homebook-events/README.md)。
