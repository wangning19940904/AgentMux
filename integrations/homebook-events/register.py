"""Explicit setup helper. Prints a newly generated signing secret exactly once."""

import os
from agentmux_sdk import AgentMuxClient

with AgentMuxClient(
    base_url=os.environ.get("AGENTMUX_BASE_URL", "http://127.0.0.1:8765"),
    token=os.environ["AGENTMUX_TENANT_TOKEN"],
) as client:
    if not client.capabilities().supports("event_subscriptions"):
        raise SystemExit("AgentMux requires contract 2.2 event_subscriptions")
    result = client.events.upsert(
        {
            "key": "homebook-bitable",
            "name": "Homebook 表格事件",
            "source": "feishu",
            "source_refs": ["channel:" + os.environ["AGENTMUX_CHANNEL_ID"]],
            "event_types": ["drive.file.bitable_record_changed_v1"],
            "filters": {
                "file_token": [os.environ["BITABLE_FILE_TOKEN"]],
                "table_id": [os.environ["BITABLE_TABLE_ID"]],
            },
            "callback_url": os.environ.get(
                "EVENT_CALLBACK_URL",
                "http://127.0.0.1:8000/api/v1/integrations/agentmux/events",
            ),
            "paused": False,
        }
    )
    print("subscription_id=" + result.subscription.id)
    if result.signing_secret:
        print("Save this value as AGENTMUX_EVENT_SECRET in the receiver environment:")
        print(result.signing_secret)
