# Codex ephemeral 用量接入

AgentMux 自动扫描 `$CODEX_HOME/sessions`、`archived_sessions` 以及 `usage/agentmux-exec`。`CODEX_HOME` 未设置时使用 `~/.codex`。扫描间隔为 30 秒。

普通 Codex CLI 和桌面 App 使用 rollout 中的累积 Token 差值。`codex exec --ephemeral --json` 的调用方需要把 `thread.started.thread_id` 与每个 `turn.completed.usage` 保存为回执；只保留计数和来源元数据，不需要保存提示词或模型回答。rookie-trade 的 `codex_usage.py` 实现了该协议，包括成功、业务输出校验失败和超时前已完成的轮次。

每个完成轮次写一个 JSON 文件，先写隐藏临时文件，再原子重命名为 `.json`，建议权限 0600：

```json
{
  "schema": "agentmux.codex-exec-usage.v1",
  "ephemeral": true,
  "invocation_id": "unique-id-for-this-process-invocation",
  "session_id": "thread.started.thread_id",
  "turn_index": 0,
  "timestamp": "2026-10-14T13:00:00Z",
  "model": "the-requested-model",
  "project": "/absolute/project/path",
  "usage": {
    "input_tokens": 100,
    "cached_input_tokens": 60,
    "cache_write_input_tokens": 0,
    "output_tokens": 20
  }
}
```

`invocation_id` 在一次调用内稳定，每次重试重新生成；`turn_index` 从 0 递增。同一回执重放不会增加用量，不同调用或轮次即使时间戳相同也分别保存。仅接受 ephemeral 回执，防止与普通 rollout 双算。

总 Token 为 input + output。缓存读取及写入属于 input 的子集，从非缓存输入中扣除后单独展示；reasoning_output_tokens 已含在 output 中，不再累加。每份回执的粒度是一次完成的 CLI 轮次，可能包含多个模型请求，不能据此恢复精确的模型请求次数。时间使用调用方观察到的完成时间；rookie-trade 在子进程返回时保存。模型为调用方指定模型，不能证明服务端未做模型路由。

缺少 `turn.completed.usage` 的失败、被强杀的进程，以及接入前未保存 stdout 的成功调用，均属于未覆盖用量，不记录为零，也不通过工作条数推算 Token。回执没有凭据，不需要 AgentMux 在线即可落盘；AgentMux 恢复后会补采。

来源分类使用原始 session_meta.originator。明确的 CLI、Desktop、IDE、AgentMux 分别展示，无法识别的来源保留未知。历史 `agentmux.internal` 请求在报表中归为 AgentMux 内部调用，原始账本保留用于核对。
