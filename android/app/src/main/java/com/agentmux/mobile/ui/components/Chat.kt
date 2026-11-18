package com.agentmux.mobile.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowRight
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.agentmux.mobile.ui.theme.AppIcons
import com.agentmux.mobile.ui.theme.AppTheme
import com.agentmux.mobile.ui.theme.Space

/**
 * One message. The person's own sit on the right in the pale accent bubble;
 * the other side's on the left on the surface, as in Persona's chat.
 */
@Composable
fun Bubble(
    text: String,
    mine: Boolean,
    time: String,
    sender: String? = null,
    failed: String? = null,
    pending: Boolean = false,
    onRetry: (() -> Unit)? = null,
) {
    val palette = AppTheme.palette
    Column(
        Modifier.fillMaxWidth().padding(horizontal = Space.l, vertical = 3.dp),
        horizontalAlignment = if (mine) Alignment.End else Alignment.Start,
    ) {
        if (sender != null) {
            Text(sender, style = AppTheme.type.micro, color = palette.ink2, modifier = Modifier.padding(start = 12.dp, bottom = 2.dp))
        }
        Box(
            Modifier.widthIn(max = 320.dp)
                .clip(RoundedCornerShape(18.dp))
                .background(if (mine) palette.userBubble else palette.surface)
                .alpha(if (pending) 0.6f else 1f)
                .padding(horizontal = 14.dp, vertical = 10.dp),
        ) {
            SelectionContainer { Text(text, style = AppTheme.type.body, color = palette.ink) }
        }
        Row(Modifier.padding(horizontal = 6.dp, vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
            if (failed != null) {
                Text(
                    failed,
                    style = AppTheme.type.micro,
                    color = palette.danger,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false)
                        .then(if (onRetry != null) Modifier.clickable(onClick = onRetry) else Modifier),
                )
            } else if (time.isNotEmpty()) {
                Text(time, style = AppTheme.type.micro, color = palette.ink3)
            }
        }
    }
}

/** A step an agent took (a tool call), folded to one line until tapped. */
@Composable
fun WorkStep(title: String, detail: String?, failed: Boolean) {
    val palette = AppTheme.palette
    var open by rememberSaveable(title, detail) { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth().padding(horizontal = Space.l, vertical = 2.dp)) {
        Row(
            Modifier.clip(RoundedCornerShape(8.dp))
                .clickable(enabled = !detail.isNullOrBlank()) { open = !open }
                .padding(vertical = 4.dp, horizontal = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(AppIcons.Wrench, contentDescription = null, tint = if (failed) palette.danger else palette.ink3, modifier = Modifier.size(14.dp))
            Spacer(Modifier.width(6.dp))
            Text(title, style = AppTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f, fill = false))
            if (!detail.isNullOrBlank()) {
                Icon(
                    if (open) Icons.Filled.KeyboardArrowDown else Icons.Filled.KeyboardArrowRight,
                    contentDescription = null, tint = palette.ink3, modifier = Modifier.size(16.dp),
                )
            }
        }
        if (open && !detail.isNullOrBlank()) {
            SelectionContainer {
                Text(
                    detail,
                    style = AppTheme.type.code,
                    color = palette.ink2,
                    maxLines = 40,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(start = 20.dp, top = 2.dp, bottom = 6.dp).fillMaxWidth()
                        .clip(RoundedCornerShape(10.dp)).background(palette.sunken).padding(10.dp),
                )
            }
        }
    }
}

/** A centered line between days or for the conversation's start. */
@Composable
fun TimelineMark(text: String) {
    Box(Modifier.fillMaxWidth().padding(vertical = Space.s), contentAlignment = Alignment.Center) {
        Text(text, style = AppTheme.type.micro, color = AppTheme.palette.ink3)
    }
}

/** The text box at the bottom of a chat, with its send button. */
@Composable
fun Composer(
    value: String,
    onValueChange: (String) -> Unit,
    placeholder: String,
    onSend: () -> Unit,
    enabled: Boolean = true,
) {
    val palette = AppTheme.palette
    val canSend = enabled && value.isNotBlank()
    Row(
        Modifier.fillMaxWidth().background(palette.background).navigationBarsPadding()
            .padding(start = Space.m, end = Space.s, top = Space.s, bottom = Space.s),
        verticalAlignment = Alignment.Bottom,
        horizontalArrangement = Arrangement.spacedBy(Space.xs),
    ) {
        Box(
            Modifier.weight(1f).heightIn(min = 44.dp).clip(RoundedCornerShape(22.dp))
                .background(palette.surface).padding(horizontal = 16.dp, vertical = 11.dp),
            contentAlignment = Alignment.CenterStart,
        ) {
            if (value.isEmpty()) Text(placeholder, style = AppTheme.type.body, color = palette.ink3)
            BasicTextField(
                value = value,
                onValueChange = onValueChange,
                enabled = enabled,
                textStyle = AppTheme.type.body.copy(color = palette.ink),
                cursorBrush = SolidColor(palette.accent),
                maxLines = 6,
                modifier = Modifier.fillMaxWidth(),
            )
        }
        IconButton(
            onClick = onSend,
            enabled = canSend,
            modifier = Modifier.size(44.dp),
            colors = IconButtonDefaults.iconButtonColors(
                containerColor = palette.accent,
                contentColor = palette.onAccent,
                disabledContainerColor = palette.sunken,
                disabledContentColor = palette.ink3,
            ),
        ) {
            Icon(Icons.AutoMirrored.Filled.Send, contentDescription = null, modifier = Modifier.size(20.dp))
        }
    }
}

/** The bold one-line title and quiet subtitle of a pushed page's top bar. */
@Composable
fun BarTitle(title: String, subtitle: String? = null) {
    Column {
        Text(title, style = AppTheme.type.headline.copy(fontWeight = FontWeight.SemiBold), maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (!subtitle.isNullOrBlank()) {
            Text(subtitle, style = AppTheme.type.micro, color = AppTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}
