"""Official Python SDK for the AgentMux control plane.

Distribution name: ``agentmux-sdk`` (the PyPI name ``agentmux`` belongs to an
unrelated project, hence the ``agentmux_sdk`` import name).

Quick start::

    from agentmux_sdk import AgentMuxClient

    client = AgentMuxClient(token="<bridge token>")
    print(client.health().state)
    result = client.invoke(agent_id="agent-abc", input="run the tests")
"""

from .client import (
    DEFAULT_BASE_URL,
    AgentMuxClient,
    AsyncAgentMuxClient,
)
from .errors import (
    AgentMuxAPIError,
    AgentMuxBusy,
    AgentMuxError,
    AgentMuxIncompatible,
    AgentMuxNotFound,
    AgentMuxUnauthorized,
    AgentMuxUnreachable,
)
from .models import (
    SUPPORTED_CONTRACT_MAJOR,
    AgentInstance,
    Attachment,
    Capabilities,
    Channel,
    ConsoleSession,
    TenantRegistration,
    HealthReport,
    HealthState,
    InvocationEvent,
    InvocationResult,
    IntegrationSnapshot,
    Orchestration,
    OrchestrationTask,
    TenancySelf,
    Tenant,
    Trigger,
    version_key,
)
from .release import (
    DEFAULT_REPOSITORY,
    AgentMuxReleaseError,
    Release,
    fetch_checksums,
    is_newer,
    latest_release,
)

# 0.1.5 adds tenancy (contract 1.1): client.tenancy and ownership
# fields on agents and channels. Unreleased until the next tag.
__version__ = "0.1.5.dev0"

__all__ = [
    "AgentMuxClient",
    "AsyncAgentMuxClient",
    "DEFAULT_BASE_URL",
    "DEFAULT_REPOSITORY",
    "SUPPORTED_CONTRACT_MAJOR",
    "AgentMuxError",
    "AgentMuxAPIError",
    "AgentMuxBusy",
    "AgentMuxIncompatible",
    "AgentMuxNotFound",
    "AgentMuxReleaseError",
    "AgentMuxUnauthorized",
    "AgentMuxUnreachable",
    "AgentInstance",
    "Attachment",
    "Capabilities",
    "Channel",
    "ConsoleSession",
    "TenantRegistration",
    "HealthReport",
    "HealthState",
    "InvocationEvent",
    "InvocationResult",
    "IntegrationSnapshot",
    "Orchestration",
    "OrchestrationTask",
    "Release",
    "TenancySelf",
    "Tenant",
    "Trigger",
    "fetch_checksums",
    "is_newer",
    "latest_release",
    "version_key",
]
