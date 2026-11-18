package com.agentmux.mobile.ui.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalIconButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.agentmux.mobile.R
import com.agentmux.mobile.hub.MailConversation
import com.agentmux.mobile.hub.MailParty
import com.agentmux.mobile.ui.AppViewModel
import com.agentmux.mobile.ui.PollWhileResumed
import com.agentmux.mobile.ui.Route
import com.agentmux.mobile.ui.ThreadState
import com.agentmux.mobile.ui.UiState
import com.agentmux.mobile.ui.components.Avatar
import com.agentmux.mobile.ui.components.BarTitle
import com.agentmux.mobile.ui.components.Bubble
import com.agentmux.mobile.ui.components.Composer
import com.agentmux.mobile.ui.components.CountBadge
import com.agentmux.mobile.ui.components.EmptyState
import com.agentmux.mobile.ui.components.Hairline
import com.agentmux.mobile.ui.components.LargeTitle
import com.agentmux.mobile.ui.components.ListBottomPadding
import com.agentmux.mobile.ui.components.Notice
import com.agentmux.mobile.ui.components.SearchField
import com.agentmux.mobile.ui.components.SectionHeader
import com.agentmux.mobile.ui.components.Tag
import com.agentmux.mobile.ui.components.TimelineMark
import com.agentmux.mobile.ui.components.clockTime
import com.agentmux.mobile.ui.components.parseInstant
import com.agentmux.mobile.ui.components.relativeTime
import com.agentmux.mobile.ui.theme.AppIcons
import com.agentmux.mobile.ui.theme.AppTheme
import com.agentmux.mobile.ui.theme.Space
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun InboxScreen(state: UiState, vm: AppViewModel) {
    PollWhileResumed(15_000) { vm.refreshInbox() }
    val palette = AppTheme.palette
    val inbox = state.inbox
    var query by rememberSaveable { mutableStateOf("") }
    val conversations = inbox.value?.conversations.orEmpty().filter { it.matches(query) }
    val unread = inbox.value?.unread ?: 0
    PullToRefreshBox(isRefreshing = inbox.loading && inbox.value != null, onRefresh = vm::refreshInbox, modifier = Modifier.fillMaxSize()) {
        LazyColumn(Modifier.fillMaxSize(), contentPadding = ListBottomPadding) {
            item {
                LargeTitle(
                    title = stringResource(R.string.tab_inbox),
                    summary = if (unread > 0) stringResource(R.string.inbox_unread, unread) else stringResource(R.string.inbox_all_read),
                ) {
                    FilledTonalIconButton(
                        onClick = { vm.open(Route.NewChat) },
                        colors = IconButtonDefaults.filledTonalIconButtonColors(containerColor = palette.accentSoft, contentColor = palette.accent),
                    ) { Icon(Icons.Filled.Add, contentDescription = stringResource(R.string.new_chat)) }
                }
            }
            item { SearchField(query, { query = it }, stringResource(R.string.search_conversations)) }
            inbox.error?.let { error -> item { Notice(error, action = stringResource(R.string.retry), onAction = vm::refreshInbox) } }
            when {
                inbox.value == null && inbox.loading -> item { LinearProgressIndicator(Modifier.fillMaxWidth().padding(Space.l)) }
                inbox.value != null && conversations.isEmpty() -> item {
                    if (query.isBlank()) {
                        EmptyState(AppIcons.Chat, stringResource(R.string.no_messages), stringResource(R.string.no_messages_hint))
                    } else {
                        EmptyState(AppIcons.Chat, stringResource(R.string.no_results))
                    }
                }
                else -> items(conversations, key = { it.peer.address }) { conversation ->
                    ConversationRow(conversation) { vm.open(Route.Thread(conversation.peer)) }
                    Hairline(start = 76.dp)
                }
            }
        }
    }
}

private fun MailConversation.matches(query: String): Boolean {
    val q = query.trim()
    if (q.isEmpty()) return true
    return peer.shownName.contains(q, ignoreCase = true) || last.text.contains(q, ignoreCase = true) ||
        peer.machine.orEmpty().contains(q, ignoreCase = true)
}

@Composable
private fun ConversationRow(conversation: MailConversation, onClick: () -> Unit) {
    val palette = AppTheme.palette
    val peer = conversation.peer
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onClick).padding(horizontal = Space.l, vertical = Space.m),
        verticalAlignment = Alignment.Top,
    ) {
        Avatar(peer.shownName, seed = peer.address, icon = if (peer.isAgent) AppIcons.Agent else null, online = peer.online)
        Spacer(Modifier.width(Space.m))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    peer.shownName,
                    style = AppTheme.type.headline,
                    color = palette.ink,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false),
                )
                peer.machine?.takeIf { it.isNotBlank() }?.let {
                    Spacer(Modifier.width(6.dp))
                    Tag(it)
                }
                Spacer(Modifier.weight(1f))
                Text(relativeTime(conversation.last.createdAt), style = AppTheme.type.micro, color = palette.ink3)
            }
            Row(verticalAlignment = Alignment.Top) {
                Text(
                    conversation.last.text,
                    style = AppTheme.type.callout,
                    color = if (conversation.unread > 0) palette.ink else palette.ink2,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f),
                )
                if (conversation.unread > 0) {
                    Spacer(Modifier.width(Space.s))
                    CountBadge(conversation.unread)
                }
            }
        }
    }
}

/** One line of the thread: a message, or the date above a day's messages. */
private sealed interface ThreadLine {
    val key: String

    data class Day(val label: String) : ThreadLine { override val key get() = "day:$label" }
    data class Message(
        override val key: String,
        val text: String,
        val mine: Boolean,
        val time: String,
        val failed: String? = null,
        val pending: Boolean = false,
        val outgoingId: String? = null,
    ) : ThreadLine
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ThreadScreen(thread: ThreadState, me: String?, vm: AppViewModel) {
    PollWhileResumed(4_000, key = thread.peer.address) { vm.refreshThread() }
    val palette = AppTheme.palette
    val peer = thread.peer
    var draft by rememberSaveable(peer.address) { mutableStateOf("") }
    val sendFailed = stringResource(R.string.send_failed_retry)
    val lines = remember(thread, me) { threadLines(thread, me, sendFailed) }
    Scaffold(
        containerColor = palette.background,
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = { vm.back() }) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = stringResource(R.string.back)) }
                },
                title = {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Avatar(peer.shownName, seed = peer.address, size = 34.dp, icon = if (peer.isAgent) AppIcons.Agent else null, online = peer.online)
                        Spacer(Modifier.width(Space.s))
                        BarTitle(peer.shownName, listOfNotNull(
                            peer.machine?.takeIf { it.isNotBlank() },
                            peer.online?.let { stringResource(if (it) R.string.online else R.string.offline) },
                        ).joinToString(" · "))
                    }
                },
                actions = {
                    IconButton(onClick = vm::refreshThread) { Icon(Icons.Filled.Refresh, contentDescription = stringResource(R.string.refresh)) }
                },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = palette.background),
            )
        },
    ) { padding ->
        Column(Modifier.padding(padding).fillMaxSize().consumeWindowInsets(padding).imePadding()) {
            thread.messages.error?.let { Notice(it, action = stringResource(R.string.retry), onAction = vm::refreshThread) }
            if (thread.messages.value == null && thread.messages.loading) {
                LinearProgressIndicator(Modifier.fillMaxWidth())
            }
            LazyColumn(Modifier.weight(1f).fillMaxWidth(), reverseLayout = true) {
                items(lines.asReversed(), key = { it.key }) { line ->
                    when (line) {
                        is ThreadLine.Day -> TimelineMark(line.label)
                        is ThreadLine.Message -> Bubble(
                            text = line.text,
                            mine = line.mine,
                            time = line.time,
                            failed = line.failed,
                            pending = line.pending,
                            onRetry = line.outgoingId?.let { id -> { thread.outgoing.firstOrNull { it.id == id }?.let(vm::retry) } },
                        )
                    }
                }
                if (thread.messages.value?.isEmpty() == true && thread.outgoing.isEmpty()) {
                    item { EmptyState(AppIcons.Chat, stringResource(R.string.thread_empty, peer.shownName)) }
                }
            }
            Composer(
                value = draft,
                onValueChange = { draft = it },
                placeholder = stringResource(R.string.message_placeholder, peer.shownName),
                onSend = {
                    vm.send(draft)
                    draft = ""
                },
            )
        }
    }
}

private fun threadLines(thread: ThreadState, me: String?, sendFailed: String): List<ThreadLine> {
    val zone = ZoneId.systemDefault()
    val dayFormat = DateTimeFormatter.ofLocalizedDate(FormatStyle.MEDIUM)
    val out = mutableListOf<ThreadLine>()
    var lastDay: String? = null
    for (message in thread.messages.value.orEmpty()) {
        val day = parseInstant(message.createdAt)?.atZone(zone)?.toLocalDate()?.let(dayFormat::format)
        if (day != null && day != lastDay) {
            out += ThreadLine.Day(day)
            lastDay = day
        }
        val mine = if (me != null) message.from == me else message.from != thread.peer.address
        out += ThreadLine.Message(
            key = "m:" + message.id.ifBlank { message.createdAt + message.text.hashCode() },
            text = message.text,
            mine = mine,
            time = clockTime(message.createdAt, withDate = false),
            failed = message.error?.takeIf { message.failedAt != null },
        )
    }
    for (outgoing in thread.outgoing) {
        out += ThreadLine.Message(
            key = "o:" + outgoing.id,
            text = outgoing.text,
            mine = true,
            time = "",
            failed = outgoing.failed?.let { "$it · $sendFailed" },
            pending = outgoing.failed == null,
            outgoingId = outgoing.id,
        )
    }
    return out
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun NewChatScreen(state: UiState, vm: AppViewModel) {
    val palette = AppTheme.palette
    var query by rememberSaveable { mutableStateOf("") }
    val directory = state.directory
    val me = state.inbox.value?.me?.address
    val reachable = directory.value?.reachable.orEmpty().toSet()
    val parties = directory.value?.parties.orEmpty()
        .filter { it.address != me && (reachable.isEmpty() || it.address in reachable) }
        .filter { query.isBlank() || it.shownName.contains(query.trim(), true) || it.machine.orEmpty().contains(query.trim(), true) }
    val agents = parties.filter { it.isAgent }.sortedBy { it.shownName.lowercase() }
    val people = parties.filterNot { it.isAgent }.sortedBy { it.shownName.lowercase() }
    Scaffold(
        containerColor = palette.background,
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = { vm.back() }) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = stringResource(R.string.back)) }
                },
                title = { BarTitle(stringResource(R.string.new_chat)) },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = palette.background),
            )
        },
    ) { padding ->
        LazyColumn(Modifier.padding(padding).fillMaxSize(), contentPadding = ListBottomPadding) {
            item { SearchField(query, { query = it }, stringResource(R.string.search_people_agents)) }
            directory.error?.let { item { Notice(it, action = stringResource(R.string.retry), onAction = vm::refreshDirectory) } }
            if (directory.value == null && directory.loading) item { LinearProgressIndicator(Modifier.fillMaxWidth().padding(Space.l)) }
            if (directory.value != null && parties.isEmpty()) item { EmptyState(AppIcons.Agent, stringResource(R.string.no_results)) }
            if (agents.isNotEmpty()) {
                item { SectionHeader(stringResource(R.string.section_agents), agents.size) }
                items(agents, key = { "a:" + it.address }) { PartyRow(it) { vm.open(Route.Thread(it)) } }
            }
            if (people.isNotEmpty()) {
                item { SectionHeader(stringResource(R.string.section_people), people.size) }
                items(people, key = { "p:" + it.address }) { PartyRow(it) { vm.open(Route.Thread(it)) } }
            }
        }
    }
}

@Composable
fun PartyRow(party: MailParty, onClick: () -> Unit) {
    val palette = AppTheme.palette
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onClick).padding(horizontal = Space.l, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Avatar(party.shownName, seed = party.address, size = 40.dp, icon = if (party.isAgent) AppIcons.Agent else null, online = party.online)
        Spacer(Modifier.width(Space.m))
        Column(Modifier.weight(1f)) {
            Text(party.shownName, style = AppTheme.type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            val detail = listOfNotNull(party.machine?.takeIf { it.isNotBlank() }, party.description?.takeIf { it.isNotBlank() }, party.ownerName)
                .joinToString(" · ")
            if (detail.isNotEmpty()) {
                Text(detail, style = AppTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}
