package com.agentmux.mobile.ui.components

import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import com.agentmux.mobile.R
import java.time.Duration
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle

/** Reads the hub's RFC 3339 times (with Z or an offset, any fraction). */
fun parseInstant(text: String?): Instant? {
    if (text.isNullOrBlank()) return null
    return runCatching { OffsetDateTime.parse(text).toInstant() }.getOrNull()
}

/** How long ago something happened, in the words a list row uses. */
sealed interface Ago {
    data object JustNow : Ago
    data class Minutes(val n: Long) : Ago
    data class Today(val time: String) : Ago
    data class Yesterday(val time: String) : Ago
    data class Date(val date: String) : Ago
}

fun ago(at: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): Ago {
    val elapsed = Duration.between(at, now)
    if (elapsed.toMinutes() < 1) return Ago.JustNow
    if (elapsed.toMinutes() < 60) return Ago.Minutes(elapsed.toMinutes())
    val day = at.atZone(zone).toLocalDate()
    val today = now.atZone(zone).toLocalDate()
    val time = DateTimeFormatter.ofPattern("HH:mm").format(at.atZone(zone))
    return when (day) {
        today -> Ago.Today(time)
        today.minusDays(1) -> Ago.Yesterday(time)
        else -> Ago.Date(DateTimeFormatter.ofLocalizedDate(FormatStyle.MEDIUM).format(at.atZone(zone)))
    }
}

@Composable
fun relativeTime(text: String?): String {
    val at = parseInstant(text) ?: return ""
    return when (val value = ago(at)) {
        Ago.JustNow -> stringResource(R.string.time_just_now)
        is Ago.Minutes -> stringResource(R.string.time_minutes_ago, value.n)
        is Ago.Today -> value.time
        is Ago.Yesterday -> stringResource(R.string.time_yesterday, value.time)
        is Ago.Date -> value.date
    }
}

/**
 * A message's clock time, with the date when it was not today, unless the
 * page already heads each day with its date ([withDate] false).
 */
fun clockTime(text: String?, withDate: Boolean = true, zone: ZoneId = ZoneId.systemDefault()): String {
    val at = parseInstant(text) ?: return ""
    val local = at.atZone(zone)
    val sameDay = local.toLocalDate() == Instant.now().atZone(zone).toLocalDate()
    return if (sameDay || !withDate) {
        DateTimeFormatter.ofPattern("HH:mm").format(local)
    } else {
        DateTimeFormatter.ofLocalizedDateTime(FormatStyle.SHORT).format(local)
    }
}

/** The last part of a path, for a project directory. */
fun baseName(path: String?): String =
    path?.trimEnd('/')?.substringAfterLast('/')?.ifBlank { null } ?: path.orEmpty()

/** A long path cut to its last two parts: …/projects/app. */
fun shortPath(path: String): String {
    val parts = path.trimEnd('/').split('/').filter { it.isNotEmpty() }
    return if (parts.size <= 2) path else "…/" + parts.takeLast(2).joinToString("/")
}
