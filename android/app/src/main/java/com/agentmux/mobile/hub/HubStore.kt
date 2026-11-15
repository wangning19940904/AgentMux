package com.agentmux.mobile.hub

import android.content.Context

/**
 * The paired hub and its account session, in app-private preferences. The
 * app opts out of backups so the session never leaves the phone.
 *
 * TODO: wrap the session token with a Keystore key before release.
 */
class HubStore(context: Context) : SessionStore {
    private val prefs = context.getSharedPreferences("hub", Context.MODE_PRIVATE)

    var link: HubLink?
        get() {
            val url = prefs.getString(KEY_URL, null) ?: return null
            return HubLink(url, prefs.getString(KEY_FINGERPRINT, null))
        }
        set(value) {
            prefs.edit()
                .putString(KEY_URL, value?.url)
                .putString(KEY_FINGERPRINT, value?.fingerprint)
                .remove(KEY_SESSION)
                .apply()
        }

    override var sessionToken: String?
        get() = prefs.getString(KEY_SESSION, null)
        set(value) {
            prefs.edit().putString(KEY_SESSION, value).apply()
        }

    private companion object {
        const val KEY_URL = "url"
        const val KEY_FINGERPRINT = "fingerprint"
        const val KEY_SESSION = "session"
    }
}
