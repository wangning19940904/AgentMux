package com.agentmux.mobile.hub

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test

class HubLinkTest {
    private val fp = "ab".repeat(32)

    @Test
    fun readsTheLinkTheHubPrints() {
        // As url.Values.Encode writes it in server/hub_clients.go.
        val link = HubLink.parse("agentmux://hub?fp=$fp&url=https%3A%2F%2F203.0.113.7%3A443")
        assertEquals(HubLink("https://203.0.113.7:443", fp), link)
    }

    @Test
    fun acceptsColonFingerprintsAndCaUrls() {
        val colons = fp.chunked(2).joinToString(":").uppercase()
        assertEquals(fp, HubLink.parse("agentmux://hub?url=https://h.example.com/&fp=$colons").fingerprint)
        val ca = HubLink.parse("agentmux://hub?url=https://hub.example.com/")
        assertEquals("https://hub.example.com", ca.url)
        assertNull(ca.fingerprint)
    }

    @Test
    fun refusesWhatIsNotASafeHubLink() {
        for (bad in listOf(
            "https://hub.example.com",
            "agentmux://other?url=https://h",
            "agentmux://hub",
            "agentmux://hub?url=http://10.0.0.2:8766",
            "agentmux://hub?url=https://h&fp=1234",
            "not a link at all",
        )) {
            assertThrows(bad, IllegalArgumentException::class.java) { HubLink.parse(bad) }
        }
    }
}
