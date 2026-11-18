package com.agentmux.mobile.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.agentmux.mobile.R
import com.agentmux.mobile.hub.PhoneDevice
import com.agentmux.mobile.ui.AppViewModel
import com.agentmux.mobile.ui.MachineRow
import com.agentmux.mobile.ui.PollWhileResumed
import com.agentmux.mobile.ui.UiState
import com.agentmux.mobile.ui.components.EmptyState
import com.agentmux.mobile.ui.components.Hairline
import com.agentmux.mobile.ui.components.LargeTitle
import com.agentmux.mobile.ui.components.ListBottomPadding
import com.agentmux.mobile.ui.components.Notice
import com.agentmux.mobile.ui.components.SectionHeader
import com.agentmux.mobile.ui.components.StatusDot
import com.agentmux.mobile.ui.components.Tag
import com.agentmux.mobile.ui.components.relativeTime
import com.agentmux.mobile.ui.theme.AppIcons
import com.agentmux.mobile.ui.theme.AppTheme
import com.agentmux.mobile.ui.theme.Space

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun DevicesScreen(state: UiState, vm: AppViewModel) {
    PollWhileResumed(20_000) { vm.refreshDevices() }
    val palette = AppTheme.palette
    val machines = state.machines
    val phones = state.phones
    val machineList = machines.value.orEmpty().sortedWith(compareByDescending<MachineRow> { it.hubRole == "hub" }.thenByDescending { it.online }.thenBy { it.name.lowercase() })
    val phoneList = phones.value.orEmpty().sortedWith(compareByDescending<PhoneDevice> { it.current }.thenByDescending { it.active })
    val onlineCount = machineList.count { it.online }
    var confirm by remember { mutableStateOf<PhoneDevice?>(null) }

    PullToRefreshBox(
        isRefreshing = (machines.loading || phones.loading) && machines.value != null,
        onRefresh = vm::refreshDevices,
        modifier = Modifier.fillMaxSize(),
    ) {
        LazyColumn(Modifier.fillMaxSize(), contentPadding = ListBottomPadding) {
            item {
                LargeTitle(
                    stringResource(R.string.tab_devices),
                    summary = stringResource(R.string.devices_summary, onlineCount, machineList.size, phoneList.size),
                )
            }
            machines.error?.let { item { Notice(it, action = stringResource(R.string.retry), onAction = vm::refreshDevices) } }
            if (machines.value == null && machines.loading) item { LinearProgressIndicator(Modifier.fillMaxWidth().padding(Space.l)) }

            item { SectionHeader(stringResource(if (state.admin) R.string.section_machines else R.string.section_my_machines), machineList.size) }
            if (machines.value != null && machineList.isEmpty()) {
                item { EmptyState(AppIcons.Computer, stringResource(R.string.no_machines), stringResource(R.string.no_machines_hint)) }
            }
            itemsIndexed(machineList, key = { _, m -> "m:" + m.id }) { index, machine ->
                CardSlice(index, machineList.size) { MachineCardRow(machine) }
            }

            item { SectionHeader(stringResource(R.string.section_phones), phoneList.size) }
            phones.error?.let { item { Notice(it) } }
            if (phones.value != null && phoneList.isEmpty()) {
                item { EmptyState(AppIcons.Phone, stringResource(R.string.no_phones)) }
            }
            itemsIndexed(phoneList, key = { _, p -> "p:" + p.id }) { index, phone ->
                CardSlice(index, phoneList.size) {
                    PhoneCardRow(phone, showAccount = state.admin, onSignOut = if (phone.current) null else ({ confirm = phone }))
                }
            }
        }
    }

    confirm?.let { phone ->
        AlertDialog(
            onDismissRequest = { confirm = null },
            title = { Text(stringResource(R.string.sign_out_phone_title)) },
            text = { Text(stringResource(R.string.sign_out_phone_body, phoneName(phone))) },
            confirmButton = {
                TextButton(onClick = {
                    vm.signOutPhone(phone)
                    confirm = null
                }) { Text(stringResource(R.string.sign_out), color = palette.danger) }
            },
            dismissButton = { TextButton(onClick = { confirm = null }) { Text(stringResource(R.string.cancel)) } },
        )
    }
}

@Composable
private fun phoneName(phone: PhoneDevice): String =
    phone.name?.takeIf { it.isNotBlank() } ?: stringResource(if (phone.platform == "ios") R.string.iphone else R.string.android_phone)

/** One row of a rounded card, drawn slice by slice so the list stays lazy. */
@Composable
private fun CardSlice(index: Int, count: Int, content: @Composable () -> Unit) {
    val top = if (index == 0) 16.dp else 0.dp
    val bottom = if (index == count - 1) 16.dp else 0.dp
    Column(
        Modifier.padding(horizontal = Space.l).fillMaxWidth()
            .clip(RoundedCornerShape(topStart = top, topEnd = top, bottomStart = bottom, bottomEnd = bottom))
            .background(AppTheme.palette.surface),
    ) {
        if (index > 0) Hairline(start = 64.dp)
        content()
    }
}

@Composable
private fun DeviceIcon(icon: ImageVector, online: Boolean) {
    val palette = AppTheme.palette
    val tint = if (online) palette.accent else palette.ink3
    Box(Modifier.size(36.dp)) {
        Box(
            Modifier.size(36.dp).clip(RoundedCornerShape(10.dp)).background(tint.copy(alpha = 0.12f)),
            contentAlignment = Alignment.Center,
        ) { Icon(icon, contentDescription = null, tint = tint, modifier = Modifier.size(20.dp)) }
        Box(
            Modifier.align(Alignment.BottomEnd).size(12.dp).clip(RoundedCornerShape(6.dp)).background(palette.surface).padding(2.dp),
        ) { StatusDot(if (online) palette.positive else palette.idle, 8.dp) }
    }
}

@Composable
private fun MachineCardRow(machine: MachineRow) {
    val palette = AppTheme.palette
    Row(Modifier.fillMaxWidth().padding(horizontal = Space.m, vertical = Space.m), verticalAlignment = Alignment.CenterVertically) {
        DeviceIcon(AppIcons.Computer, machine.online)
        Spacer(Modifier.width(Space.m))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                Text(machine.name, style = AppTheme.type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false))
                when (machine.hubRole) {
                    "hub" -> Tag(stringResource(R.string.role_hub), palette.accent, filled = true)
                    "standby" -> Tag(stringResource(R.string.role_standby))
                }
            }
            val status = if (machine.online) {
                stringResource(R.string.online)
            } else {
                machine.lastSeenAt?.let { stringResource(R.string.last_seen, relativeTime(it)) } ?: stringResource(R.string.offline)
            }
            Text(
                listOfNotNull(status, machine.version?.takeIf { it.isNotBlank() }?.let { "v" + it.removePrefix("v") }).joinToString(" · "),
                style = AppTheme.type.caption, color = if (machine.online) palette.ink2 else palette.ink3,
            )
        }
    }
}

@Composable
private fun PhoneCardRow(phone: PhoneDevice, showAccount: Boolean, onSignOut: (() -> Unit)?) {
    val palette = AppTheme.palette
    var menu by remember { mutableStateOf(false) }
    Row(Modifier.fillMaxWidth().padding(start = Space.m, top = Space.m, bottom = Space.m), verticalAlignment = Alignment.CenterVertically) {
        DeviceIcon(AppIcons.Phone, phone.active || phone.current)
        Spacer(Modifier.width(Space.m))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                Text(phoneName(phone), style = AppTheme.type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false))
                if (phone.current) Tag(stringResource(R.string.this_phone), palette.accent, filled = true)
            }
            Text(
                listOfNotNull(
                    phone.accountName?.takeIf { showAccount && it.isNotBlank() },
                    phone.os?.takeIf { it.isNotBlank() },
                    phone.appVersion?.takeIf { it.isNotBlank() }?.let { "v$it" },
                ).joinToString(" · ").ifEmpty { stringResource(if (phone.platform == "ios") R.string.iphone else R.string.android_phone) },
                style = AppTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
            Text(
                listOfNotNull(
                    phone.signedInAt?.let { stringResource(R.string.signed_in_at, relativeTime(it)) },
                    if (phone.active || phone.current) stringResource(R.string.active_now) else phone.lastSeenAt?.let { stringResource(R.string.last_seen, relativeTime(it)) },
                ).joinToString(" · "),
                style = AppTheme.type.micro, color = palette.ink3,
            )
        }
        if (onSignOut != null) {
            Box {
                IconButton(onClick = { menu = true }) { Icon(Icons.Filled.MoreVert, contentDescription = null, tint = palette.ink3) }
                DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                    DropdownMenuItem(
                        text = { Text(stringResource(R.string.sign_out), color = palette.danger) },
                        onClick = {
                            menu = false
                            onSignOut()
                        },
                    )
                }
            }
        } else {
            Spacer(Modifier.width(Space.m))
        }
    }
}
