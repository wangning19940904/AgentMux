package com.agentmux.mobile.hub

/**
 * The User-Agent the app sends, which the hub's Devices page reads to list
 * this phone: `AgentMux-Android/0.2.0 (Xiaomi 14; Android 15)`.
 *
 * Header values must be ASCII, and a phone's name is often not ("王宁的手机"),
 * so anything outside printable ASCII, and the characters that delimit the
 * parts, are percent-encoded; the hub decodes them.
 */
object PhoneIdentity {
    fun userAgent(appVersion: String, deviceName: String, androidRelease: String): String {
        val name = encode(deviceName.trim().ifEmpty { "Android" })
        return "AgentMux-Android/${encode(appVersion)} ($name; Android ${encode(androidRelease)})"
    }

    internal fun encode(text: String): String = buildString {
        for (byte in text.toByteArray(Charsets.UTF_8)) {
            val c = byte.toInt() and 0xff
            if (c in 0x20..0x7e && c.toChar() !in "%;()") append(c.toChar()) else append("%%%02X".format(c))
        }
    }
}
