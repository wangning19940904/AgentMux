# Homebook 事件接收示例

这是独立 FastAPI 示例，不修改 homebook 仓库或业务表。使用 SQLite inbox 演示验签、事务提交后确认、幂等与后台处理；实际接入 Homebook 时，将 inbox 操作改为已有 MariaDB 事务接口。

1. 管理员创建或复用服务租户，为 `channel:<id>` 授予 `event_source/use`。不要把管理员 Bridge Token 发给接收端。
2. 在虚拟环境中安装依赖（仓库根目录）：`pip install -e ./sdk/python fastapi uvicorn`。
3. 设置 `AGENTMUX_BASE_URL`、`AGENTMUX_TENANT_TOKEN`、`AGENTMUX_CHANNEL_ID`、`BITABLE_FILE_TOKEN`、`BITABLE_TABLE_ID`，运行 `python integrations/homebook-events/register.py`。首次注册输出一次签名密钥，将其配置为接收服务的 `AGENTMUX_EVENT_SECRET`，不要提交到仓库。
4. 在本目录运行 `uvicorn receiver:app --host 127.0.0.1 --port 8000 --workers 1`。若 Homebook 已占用 8000，先通过 `EVENT_CALLBACK_URL` 设置另一个回环端口再注册，并使用对应端口启动示例。
5. 在 AgentMux「事件订阅」测试回调；确认投递记录为 `sent`。关闭接收端后发送测试事件，恢复后会自动补投。

接收示例默认将数据库写到当前目录的 `event-inbox.sqlite3`，可用 `EVENT_INBOX_PATH` 改为持久目录。测试事件只记录、不更新业务。生产处理应将业务修改与 processed 标记放在同一事务；调用外部 API 时需额外的幂等键及对账。事件可以重复、乱序。

平台侧还需配置飞书事件类型、应用权限和目标表格订阅。接收服务不需要 App Secret，也不启动另一条飞书长连接。轮换签名密钥后立即更新接收端环境变量并重启；过渡期间认证失败会重试。

## 验证

从仓库根目录运行接收端测试：

```sh
uv run --project sdk/python --with fastapi --with pytest pytest integrations/homebook-events/test_receiver.py -q
```

完整端到端测试会自动启动两个临时接收端，注入飞书多维表格事件，停止其中一个再恢复，并验证两个 inbox 都只保存两条独立事件：

```sh
uv run --project sdk/python --with fastapi --with uvicorn python - <<'PY'
import os, subprocess, sys
raise SystemExit(subprocess.run(
    ['go', 'test', './platform/feishu', '-run', 'TestRelayFastAPIEndToEnd', '-v'],
    env={**os.environ, 'AGENTMUX_EVENT_TEST_PYTHON': sys.executable},
).returncode)
PY
```
