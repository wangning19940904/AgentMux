package com.agentmux.mobile.ui.theme

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Shapes
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * The app's colors, after Persona's design system: a quiet page, ink in three
 * strengths, hairlines instead of boxes, one accent, and a color per state.
 */
@Immutable
data class Palette(
    val background: Color,
    val surface: Color,
    val sunken: Color,
    val line: Color,
    val ink: Color,
    val ink2: Color,
    val ink3: Color,
    val accent: Color,
    val accentSoft: Color,
    val onAccent: Color,
    val userBubble: Color,
    val working: Color,
    val needsYou: Color,
    val positive: Color,
    val danger: Color,
    val idle: Color,
)

private val LightPalette = Palette(
    background = Color(0xFFF6F6F8),
    surface = Color(0xFFFFFFFF),
    sunken = Color(0xFFEDEDF1),
    line = Color(0x1F3C3C43),
    ink = Color(0xFF16161A),
    ink2 = Color(0xFF5E5E66),
    ink3 = Color(0xFF9A9AA2),
    accent = Color(0xFF4F5BD5),
    accentSoft = Color(0xFFE7E9FB),
    onAccent = Color(0xFFFFFFFF),
    userBubble = Color(0xFFDDE6FF),
    working = Color(0xFF2F7CF6),
    needsYou = Color(0xFFF08C1A),
    positive = Color(0xFF2EA44F),
    danger = Color(0xFFD93A3A),
    idle = Color(0xFFA3A3AB),
)

private val DarkPalette = Palette(
    background = Color(0xFF0B0B0D),
    surface = Color(0xFF1A1A1E),
    sunken = Color(0xFF26262B),
    line = Color(0x2EFFFFFF),
    ink = Color(0xFFF2F2F5),
    ink2 = Color(0xFFADADB5),
    ink3 = Color(0xFF75757D),
    accent = Color(0xFF8C95FF),
    accentSoft = Color(0xFF262A4D),
    onAccent = Color(0xFF0B0B0D),
    userBubble = Color(0xFF2B3566),
    working = Color(0xFF5EA0FF),
    needsYou = Color(0xFFFFA940),
    positive = Color(0xFF4CC46E),
    danger = Color(0xFFFF6464),
    idle = Color(0xFF6E6E76),
)

@Immutable
data class AppType(
    val largeTitle: TextStyle,
    val title: TextStyle,
    val headline: TextStyle,
    val body: TextStyle,
    val bodyStrong: TextStyle,
    val callout: TextStyle,
    val caption: TextStyle,
    val micro: TextStyle,
    val code: TextStyle,
)

private val Type = AppType(
    largeTitle = TextStyle(fontSize = 30.sp, lineHeight = 36.sp, fontWeight = FontWeight.Bold, letterSpacing = (-0.3).sp),
    title = TextStyle(fontSize = 20.sp, lineHeight = 26.sp, fontWeight = FontWeight.SemiBold),
    headline = TextStyle(fontSize = 16.sp, lineHeight = 22.sp, fontWeight = FontWeight.SemiBold),
    body = TextStyle(fontSize = 16.sp, lineHeight = 23.sp),
    bodyStrong = TextStyle(fontSize = 16.sp, lineHeight = 23.sp, fontWeight = FontWeight.Medium),
    callout = TextStyle(fontSize = 15.sp, lineHeight = 21.sp),
    caption = TextStyle(fontSize = 13.sp, lineHeight = 18.sp),
    micro = TextStyle(fontSize = 11.sp, lineHeight = 14.sp, fontWeight = FontWeight.Medium, fontFeatureSettings = "tnum"),
    code = TextStyle(fontSize = 13.sp, lineHeight = 19.sp, fontFamily = FontFamily.Monospace),
)

private val LocalPalette = staticCompositionLocalOf { LightPalette }
private val LocalType = staticCompositionLocalOf { Type }

object AppTheme {
    val palette: Palette @Composable get() = LocalPalette.current
    val type: AppType @Composable get() = LocalType.current
}

/** Spacing steps used everywhere, so pages line up. */
object Space {
    val xs = 4.dp
    val s = 8.dp
    val m = 12.dp
    val l = 16.dp
    val xl = 24.dp
    val xxl = 32.dp
}

@Composable
fun AgentMuxTheme(dark: Boolean = isSystemInDarkTheme(), content: @Composable () -> Unit) {
    val palette = if (dark) DarkPalette else LightPalette
    val colors = if (dark) {
        darkColorScheme(
            primary = palette.accent, onPrimary = palette.onAccent,
            primaryContainer = palette.accentSoft, onPrimaryContainer = palette.ink,
            secondaryContainer = palette.accentSoft, onSecondaryContainer = palette.ink,
            background = palette.background, onBackground = palette.ink,
            surface = palette.background, onSurface = palette.ink,
            surfaceVariant = palette.sunken, onSurfaceVariant = palette.ink2,
            surfaceContainer = palette.surface, surfaceContainerHigh = palette.surface,
            surfaceContainerLow = palette.surface,
            outline = palette.ink3, outlineVariant = palette.line,
            error = palette.danger,
        )
    } else {
        lightColorScheme(
            primary = palette.accent, onPrimary = palette.onAccent,
            primaryContainer = palette.accentSoft, onPrimaryContainer = palette.ink,
            secondaryContainer = palette.accentSoft, onSecondaryContainer = palette.ink,
            background = palette.background, onBackground = palette.ink,
            surface = palette.background, onSurface = palette.ink,
            surfaceVariant = palette.sunken, onSurfaceVariant = palette.ink2,
            surfaceContainer = palette.surface, surfaceContainerHigh = palette.surface,
            surfaceContainerLow = palette.surface,
            outline = palette.ink3, outlineVariant = palette.line,
            error = palette.danger,
        )
    }
    val typography = Typography(
        headlineLarge = Type.largeTitle,
        titleLarge = Type.title,
        titleMedium = Type.headline,
        bodyLarge = Type.body,
        bodyMedium = Type.callout,
        bodySmall = Type.caption,
        labelSmall = Type.micro,
    )
    val shapes = Shapes(
        small = RoundedCornerShape(10.dp),
        medium = RoundedCornerShape(14.dp),
        large = RoundedCornerShape(20.dp),
    )
    CompositionLocalProvider(LocalPalette provides palette, LocalType provides Type) {
        MaterialTheme(colorScheme = colors, typography = typography, shapes = shapes, content = content)
    }
}
