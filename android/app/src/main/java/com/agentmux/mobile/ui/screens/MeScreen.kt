package com.agentmux.mobile.ui.screens

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ExitToApp
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.agentmux.mobile.BuildConfig
import com.agentmux.mobile.R
import com.agentmux.mobile.ui.AppViewModel
import com.agentmux.mobile.ui.Phase
import com.agentmux.mobile.ui.UiState
import com.agentmux.mobile.ui.components.Avatar
import com.agentmux.mobile.ui.components.Group
import com.agentmux.mobile.ui.components.Hairline
import com.agentmux.mobile.ui.components.InfoRow
import com.agentmux.mobile.ui.components.LargeTitle
import com.agentmux.mobile.ui.components.SectionHeader
import com.agentmux.mobile.ui.components.Tag
import com.agentmux.mobile.ui.theme.AppIcons
import com.agentmux.mobile.ui.theme.AppTheme
import com.agentmux.mobile.ui.theme.Space

@Composable
fun MeScreen(state: UiState, phase: Phase.Ready, vm: AppViewModel) {
    val palette = AppTheme.palette
    val account = phase.account
    val clipboard = LocalClipboardManager.current
    var confirmForget by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(bottom = Space.xl)) {
        LargeTitle(stringResource(R.string.tab_me))
        Group {
            Row(Modifier.fillMaxWidth().padding(Space.l), verticalAlignment = Alignment.CenterVertically) {
                Avatar(account.shownName, seed = account.id, size = 56.dp)
                Spacer(Modifier.width(Space.m))
                Column(Modifier.weight(1f)) {
                    Text(account.shownName, style = AppTheme.type.title, color = palette.ink)
                    Text("@" + account.login, style = AppTheme.type.caption, color = palette.ink2)
                }
                Tag(
                    stringResource(if (account.isAdmin) R.string.role_admin else R.string.role_member),
                    if (account.isAdmin) palette.accent else palette.ink2,
                    filled = account.isAdmin,
                )
            }
        }

        SectionHeader(stringResource(R.string.section_hub))
        Group {
            InfoRow(
                title = phase.link.url,
                subtitle = stringResource(R.string.hub_address),
                icon = AppIcons.Computer,
                onClick = { clipboard.setText(AnnotatedString(phase.link.url)) },
            )
            Hairline(start = 58.dp)
            InfoRow(
                title = phase.link.fingerprint?.let { it.chunked(4).take(6).joinToString(" ") + " …" }
                    ?: stringResource(R.string.public_certificate),
                subtitle = stringResource(if (phase.link.fingerprint != null) R.string.certificate_pinned else R.string.certificate_ca),
                icon = if (phase.link.fingerprint != null) AppIcons.Shield else Icons.Filled.Lock,
                iconTint = palette.positive,
                onClick = phase.link.fingerprint?.let { fp -> { clipboard.setText(AnnotatedString(fp)) } },
            )
        }

        SectionHeader(stringResource(R.string.section_this_phone))
        Group {
            InfoRow(
                title = vm.deviceName.ifBlank { stringResource(R.string.android_phone) },
                subtitle = stringResource(R.string.this_phone_hint),
                icon = AppIcons.Phone,
            )
            Hairline(start = 58.dp)
            InfoRow(
                title = stringResource(R.string.app_version),
                icon = Icons.Filled.Info,
                iconTint = palette.ink2,
            ) { Text(BuildConfig.VERSION_NAME, style = AppTheme.type.callout, color = palette.ink2) }
        }

        SectionHeader(stringResource(R.string.section_account))
        Group {
            InfoRow(
                title = stringResource(R.string.sign_out),
                icon = Icons.AutoMirrored.Filled.ExitToApp,
                iconTint = palette.danger,
                titleColor = palette.danger,
                onClick = vm::signOut,
            )
            Hairline(start = 58.dp)
            InfoRow(
                title = stringResource(R.string.use_another_hub),
                subtitle = stringResource(R.string.use_another_hub_hint),
                icon = Icons.Filled.Refresh,
                iconTint = palette.ink2,
                onClick = { confirmForget = true },
            )
        }
        Text(
            stringResource(R.string.me_footer),
            style = AppTheme.type.micro,
            color = palette.ink3,
            maxLines = 3,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(horizontal = Space.xl, vertical = Space.l),
        )
    }

    if (confirmForget) {
        AlertDialog(
            onDismissRequest = { confirmForget = false },
            title = { Text(stringResource(R.string.use_another_hub)) },
            text = { Text(stringResource(R.string.forget_hub_body)) },
            confirmButton = {
                TextButton(onClick = {
                    confirmForget = false
                    vm.forgetHub()
                }) { Text(stringResource(R.string.forget_hub_confirm), color = palette.danger) }
            },
            dismissButton = { TextButton(onClick = { confirmForget = false }) { Text(stringResource(R.string.cancel)) } },
        )
    }
}
