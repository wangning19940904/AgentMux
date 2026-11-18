package com.agentmux.mobile.hub

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement

// The hub's JSON, as server/accounts.go, server/mailbox.go, server/fleet.go,
// server/phones.go and sessions/sessions.go write it. Only the fields the app
// shows are declared; the rest are ignored.

@Serializable
data class AccountIdentity(
    val id: String,
    val login: String,
    @SerialName("display_name") val displayName: String? = null,
    val role: String,
) {
    val shownName: String get() = displayName?.takeIf { it.isNotBlank() } ?: login
    val isAdmin: Boolean get() = role == "admin"
}

@Serializable
data class AuthSession(
    @SerialName("accounts_enabled") val accountsEnabled: Boolean = false,
    @SerialName("sign_in_required") val signInRequired: Boolean = false,
    val authenticated: Boolean = false,
    val admin: Boolean = false,
    val account: AccountIdentity? = null,
)

@Serializable
data class SignInResponse(val account: AccountIdentity)

// Mailbox: direct messages between people and agents.

@Serializable
data class MailParty(
    val address: String,
    val kind: String = "",
    val name: String = "",
    val description: String? = null,
    val machine: String? = null,
    @SerialName("owner_name") val ownerName: String? = null,
    val online: Boolean? = null,
) {
    val shownName: String get() = name.ifBlank { address }
    val isAgent: Boolean get() = kind == "agent"
}

@Serializable
data class MailMessage(
    val id: String = "",
    val from: String = "",
    @SerialName("from_name") val fromName: String? = null,
    val to: String = "",
    val text: String = "",
    @SerialName("created_at") val createdAt: String = "",
    @SerialName("read_at") val readAt: String? = null,
    val error: String? = null,
    @SerialName("failed_at") val failedAt: String? = null,
)

@Serializable
data class MailConversation(
    val peer: MailParty,
    val self: MailParty,
    val last: MailMessage,
    val unread: Int = 0,
    val messages: Int = 0,
)

@Serializable
data class MailOverview(
    val me: MailParty,
    val admin: Boolean = false,
    val conversations: List<MailConversation>? = null,
    val unread: Int = 0,
)

@Serializable
data class MailDirectory(
    val parties: List<MailParty>? = null,
    val reachable: List<String>? = null,
)

@Serializable
internal data class SendMail(val to: String, val text: String)

// Machines and phones.

@Serializable
data class FleetMachine(
    val id: String,
    val name: String,
    val version: String? = null,
    val online: Boolean = false,
    @SerialName("last_seen_at") val lastSeenAt: String? = null,
    @SerialName("hub_role") val hubRole: String? = null,
)

@Serializable
data class FleetView(
    val source: String = "",
    @SerialName("fetched_at") val fetchedAt: String? = null,
    val stale: Boolean = false,
    val machines: List<FleetMachine>? = null,
)

@Serializable
data class PairedDevice(
    val id: String,
    val name: String = "",
    val hostname: String? = null,
    val os: String? = null,
    val version: String? = null,
    @SerialName("last_seen_at") val lastSeenAt: String? = null,
    val online: Boolean = false,
)

@Serializable
internal data class PairedDevices(val devices: List<PairedDevice>? = null)

@Serializable
data class PhoneDevice(
    val id: String,
    val platform: String = "android",
    val name: String? = null,
    val os: String? = null,
    @SerialName("app_version") val appVersion: String? = null,
    @SerialName("account_name") val accountName: String? = null,
    @SerialName("signed_in_at") val signedInAt: String? = null,
    @SerialName("last_seen_at") val lastSeenAt: String? = null,
    val active: Boolean = false,
    val current: Boolean = false,
)

@Serializable
internal data class Phones(val phones: List<PhoneDevice>? = null)

// Fleet calls: one request run on many machines through the hub.

@Serializable
data class MachineTarget(
    val id: String,
    val name: String = "",
    val kind: String = "",
    val online: Boolean = false,
)

@Serializable
internal data class FleetOperation(
    val key: String,
    val path: String,
    val method: String? = null,
    val body: JsonElement? = null,
)

@Serializable
internal data class FleetBatchRequest(
    @SerialName("target_ids") val targetIds: List<String>,
    val requests: List<FleetOperation>,
)

@Serializable
internal data class FleetResponse(
    val key: String = "",
    val status: Int = 0,
    val ok: Boolean = false,
    val data: JsonElement? = null,
    val error: String? = null,
)

@Serializable
internal data class FleetTargetResult(val target: MachineTarget, val responses: List<FleetResponse> = emptyList())

@Serializable
internal data class FleetBatch(val targets: List<FleetTargetResult> = emptyList())

/** Something that lives on one machine, as a fleet call found it. */
data class OnMachine<T>(val machineId: String, val machineName: String, val item: T)

/** A fleet read: what each machine answered, and which could not. */
data class FleetRead<T>(val items: List<OnMachine<T>>, val failures: List<String>)

// Agents and their sessions.

@Serializable
data class AgentInstance(
    val id: String,
    val name: String = "",
    @SerialName("runtime_id") val runtimeId: String = "",
    @SerialName("work_dir") val workDir: String? = null,
    @SerialName("workspace_mode") val workspaceMode: String? = null,
    @SerialName("session_backend") val sessionBackend: String? = null,
    @SerialName("system_prompt") val systemPrompt: String? = null,
    @SerialName("default_model") val defaultModel: String? = null,
    @SerialName("channel_bindings") val channelBindings: List<JsonElement>? = null,
    val schedules: List<JsonElement>? = null,
    val skills: List<String>? = null,
    val enabled: Boolean = true,
)

@Serializable
data class AgentSession(
    @SerialName("provider_id") val providerId: String = "",
    val surface: String = "",
    @SerialName("session_id") val sessionId: String = "",
    val title: String? = null,
    val summary: String? = null,
    @SerialName("project_dir") val projectDir: String? = null,
    @SerialName("last_active_at") val lastActiveAt: String? = null,
    @SerialName("source_path") val sourcePath: String? = null,
    @SerialName("message_count") val messageCount: Int = 0,
    val available: Boolean = true,
    val origin: String? = null,
    @SerialName("agent_name") val agentName: String? = null,
    @SerialName("channel_id") val channelId: String? = null,
    @SerialName("channel_name") val channelName: String? = null,
    @SerialName("conversation_id") val conversationId: String? = null,
    @SerialName("can_chat") val canChat: Boolean = false,
    @SerialName("run_status") val runStatus: String? = null,
) {
    val shownTitle: String
        get() = title?.takeIf { it.isNotBlank() } ?: summary?.takeIf { it.isNotBlank() } ?: sessionId
    val working: Boolean get() = !runStatus.isNullOrBlank() && runStatus != "idle"
    /** Messages go to a session that runs behind a channel. */
    val canSend: Boolean get() = canChat && !channelId.isNullOrBlank() && !conversationId.isNullOrBlank()
}

@Serializable
data class SessionMessage(
    val role: String = "",
    val kind: String? = null,
    val content: String = "",
    val timestamp: String? = null,
    @SerialName("tool_name") val toolName: String? = null,
    @SerialName("tool_input") val toolInput: String? = null,
    @SerialName("tool_output") val toolOutput: String? = null,
    @SerialName("tool_status") val toolStatus: String? = null,
)

@Serializable
internal data class SessionSend(
    @SerialName("channel_id") val channelId: String,
    @SerialName("conversation_id") val conversationId: String,
    val text: String,
)

@Serializable
internal data class ApiError(val error: String = "")
