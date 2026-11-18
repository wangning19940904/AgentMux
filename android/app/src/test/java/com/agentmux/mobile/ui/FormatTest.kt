package com.agentmux.mobile.ui

import com.agentmux.mobile.ui.components.Ago
import com.agentmux.mobile.ui.components.ago
import com.agentmux.mobile.ui.components.baseName
import com.agentmux.mobile.ui.components.monogram
import com.agentmux.mobile.ui.components.parseInstant
import com.agentmux.mobile.ui.components.shortPath
import com.agentmux.mobile.ui.theme.AppIcons
import java.time.Instant
import java.time.ZoneOffset
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class FormatTest {
    private val zone = ZoneOffset.ofHours(8)

    @Test
    fun readsTheHubsTimes() {
        assertEquals(Instant.parse("2026-11-18T06:00:00Z"), parseInstant("2026-11-18T14:00:00.123456789+08:00")!!.minusNanos(123456789))
        assertEquals(Instant.parse("2026-11-18T06:00:00Z"), parseInstant("2026-11-18T06:00:00Z"))
        assertNull(parseInstant("yesterday"))
        assertNull(parseInstant(null))
    }

    @Test
    fun saysHowLongAgo() {
        val now = Instant.parse("2026-11-18T06:00:00Z") // 14:00 in UTC+8
        assertEquals(Ago.JustNow, ago(now.minusSeconds(20), now, zone))
        assertEquals(Ago.Minutes(5), ago(now.minusSeconds(300), now, zone))
        assertEquals(Ago.Today("09:00"), ago(Instant.parse("2026-11-18T01:00:00Z"), now, zone))
        assertEquals(Ago.Yesterday("23:30"), ago(Instant.parse("2026-11-17T15:30:00Z"), now, zone))
        assertTrue(ago(Instant.parse("2026-10-01T00:00:00Z"), now, zone) is Ago.Date)
    }

    @Test
    fun namesThingsShortly() {
        assertEquals("王", monogram("王宁"))
        assertEquals("WA", monogram("Work Agent"))
        assertEquals("C", monogram("claude"))
        assertEquals("?", monogram(" "))
        assertEquals("app", baseName("/Users/me/projects/app/"))
        assertEquals("…/projects/app", shortPath("/Users/me/projects/app"))
        assertEquals("/tmp", shortPath("/tmp"))
    }

    @Test
    fun iconPathsParse() {
        AppIcons.all.forEach { assertTrue(it.name, it.root.iterator().hasNext()) }
    }
}
