package com.agentmux.mobile.hub

import kotlinx.coroutines.test.runTest
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Before
import org.junit.Test

/** A hub with its own self-signed certificate, as `[hub] tls = "auto"` makes. */
class HubClientTest {
    private lateinit var server: MockWebServer
    private lateinit var certificate: HeldCertificate
    private val sessions = object : SessionStore {
        override var sessionToken: String? = null
    }

    @Before
    fun start() {
        certificate = HeldCertificate.Builder().commonName("agentmux-hub").addSubjectAlternativeName("localhost").build()
        server = MockWebServer()
        server.useHttps(HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(), false)
        server.start()
    }

    @After
    fun stop() = server.shutdown()

    private fun client(fingerprint: String? = PinnedTls.fingerprint(certificate.certificate)) =
        HubClient(HubLink(server.url("/").toString().trimEnd('/'), fingerprint), sessions)

    @Test
    fun signsInKeepsTheSessionAndSendsItBack() = runTest {
        server.enqueue(
            MockResponse()
                .addHeader("Set-Cookie", "agentmux_console=ams_secret; Path=/; Max-Age=604800; HttpOnly; Secure; SameSite=Strict")
                .setBody("""{"account":{"id":"a1","login":"bob","role":"member"}}"""),
        )
        server.enqueue(MockResponse().setBody("""{"me":{"address":"person:bob"},"unread":2,"conversations":[]}"""))

        val hub = client()
        assertEquals("bob", hub.signIn("bob", "pw").login)
        assertEquals("ams_secret", sessions.sessionToken)
        assertEquals(2, hub.mailbox().unread)

        val signIn = server.takeRequest()
        assertEquals("/api/v1/auth/sign-in", signIn.path)
        assertEquals("1", signIn.getHeader(HubClient.CONSOLE_HEADER))
        assertEquals("""{"login":"bob","password":"pw"}""", signIn.body.readUtf8())
        assertEquals("agentmux_console=ams_secret", server.takeRequest().getHeader("Cookie"))
    }

    @Test
    fun anEndedSessionIsForgotten() = runTest {
        sessions.sessionToken = "ams_old"
        server.enqueue(MockResponse().setResponseCode(401).setBody("unauthorized"))
        try {
            client().mailbox()
            fail("expected Unauthorized")
        } catch (e: HubException.Unauthorized) {
            assertNull(sessions.sessionToken)
        }
    }

    @Test
    fun hubErrorsCarryTheirMessage() = runTest {
        server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"reserved for administrators"}"""))
        try {
            client().mailbox()
            fail("expected Forbidden")
        } catch (e: HubException.Forbidden) {
            assertEquals("reserved for administrators", e.message)
        }
    }

    @Test
    fun anotherCertificateIsRefused() = runTest {
        try {
            client(fingerprint = "00".repeat(32)).session()
            fail("a server with another certificate was trusted")
        } catch (e: HubException.WrongCertificate) {
            assertEquals(0, server.requestCount)
        }
    }

    @Test
    fun withoutAPinTheSystemCasDecide() = runTest {
        try {
            client(fingerprint = null).session()
            fail("a self-signed certificate passed the system's CA checks")
        } catch (e: HubException.Unreachable) {
            assertTrue(e.message!!.isNotEmpty())
        }
    }
}
