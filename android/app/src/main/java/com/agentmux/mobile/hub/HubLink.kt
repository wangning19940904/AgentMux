package com.agentmux.mobile.hub

import java.net.URI
import java.net.URLDecoder

/**
 * Where a hub is and which certificate to expect there, as the hub's
 * `GET /api/v1/hub/client-link` describes it:
 * `agentmux://hub?url=https://203.0.113.7:443&fp=<sha-256 hex>`.
 *
 * [fingerprint] is null when the hub has a certificate a public CA signed;
 * the system's trust store checks it then.
 */
data class HubLink(val url: String, val fingerprint: String?) {

    companion object {
        const val SCHEME = "agentmux"
        private const val HOST = "hub"
        private val HEX64 = Regex("^[0-9a-f]{64}$")

        /** Reads a scanned or pasted link, or throws [IllegalArgumentException]. */
        fun parse(text: String): HubLink {
            val uri = try {
                URI(text.trim())
            } catch (e: Exception) {
                throw IllegalArgumentException("This is not an AgentMux hub link.")
            }
            require(uri.scheme.equals(SCHEME, ignoreCase = true) && uri.host.equals(HOST, ignoreCase = true)) {
                "This is not an AgentMux hub link."
            }
            val query = parseQuery(uri.rawQuery.orEmpty())
            val url = normalizeUrl(query["url"] ?: throw IllegalArgumentException("The link has no hub address."))
            val fingerprint = query["fp"]?.let(::normalizeFingerprint)?.takeIf { it.isNotEmpty() }
            if (fingerprint != null) {
                require(HEX64.matches(fingerprint)) { "The link's certificate fingerprint is malformed." }
            }
            return HubLink(url, fingerprint)
        }

        /** Accepts the hex form the hub prints and the colon-separated form. */
        fun normalizeFingerprint(raw: String): String = raw.trim().lowercase().replace(":", "")

        private fun normalizeUrl(raw: String): String {
            val uri = try {
                URI(raw.trim())
            } catch (e: Exception) {
                throw IllegalArgumentException("The hub address is malformed.")
            }
            // The phone sends a password and a session to this address.
            require(uri.scheme.equals("https", ignoreCase = true)) { "The hub address must use https://." }
            require(!uri.host.isNullOrEmpty()) { "The hub address has no host." }
            return raw.trim().trimEnd('/')
        }

        private fun parseQuery(raw: String): Map<String, String> =
            raw.split('&').filter { it.isNotEmpty() }.associate { pair ->
                val key = pair.substringBefore('=')
                val value = pair.substringAfter('=', "")
                URLDecoder.decode(key, "UTF-8") to URLDecoder.decode(value, "UTF-8")
            }
    }
}
