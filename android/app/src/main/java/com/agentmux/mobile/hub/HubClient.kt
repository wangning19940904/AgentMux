package com.agentmux.mobile.hub

import java.io.IOException
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.Json
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
) {
    private val json = Json { ignoreUnknownKeys = true; explicitNulls = false }
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

    suspend fun mailbox(): MailOverview = get("/api/v1/mailbox")

    private suspend inline fun <reified T> get(path: String): T = send(request(path).get())

    private fun request(path: String): Request.Builder {
        val builder = Request.Builder()
            .url(link.url + path)
            .header(CONSOLE_HEADER, "1")
            .header("Accept", "application/json")
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
