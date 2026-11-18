package com.agentmux.mobile.hub

import org.junit.Assert.assertEquals
import org.junit.Test

class PhoneIdentityTest {
    @Test
    fun namesTheAppAndPhone() {
        assertEquals("AgentMux-Android/0.2.0 (Xiaomi 14; Android 15)", PhoneIdentity.userAgent("0.2.0", "Xiaomi 14", "15"))
    }

    @Test
    fun encodesWhatAHeaderCannotCarry() {
        // The hub decodes these (server/phones.go).
        assertEquals(
            "AgentMux-Android/0.2.0 (%E7%8E%8B%E5%AE%81%E7%9A%84%E6%89%8B%E6%9C%BA; Android 14)",
            PhoneIdentity.userAgent("0.2.0", "王宁的手机", "14"),
        )
        assertEquals("AgentMux-Android/1 (a%3Bb %28c%29 100%25; Android 9)", PhoneIdentity.userAgent("1", "a;b (c) 100%", "9"))
        assertEquals("AgentMux-Android/1 (Android; Android 9)", PhoneIdentity.userAgent("1", "  ", "9"))
    }
}
