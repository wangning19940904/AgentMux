package com.agentmux.mobile.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Search
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.agentmux.mobile.ui.theme.AppTheme
import com.agentmux.mobile.ui.theme.Space

/** Hues for monogram avatars, picked by a stable hash of the seed. */
private val AvatarHues = listOf(
    Color(0xFF5B6CF0), Color(0xFF2E9E8F), Color(0xFFE0703A), Color(0xFFB0509E),
    Color(0xFF3D8BD9), Color(0xFF7A9A2E), Color(0xFFD14B6A), Color(0xFF8A63D2),
)

fun avatarHue(seed: String): Color = AvatarHues[Math.floorMod(seed.hashCode(), AvatarHues.size)]

fun monogram(name: String): String {
    val trimmed = name.trim()
    if (trimmed.isEmpty()) return "?"
    val first = trimmed.codePointAt(0)
    // CJK names read best by their first character; Latin names by initials.
    if (Character.isIdeographic(first)) return String(Character.toChars(first))
    val words = trimmed.split(Regex("[\\s_\\-.]+")).filter { it.isNotEmpty() }
    return words.take(2).joinToString("") { it.substring(0, 1).uppercase() }
}

/**
 * A person, agent or machine: a monogram (or [icon]) on its hue, with a
 * presence dot at the corner when [online] is known.
 */
@Composable
fun Avatar(
    name: String,
    seed: String = name,
    size: Dp = 44.dp,
    icon: ImageVector? = null,
    online: Boolean? = null,
) {
    val palette = AppTheme.palette
    val hue = avatarHue(seed)
    Box(Modifier.size(size)) {
        Box(
            Modifier.size(size).clip(CircleShape).background(hue.copy(alpha = 0.16f)),
            contentAlignment = Alignment.Center,
        ) {
            if (icon != null) {
                Icon(icon, contentDescription = null, tint = hue, modifier = Modifier.size(size * 0.5f))
            } else {
                Text(
                    monogram(name),
                    color = hue,
                    fontWeight = FontWeight.SemiBold,
                    fontSize = (size.value * 0.4f).sp,
                    maxLines = 1,
                )
            }
        }
        if (online != null) {
            Box(
                Modifier.align(Alignment.BottomEnd).size(size * 0.3f)
                    .clip(CircleShape).background(palette.background).padding(2.dp)
                    .clip(CircleShape).background(if (online) palette.positive else palette.idle),
            )
        }
    }
}

@Composable
fun StatusDot(color: Color, size: Dp = 8.dp) {
    Box(Modifier.size(size).clip(CircleShape).background(color))
}

/** A small static label, such as Admin, Hub or This phone. */
@Composable
fun Tag(text: String, color: Color = AppTheme.palette.ink2, filled: Boolean = false) {
    Text(
        text,
        style = AppTheme.type.micro,
        color = color,
        maxLines = 1,
        modifier = Modifier
            .clip(RoundedCornerShape(6.dp))
            .background(if (filled) color.copy(alpha = 0.14f) else AppTheme.palette.sunken)
            .padding(horizontal = 6.dp, vertical = 2.dp),
    )
}

/** Count of things waiting for the person. */
@Composable
fun CountBadge(count: Int, color: Color = AppTheme.palette.danger) {
    Box(
        Modifier.heightIn(min = 20.dp).widthIn(min = 20.dp).clip(CircleShape).background(color)
            .padding(horizontal = 6.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(if (count > 99) "99+" else count.toString(), color = Color.White, style = AppTheme.type.micro)
    }
}

/** A root page's large title, with its one-line summary and actions. */
@Composable
fun LargeTitle(title: String, summary: String? = null, actions: @Composable RowScope.() -> Unit = {}) {
    Row(
        Modifier.fillMaxWidth().padding(start = Space.l, end = Space.s, top = Space.l, bottom = Space.s),
        verticalAlignment = Alignment.Bottom,
    ) {
        Column(Modifier.weight(1f)) {
            Text(title, style = AppTheme.type.largeTitle, color = AppTheme.palette.ink)
            if (!summary.isNullOrBlank()) {
                Text(summary, style = AppTheme.type.caption, color = AppTheme.palette.ink2)
            }
        }
        Row(verticalAlignment = Alignment.CenterVertically, content = actions)
    }
}

@Composable
fun SectionHeader(title: String, count: Int? = null, trailing: @Composable RowScope.() -> Unit = {}) {
    Row(
        Modifier.fillMaxWidth().padding(start = Space.l, end = Space.l, top = Space.xl, bottom = Space.s),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(title.uppercase(), style = AppTheme.type.micro, color = AppTheme.palette.ink2)
        if (count != null) {
            Spacer(Modifier.width(6.dp))
            Text(count.toString(), style = AppTheme.type.micro, color = AppTheme.palette.ink3)
        }
        Spacer(Modifier.weight(1f))
        trailing()
    }
}

/** A rounded group of rows on the page, separated by hairlines. */
@Composable
fun Group(modifier: Modifier = Modifier, content: @Composable ColumnScope.() -> Unit) {
    Column(
        modifier.padding(horizontal = Space.l).fillMaxWidth()
            .clip(RoundedCornerShape(16.dp)).background(AppTheme.palette.surface),
        content = content,
    )
}

@Composable
fun Hairline(start: Dp = 0.dp) {
    Box(Modifier.padding(start = start).fillMaxWidth().height(0.5.dp).background(AppTheme.palette.line))
}

/** A settings-style row: icon, title, subtitle, a value or control at the end. */
@Composable
fun InfoRow(
    title: String,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
    icon: ImageVector? = null,
    iconTint: Color = AppTheme.palette.accent,
    titleColor: Color = AppTheme.palette.ink,
    onClick: (() -> Unit)? = null,
    trailing: @Composable RowScope.() -> Unit = {},
) {
    Row(
        modifier.fillMaxWidth()
            .then(if (onClick != null) Modifier.clickable(role = Role.Button, onClick = onClick) else Modifier)
            .heightIn(min = 52.dp)
            .padding(horizontal = Space.l, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (icon != null) {
            Box(
                Modifier.size(30.dp).clip(RoundedCornerShape(8.dp)).background(iconTint.copy(alpha = 0.14f)),
                contentAlignment = Alignment.Center,
            ) {
                Icon(icon, contentDescription = null, tint = iconTint, modifier = Modifier.size(18.dp))
            }
            Spacer(Modifier.width(Space.m))
        }
        Column(Modifier.weight(1f)) {
            Text(title, style = AppTheme.type.body, color = titleColor, maxLines = 2, overflow = TextOverflow.Ellipsis)
            if (!subtitle.isNullOrBlank()) {
                Text(subtitle, style = AppTheme.type.caption, color = AppTheme.palette.ink2)
            }
        }
        trailing()
    }
}

@Composable
fun EmptyState(icon: ImageVector, title: String, message: String? = null, action: @Composable () -> Unit = {}) {
    Column(
        Modifier.fillMaxWidth().padding(horizontal = Space.xxl, vertical = 56.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(Space.s),
    ) {
        Box(
            Modifier.size(64.dp).clip(CircleShape).background(AppTheme.palette.sunken),
            contentAlignment = Alignment.Center,
        ) {
            Icon(icon, contentDescription = null, tint = AppTheme.palette.ink3, modifier = Modifier.size(30.dp))
        }
        Spacer(Modifier.height(Space.xs))
        Text(title, style = AppTheme.type.headline, color = AppTheme.palette.ink, textAlign = TextAlign.Center)
        if (message != null) {
            Text(message, style = AppTheme.type.callout, color = AppTheme.palette.ink2, textAlign = TextAlign.Center)
        }
        action()
    }
}

/** An in-page notice, such as a failed refresh, with an optional action. */
@Composable
fun Notice(text: String, color: Color = AppTheme.palette.danger, action: String? = null, onAction: () -> Unit = {}) {
    Row(
        Modifier.padding(horizontal = Space.l, vertical = Space.s).fillMaxWidth()
            .clip(RoundedCornerShape(12.dp)).background(color.copy(alpha = 0.1f))
            .padding(horizontal = Space.m, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(text, style = AppTheme.type.caption, color = color, modifier = Modifier.weight(1f))
        if (action != null) {
            Text(
                action,
                style = AppTheme.type.caption.copy(fontWeight = FontWeight.SemiBold),
                color = color,
                modifier = Modifier.clip(RoundedCornerShape(6.dp)).clickable(onClick = onAction).padding(6.dp),
            )
        }
    }
}

/** Mutually exclusive choices, as a pill track. */
@Composable
fun Segmented(options: List<String>, selected: Int, onSelect: (Int) -> Unit, modifier: Modifier = Modifier) {
    val palette = AppTheme.palette
    Row(
        modifier.padding(horizontal = Space.l).fillMaxWidth()
            .clip(RoundedCornerShape(10.dp)).background(palette.sunken).padding(3.dp),
    ) {
        options.forEachIndexed { index, label ->
            val on = index == selected
            Box(
                Modifier.weight(1f).clip(RoundedCornerShape(8.dp))
                    .background(if (on) palette.surface else Color.Transparent)
                    .clickable(role = Role.Tab) { onSelect(index) }
                    .padding(vertical = 7.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    label,
                    style = AppTheme.type.caption.copy(fontWeight = if (on) FontWeight.SemiBold else FontWeight.Normal),
                    color = if (on) palette.ink else palette.ink2,
                )
            }
        }
    }
}

data class ChipItem(val key: String, val label: String, val count: Int? = null, val online: Boolean? = null)

/** Filter chips that scroll sideways, such as All machines + each machine. */
@Composable
fun ChipRow(items: List<ChipItem>, selected: String, onSelect: (String) -> Unit) {
    val palette = AppTheme.palette
    Row(
        Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = Space.l, vertical = Space.s),
        horizontalArrangement = Arrangement.spacedBy(Space.s),
    ) {
        items.forEach { item ->
            val on = item.key == selected
            Row(
                Modifier.height(32.dp).clip(CircleShape)
                    .background(if (on) palette.accentSoft else palette.surface)
                    .border(0.5.dp, if (on) palette.accent.copy(alpha = 0.4f) else palette.line, CircleShape)
                    .clickable { onSelect(item.key) }
                    .padding(horizontal = 12.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                if (item.online != null) StatusDot(if (item.online) palette.positive else palette.idle, 7.dp)
                Text(item.label, style = AppTheme.type.caption, color = if (on) palette.accent else palette.ink, maxLines = 1)
                if (item.count != null) Text(item.count.toString(), style = AppTheme.type.micro, color = palette.ink3)
            }
        }
    }
}

@Composable
fun SearchField(value: String, onValueChange: (String) -> Unit, placeholder: String) {
    val palette = AppTheme.palette
    Row(
        Modifier.padding(horizontal = Space.l, vertical = Space.xs).fillMaxWidth().height(40.dp)
            .clip(RoundedCornerShape(12.dp)).background(palette.sunken).padding(start = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(Icons.Filled.Search, contentDescription = null, tint = palette.ink3, modifier = Modifier.size(18.dp))
        Spacer(Modifier.width(6.dp))
        Box(Modifier.weight(1f)) {
            if (value.isEmpty()) Text(placeholder, style = AppTheme.type.callout, color = palette.ink3)
            BasicTextField(
                value = value,
                onValueChange = onValueChange,
                singleLine = true,
                textStyle = AppTheme.type.callout.copy(color = palette.ink),
                cursorBrush = SolidColor(palette.accent),
                modifier = Modifier.fillMaxWidth(),
            )
        }
        if (value.isNotEmpty()) {
            IconButton(onClick = { onValueChange("") }) {
                Icon(Icons.Filled.Close, contentDescription = null, tint = palette.ink3, modifier = Modifier.size(16.dp))
            }
        }
    }
}

/** A page that fills the screen under its own content. */
@Composable
fun PageBox(content: @Composable () -> Unit) {
    Box(Modifier.fillMaxSize().background(AppTheme.palette.background)) { content() }
}

val ListBottomPadding = PaddingValues(bottom = 24.dp)
