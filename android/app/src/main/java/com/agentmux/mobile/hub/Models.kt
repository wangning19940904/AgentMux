package com.agentmux.mobile.hub

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

// The hub's JSON, as server/accounts.go and server/mailbox.go write it.
// Only the fields the app shows are declared; the rest are ignored.

@Serializable
data class AccountIdentity(
    val id: String,
    val login: String,
    @SerialName("display_name") val displayName: String? = null,
    val role: String,
) {
    val shownName: String get() = displayName?.takeIf { it.isNotBlank() } ?: login
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

@Serializable
data class MailParty(
    val address: String,
    val kind: String = "",
    val name: String = "",
    val machine: String? = null,
) {
    val shownName: String get() = name.ifBlank { address }
}

@Serializable
data class MailMessage(
    val id: String = "",
    val from: String = "",
    @SerialName("from_name") val fromName: String? = null,
    val text: String = "",
    @SerialName("created_at") val createdAt: String = "",
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
    val conversations: List<MailConversation> = emptyList(),
    val unread: Int = 0,
)

@Serializable
internal data class ApiError(val error: String = "")
