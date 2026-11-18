package com.agentmux.mobile.hub

import kotlinx.coroutines.test.runTest
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

/** The calls behind the Messages, Agents and Devices tabs. */
class HubClientFleetTest {
    private lateinit var server: MockWebServer
    private val sessions = object : SessionStore {
        override var sessionToken: String? = "amxs_token"
    }

    @Before
    fun start() {
        server = MockWebServer()
        server.start()
    }

    @After
    fun stop() = server.shutdown()

    // MockWebServer speaks plain HTTP here; HubLink.parse is what refuses
    // http:// links, so the client itself can be pointed at it.
    private fun client() = HubClient(
        HubLink(server.url("/").toString().trimEnd('/'), null),
        sessions,
        userAgent = "AgentMux-Android/0.2.0 (Pixel 8; Android 15)",
    )

    @Test
    fun readsAndSendsAThread() = runTest {
        server.enqueue(MockResponse().setBody("""[{"id":"m1","from":"agent:a1","to":"person:bob","text":"hi","created_at":"2026-11-18T06:00:00Z"}]"""))
        server.enqueue(MockResponse().setBody("""{"id":"m2","from":"person:bob","to":"agent:a1","text":"hello","created_at":"2026-11-18T06:01:00Z"}"""))
        server.enqueue(MockResponse().setBody("null"))

        val hub = client()
        assertEquals("hi", hub.messages("agent:a1@dev 1").single().text)
        assertEquals("m2", hub.sendMessage("agent:a1", "hello").id)
        assertTrue(hub.messages("agent:none").isEmpty())

        val read = server.takeRequest()
        assertEquals("/api/v1/mailbox/messages?with=agent%3Aa1%40dev%201", read.path)
        assertEquals("AgentMux-Android/0.2.0 (Pixel 8; Android 15)", read.getHeader("User-Agent"))
        assertEquals("agentmux_console=amxs_token", read.getHeader("Cookie"))
        val send = server.takeRequest()
        assertEquals("POST", send.method)
        assertEquals("""{"to":"agent:a1","text":"hello"}""", send.body.readUtf8())
    }

    @Test
    fun readsAgentsFromEveryMachine() = runTest {
        server.enqueue(
            MockResponse().setBody(
                """{"targets":[
                  {"target":{"id":"local","name":"laptop","online":true},"responses":[{"key":"data","status":200,"ok":true,
                    "data":[{"id":"a1","name":"Work Agent","runtime_id":"claude","enabled":true}]}]},
                  {"target":{"id":"dev_2","name":"mini","online":false},"responses":[{"key":"data","status":502,"ok":false,"error":"device is offline"}]},
                  {"target":{"id":"dev_3","name":"ecs","online":true},"responses":[{"key":"data","status":200,"ok":true,"data":null}]}
                ]}""",
            ),
        )
        val read = client().agents()
        assertEquals(listOf("laptop" to "Work Agent"), read.items.map { it.machineName to it.item.name })
        assertEquals(listOf("mini: device is offline"), read.failures)

        val request = server.takeRequest()
        assertEquals("/api/v1/remote/fleet/query", request.path)
        assertEquals(
            """{"target_ids":["all"],"requests":[{"key":"data","path":"/api/v1/agent-instances"}]}""",
            request.body.readUtf8(),
        )
    }

    @Test
    fun sendsToASessionOnItsMachine() = runTest {
        server.enqueue(MockResponse().setBody("""{"targets":[{"target":{"id":"dev_2","name":"mini"},"responses":[{"key":"message","status":200,"ok":true,"data":{"ok":true}}]}]}"""))
        val session = AgentSession(providerId = "claude", sessionId = "s1", channelId = "ch", conversationId = "c1", canChat = true)
        client().sendSessionMessage("dev_2", session, "go on")

        val request = server.takeRequest()
        assertEquals("/api/v1/remote/fleet/execute", request.path)
        assertEquals(
            """{"target_ids":["dev_2"],"requests":[{"key":"message","path":"/api/v1/sessions/messages","method":"POST",""" +
                """"body":{"channel_id":"ch","conversation_id":"c1","text":"go on"}}]}""",
            request.body.readUtf8(),
        )
    }

    @Test
    fun listsAndSignsOutPhones() = runTest {
        server.enqueue(MockResponse().setBody("""{"phones":[{"id":"sess_1","platform":"android","name":"Pixel 8","active":true,"current":true}]}"""))
        server.enqueue(MockResponse().setBody("""{"ok":true}"""))
        val hub = client()
        val phone = hub.phones().single()
        assertTrue(phone.current)
        hub.signOutPhone(phone.id)

        assertEquals("/api/v1/devices/phones", server.takeRequest().path)
        val delete = server.takeRequest()
        assertEquals("DELETE", delete.method)
        assertEquals("/api/v1/devices/phones?id=sess_1", delete.path)
    }
}
