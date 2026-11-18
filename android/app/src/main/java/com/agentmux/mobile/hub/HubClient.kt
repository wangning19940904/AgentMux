package com.agentmux.mobile.hub

import java.io.IOException
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.decodeFromJsonElement
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response

/** Keeps the account session a sign-in returns. */
interface SessionStore {
    var sessionToken: String?
}

/** Why a call to the hub failed, in terms the screens can act on. */
sealed class HubException(message: String, cause: Throwable? = null) : Exception(message, cause) {
    /** No session, or it ended: sign in again. */
    class Unauthorized(message: String) : HubException(message)
    class Forbidden(message: String) : HubException(message)
    class RateLimited(message: String) : HubException(message)
    /** Whatever answered is not the hub in the link. */
    class WrongCertificate(cause: Throwable) :
        HubException("The server's certificate does not match this hub's link. Scan the link again.", cause)
    class Unreachable(cause: Throwable) : HubException("Cannot reach the hub: ${cause.message}", cause)
    class Http(val status: Int, message: String) : HubException(message)
}

/**
 * The hub's API on its client port ([hub] client_api). A signed-in phone
 * sends the account session as the Console does: the `agentmux_console`
 * cookie together with the `X-AgentMux-Console` header, without which the
 * hub ignores the cookie.
 */
class HubClient(
    val link: HubLink,
    private val sessions: SessionStore,
    http: OkHttpClient = OkHttpClient(),
    /** Names the app and phone, so the hub's Devices page can list it. */
    private val userAgent: String = "AgentMux-Android",
) {
    private val json = Json { ignoreUnknownKeys = true; explicitNulls = false; encodeDefaults = false }
    private val http: OkHttpClient = PinnedTls.pin(
        http.newBuilder()
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(30, TimeUnit.SECONDS),
        link.fingerprint,
    ).build()

    suspend fun session(): AuthSession = get("/api/v1/auth/session")

    suspend fun signIn(login: String, password: String): AccountIdentity {
        val body = json.encodeToString(SignInRequest.serializer(), SignInRequest(login, password))
        val response: SignInResponse = send(request("/api/v1/auth/sign-in").post(body.toRequestBody(JSON)))
        return response.account
    }

    suspend fun signOut() {
        try {
            send<Unit>(request("/api/v1/auth/sign-out").post(ByteArray(0).toRequestBody(null)), decode = false)
        } finally {
            sessions.sessionToken = null
        }
    }

    // Mailbox.

    suspend fun mailbox(): MailOverview = get("/api/v1/mailbox")

    suspend fun messages(peer: String): List<MailMessage> =
        get<List<MailMessage>?>("/api/v1/mailbox/messages", mapOf("with" to peer)).orEmpty()

    suspend fun sendMessage(to: String, text: String): MailMessage =
        post("/api/v1/mailbox/messages", json.encodeToString(SendMail.serializer(), SendMail(to, text)))

    suspend fun directory(): MailDirectory = get("/api/v1/mailbox/directory")

    // Machines and phones.

    /** The hub's view of its machines; administrators only. */
    suspend fun fleet(): FleetView = get("/api/v1/fleet")

    /** The machines paired to the caller's account. */
    suspend fun pairedDevices(): List<PairedDevice> = get<PairedDevices>("/api/v1/devices").devices.orEmpty()

    suspend fun phones(): List<PhoneDevice> = get<Phones>("/api/v1/devices/phones").phones.orEmpty()

    suspend fun signOutPhone(id: String) {
        send<Unit>(request("/api/v1/devices/phones", mapOf("id" to id)).delete(), decode = false)
    }

    // Agents and sessions, read from every machine through the hub.

    suspend fun agents(): FleetRead<AgentInstance> = fleetRead("/api/v1/agent-instances")

    suspend fun sessions(): FleetRead<AgentSession> = fleetRead("/api/v1/sessions?provider=&surface=")

    suspend fun sessionMessages(machineId: String, session: AgentSession): List<SessionMessage> {
        val query = listOf(
            "provider" to session.providerId, "surface" to session.surface, "session_id" to session.sessionId,
            "source_path" to session.sourcePath.orEmpty(), "project_dir" to session.projectDir.orEmpty(),
            "conversation_id" to session.conversationId.orEmpty(),
        ).joinToString("&") { (key, value) -> key + "=" + java.net.URLEncoder.encode(value, "UTF-8") }
        val response = fleet("query", listOf(machineId), FleetOperation("data", "/api/v1/sessions/messages?$query"))
            .targets.firstOrNull()?.responses?.firstOrNull { it.key == "data" }
            ?: throw HubException.Http(502, "The machine did not answer.")
        if (!response.ok) throw HubException.Http(response.status, response.error ?: "HTTP ${response.status}")
        val data = response.data ?: return emptyList()
        return runCatching { json.decodeFromJsonElement<List<SessionMessage>?>(data) }.getOrNull().orEmpty()
    }

    suspend fun sendSessionMessage(machineId: String, session: AgentSession, text: String) {
        val body = json.encodeToJsonElement(
            SessionSend.serializer(),
            SessionSend(session.channelId.orEmpty(), session.conversationId.orEmpty(), text),
        )
        val response = fleet("execute", listOf(machineId), FleetOperation("message", "/api/v1/sessions/messages", "POST", body))
            .targets.firstOrNull()?.responses?.firstOrNull { it.key == "message" }
            ?: throw HubException.Http(502, "The machine did not answer.")
        if (!response.ok) throw HubException.Http(response.status, response.error ?: "HTTP ${response.status}")
    }

    private suspend inline fun <reified T> fleetRead(path: String): FleetRead<T> {
        val batch = fleet("query", listOf("all"), FleetOperation("data", path))
        val items = mutableListOf<OnMachine<T>>()
        val failures = mutableListOf<String>()
        for (target in batch.targets) {
            val response = target.responses.firstOrNull { it.key == "data" }
            if (response == null || !response.ok) {
                failures += "${target.target.name}: ${response?.error ?: "unavailable"}"
                continue
            }
            val data = response.data ?: continue
            val list = runCatching { json.decodeFromJsonElement<List<T>?>(data) }.getOrNull().orEmpty()
            list.mapTo(items) { OnMachine(target.target.id, target.target.name, it) }
        }
        return FleetRead(items, failures)
    }

    private suspend fun fleet(kind: String, targets: List<String>, operation: FleetOperation): FleetBatch {
        val body = json.encodeToString(FleetBatchRequest.serializer(), FleetBatchRequest(targets, listOf(operation)))
        return post("/api/v1/remote/fleet/$kind", body)
    }

    private suspend inline fun <reified T> get(path: String, query: Map<String, String> = emptyMap()): T =
        send(request(path, query).get())

    private suspend inline fun <reified T> post(path: String, body: String): T =
        send(request(path).post(body.toRequestBody(JSON)))

    private fun request(path: String, query: Map<String, String> = emptyMap()): Request.Builder {
        val url = (link.url + path).toHttpUrl().newBuilder()
        query.forEach { (key, value) -> url.addQueryParameter(key, value) }
        val builder = Request.Builder()
            .url(url.build())
            .header(CONSOLE_HEADER, "1")
            .header("Accept", "application/json")
            .header("User-Agent", userAgent)
        sessions.sessionToken?.let { builder.header("Cookie", "$SESSION_COOKIE=$it") }
        return builder
    }

    private suspend inline fun <reified T> send(builder: Request.Builder, decode: Boolean = true): T =
        withContext(Dispatchers.IO) {
            val response = try {
                http.newCall(builder.build()).execute()
            } catch (e: SSLException) {
                throw if (e.hasCause<CertificateMismatchException>()) HubException.WrongCertificate(e) else HubException.Unreachable(e)
            } catch (e: IOException) {
                throw HubException.Unreachable(e)
            }
            response.use {
                rememberSession(it)
                val text = it.body?.string().orEmpty()
                if (!it.isSuccessful) throw failure(it.code, text)
                @Suppress("UNCHECKED_CAST")
                if (!decode) Unit as T else json.decodeFromString<T>(text)
            }
        }

    private fun rememberSession(response: Response) {
        for (header in response.headers("Set-Cookie")) {
            val pair = header.substringBefore(';')
            if (pair.substringBefore('=').trim() != SESSION_COOKIE) continue
            val value = pair.substringAfter('=', "").trim()
            val cleared = value.isEmpty() || header.contains("Max-Age=0") || header.contains("Max-Age=-")
            sessions.sessionToken = if (cleared) null else value
        }
    }

    private fun failure(status: Int, body: String): HubException {
        val message = runCatching { json.decodeFromString<ApiError>(body).error }.getOrNull()
            ?.takeIf { it.isNotBlank() } ?: body.trim().ifBlank { "HTTP $status" }
        return when (status) {
            401 -> {
                sessions.sessionToken = null
                HubException.Unauthorized(message)
            }
            403 -> HubException.Forbidden(message)
            429 -> HubException.RateLimited(message)
            else -> HubException.Http(status, message)
        }
    }

    @kotlinx.serialization.Serializable
    private data class SignInRequest(val login: String, val password: String)

    companion object {
        const val SESSION_COOKIE = "agentmux_console"
        const val CONSOLE_HEADER = "X-AgentMux-Console"
        private val JSON = "application/json".toMediaType()
    }
}

private inline fun <reified T : Throwable> Throwable.hasCause(): Boolean {
    var current: Throwable? = this
    while (current != null) {
        if (current is T) return true
        current = current.cause
    }
    return false
}
