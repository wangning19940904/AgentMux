package com.agentmux.mobile.ui.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
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
import com.agentmux.mobile.hub.AgentInstance
import com.agentmux.mobile.hub.AgentSession
import com.agentmux.mobile.hub.OnMachine
import com.agentmux.mobile.hub.SessionMessage
import com.agentmux.mobile.ui.AppViewModel
import com.agentmux.mobile.ui.PollWhileResumed
import com.agentmux.mobile.ui.Route
import com.agentmux.mobile.ui.TranscriptState
import com.agentmux.mobile.ui.UiState
import com.agentmux.mobile.ui.components.Avatar
import com.agentmux.mobile.ui.components.BarTitle
import com.agentmux.mobile.ui.components.Bubble
import com.agentmux.mobile.ui.components.ChipItem
import com.agentmux.mobile.ui.components.ChipRow
import com.agentmux.mobile.ui.components.Composer
import com.agentmux.mobile.ui.components.EmptyState
import com.agentmux.mobile.ui.components.Group
import com.agentmux.mobile.ui.components.Hairline
import com.agentmux.mobile.ui.components.InfoRow
import com.agentmux.mobile.ui.components.LargeTitle
import com.agentmux.mobile.ui.components.ListBottomPadding
import com.agentmux.mobile.ui.components.Notice
import com.agentmux.mobile.ui.components.SearchField
import com.agentmux.mobile.ui.components.SectionHeader
import com.agentmux.mobile.ui.components.Segmented
import com.agentmux.mobile.ui.components.StatusDot
import com.agentmux.mobile.ui.components.Tag
import com.agentmux.mobile.ui.components.WorkStep
import com.agentmux.mobile.ui.components.baseName
import com.agentmux.mobile.ui.components.clockTime
import com.agentmux.mobile.ui.components.relativeTime
import com.agentmux.mobile.ui.components.shortPath
import com.agentmux.mobile.ui.theme.AppIcons
import com.agentmux.mobile.ui.theme.AppTheme
import com.agentmux.mobile.ui.theme.Space

private const val ALL = "*"

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AgentsScreen(state: UiState, vm: AppViewModel) {
    var view by rememberSaveable { mutableStateOf(0) }
    var machine by rememberSaveable { mutableStateOf(ALL) }
    var query by rememberSaveable { mutableStateOf("") }
    PollWhileResumed(30_000, key = view) { if (view == 0) vm.refreshAgents() else vm.refreshSessions() }
    LaunchedEffect(Unit) { if (state.machines.value == null) vm.refreshDevices() }

    val agents = state.agents
    val sessions = state.sessions
    val loadable = if (view == 0) agents else sessions
    val agentItems = agents.value?.items.orEmpty()
    val sessionItems = sessions.value?.items.orEmpty()
    val machineNames = (if (view == 0) agentItems.map { it.machineId to it.machineName } else sessionItems.map { it.machineId to it.machineName })
        .distinct().sortedBy { it.second.lowercase() }
    val online = state.machines.value.orEmpty().associate { it.id to it.online }
    val q = query.trim()

    PullToRefreshBox(
        isRefreshing = loadable.loading && loadable.value != null,
        onRefresh = { if (view == 0) vm.refreshAgents() else vm.refreshSessions() },
        modifier = Modifier.fillMaxSize(),
    ) {
        LazyColumn(Modifier.fillMaxSize(), contentPadding = ListBottomPadding) {
            item {
                val working = sessionItems.count { it.item.working }
                LargeTitle(
                    stringResource(R.string.tab_agents),
                    summary = if (view == 0) {
                        stringResource(R.string.agents_summary, agentItems.size, machineNames.size)
                    } else {
                        stringResource(R.string.sessions_summary, sessionItems.size, working)
                    },
                )
            }
            item {
                Segmented(
                    listOf(stringResource(R.string.view_agents), stringResource(R.string.view_sessions)),
                    view,
                    { view = it; machine = ALL },
                )
            }
            item { SearchField(query, { query = it }, stringResource(if (view == 0) R.string.search_agents else R.string.search_sessions)) }
            if (machineNames.size > 1) {
                item {
                    val counts = (if (view == 0) agentItems.map { it.machineId } else sessionItems.map { it.machineId }).groupingBy { it }.eachCount()
                    ChipRow(
                        listOf(ChipItem(ALL, stringResource(R.string.all_machines), counts.values.sum())) +
                            machineNames.map { (id, name) -> ChipItem(id, name, counts[id], online[id]) },
                        machine,
                    ) { machine = it }
                }
            }
            loadable.error?.let { error ->
                item { Notice(error, action = stringResource(R.string.retry)) { if (view == 0) vm.refreshAgents() else vm.refreshSessions() } }
            }
            loadable.value?.failures?.takeIf { it.isNotEmpty() }?.let { failures ->
                item { Notice(stringResource(R.string.some_machines_failed, failures.joinToString("; ")), color = AppTheme.palette.needsYou) }
            }
            if (loadable.value == null && loadable.loading) {
                item { LinearProgressIndicator(Modifier.fillMaxWidth().padding(Space.l)) }
            }
            if (view == 0) {
                val shown = agentItems.filter { (machine == ALL || it.machineId == machine) && it.item.matches(q) }
                if (agents.value != null && shown.isEmpty()) {
                    item { EmptyState(AppIcons.Agent, stringResource(if (q.isEmpty()) R.string.no_agents else R.string.no_results)) }
                }
                shown.groupBy { it.machineId }.toSortedMap(compareBy { id -> shown.first { it.machineId == id }.machineName.lowercase() })
                    .forEach { (id, group) ->
                        item(key = "h:$id") { MachineHeader(group.first().machineName, online[id], group.size) }
                        items(group.sortedBy { it.item.name.lowercase() }, key = { "a:${it.machineId}:${it.item.id}" }) { agent ->
                            AgentRow(agent) { vm.open(Route.Agent(agent)) }
                            Hairline(start = 72.dp)
                        }
                    }
            } else {
                val shown = sessionItems.filter { (machine == ALL || it.machineId == machine) && it.item.matches(q) }
                if (sessions.value != null && shown.isEmpty()) {
                    item { EmptyState(AppIcons.Code, stringResource(if (q.isEmpty()) R.string.no_sessions else R.string.no_results)) }
                }
                val (working, rest) = shown.partition { it.item.working }
                if (working.isNotEmpty()) {
                    item { SectionHeader(stringResource(R.string.section_working), working.size) }
                    items(working, key = { "w:${it.machineId}:${it.item.providerId}:${it.item.sessionId}" }) { session ->
                        SessionRow(session) { vm.open(Route.Session(session)) }
                        Hairline(start = 72.dp)
                    }
                }
                if (rest.isNotEmpty()) {
                    item { SectionHeader(stringResource(R.string.section_recent), rest.size) }
                    items(rest.take(200), key = { "s:${it.machineId}:${it.item.providerId}:${it.item.sessionId}" }) { session ->
                        SessionRow(session) { vm.open(Route.Session(session)) }
                        Hairline(start = 72.dp)
                    }
                }
            }
        }
    }
}

private fun AgentInstance.matches(q: String) =
    q.isEmpty() || name.contains(q, true) || runtimeId.contains(q, true) || workDir.orEmpty().contains(q, true)

private fun AgentSession.matches(q: String) =
    q.isEmpty() || shownTitle.contains(q, true) || projectDir.orEmpty().contains(q, true) ||
        agentName.orEmpty().contains(q, true) || providerId.contains(q, true)

@Composable
private fun MachineHeader(name: String, online: Boolean?, count: Int) {
    val palette = AppTheme.palette
    Row(
        Modifier.fillMaxWidth().padding(start = Space.l, end = Space.l, top = Space.l, bottom = Space.xs),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(AppIcons.Computer, contentDescription = null, tint = palette.ink2, modifier = Modifier.padding(end = 6.dp).height(16.dp))
        Text(name, style = AppTheme.type.headline, color = palette.ink)
        if (online != null) {
            Spacer(Modifier.width(Space.s))
            StatusDot(if (online) palette.positive else palette.idle, 7.dp)
            Spacer(Modifier.width(4.dp))
            Text(stringResource(if (online) R.string.online else R.string.offline), style = AppTheme.type.micro, color = palette.ink2)
        }
        Spacer(Modifier.weight(1f))
        Text(count.toString(), style = AppTheme.type.micro, color = palette.ink3)
    }
}

@Composable
private fun AgentRow(agent: OnMachine<AgentInstance>, onClick: () -> Unit) {
    val palette = AppTheme.palette
    val item = agent.item
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onClick).padding(horizontal = Space.l, vertical = Space.m),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Avatar(item.name, seed = item.id, size = 40.dp, icon = AppIcons.Agent)
        Spacer(Modifier.width(Space.m))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(item.name, style = AppTheme.type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false))
                if (!item.enabled) {
                    Spacer(Modifier.width(6.dp))
                    Tag(stringResource(R.string.disabled))
                }
            }
            Text(
                listOfNotNull(item.runtimeId.ifBlank { null }, item.defaultModel?.ifBlank { null }).joinToString(" · "),
                style = AppTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
            item.workDir?.takeIf { it.isNotBlank() }?.let {
                Text(shortPath(it), style = AppTheme.type.micro, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

@Composable
private fun SessionRow(session: OnMachine<AgentSession>, onClick: () -> Unit) {
    val palette = AppTheme.palette
    val item = session.item
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onClick).padding(horizontal = Space.l, vertical = Space.m),
        verticalAlignment = Alignment.Top,
    ) {
        Avatar(item.providerId.ifBlank { "?" }, seed = item.providerId, size = 40.dp, icon = AppIcons.Code)
        Spacer(Modifier.width(Space.m))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (item.working) {
                    StatusDot(palette.working)
                    Spacer(Modifier.width(6.dp))
                }
                Text(item.shownTitle, style = AppTheme.type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f))
                Text(relativeTime(item.lastActiveAt), style = AppTheme.type.micro, color = palette.ink3)
            }
            Text(
                listOfNotNull(
                    item.agentName?.ifBlank { null } ?: item.providerId.ifBlank { null },
                    session.machineName,
                    baseName(item.projectDir).ifBlank { null },
                ).joinToString(" · "),
                style = AppTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
            Text(
                stringResource(R.string.message_count, item.messageCount) +
                    (item.channelName?.takeIf { it.isNotBlank() }?.let { " · $it" } ?: ""),
                style = AppTheme.type.micro, color = palette.ink3,
            )
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AgentDetailScreen(agent: OnMachine<AgentInstance>, state: UiState, vm: AppViewModel) {
    val palette = AppTheme.palette
    val item = agent.item
    val address = remember(state.directory.value, agent) { vm.addressOf(agent) }
    val sessions = state.sessions.value?.items.orEmpty()
        .filter { it.machineId == agent.machineId && it.item.agentName == item.name }
    Scaffold(
        containerColor = palette.background,
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = { vm.back() }) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = stringResource(R.string.back)) }
                },
                title = { BarTitle(item.name, agent.machineName) },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = palette.background),
            )
        },
    ) { padding ->
        Column(Modifier.padding(padding).fillMaxSize().verticalScroll(rememberScrollState()).padding(bottom = Space.xl)) {
            Column(Modifier.fillMaxWidth().padding(Space.l), horizontalAlignment = Alignment.CenterHorizontally) {
                Avatar(item.name, seed = item.id, size = 72.dp, icon = AppIcons.Agent)
                Spacer(Modifier.height(Space.s))
                Text(item.name, style = AppTheme.type.title, color = palette.ink)
                Text(
                    listOf(agent.machineName, item.runtimeId).filter { it.isNotBlank() }.joinToString(" · "),
                    style = AppTheme.type.caption, color = palette.ink2,
                )
                Spacer(Modifier.height(Space.m))
                Button(
                    onClick = { address?.let { vm.open(Route.Thread(it)) } },
                    enabled = address != null,
                    shape = RoundedCornerShape(12.dp),
                ) {
                    Icon(AppIcons.Chat, contentDescription = null, modifier = Modifier.height(18.dp))
                    Spacer(Modifier.width(Space.s))
                    Text(stringResource(R.string.message_agent))
                }
                if (address == null && state.directory.value != null) {
                    Text(stringResource(R.string.agent_not_in_directory), style = AppTheme.type.micro, color = palette.ink3,
                        modifier = Modifier.padding(top = Space.xs))
                }
            }
            SectionHeader(stringResource(R.string.section_details))
            Group {
                val rows = listOfNotNull(
                    stringResource(R.string.field_status) to stringResource(if (item.enabled) R.string.enabled else R.string.disabled),
                    item.defaultModel?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.field_model) to it },
                    item.workDir?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.field_work_dir) to it },
                    item.workspaceMode?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.field_workspace) to it },
                    item.sessionBackend?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.field_backend) to it },
                    stringResource(R.string.field_channels) to (item.channelBindings?.size ?: 0).toString(),
                    stringResource(R.string.field_schedules) to (item.schedules?.size ?: 0).toString(),
                    item.skills?.takeIf { it.isNotEmpty() }?.let { stringResource(R.string.field_skills) to it.joinToString(", ") },
                )
                rows.forEachIndexed { index, (label, value) ->
                    if (index > 0) Hairline(start = Space.l)
                    InfoRow(title = label) {
                        Text(value, style = AppTheme.type.callout, color = palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.padding(start = Space.m).fillMaxWidth(0.6f))
                    }
                }
            }
            item.systemPrompt?.takeIf { it.isNotBlank() }?.let { prompt ->
                SectionHeader(stringResource(R.string.field_system_prompt))
                Group {
                    SelectionContainer {
                        Text(prompt, style = AppTheme.type.callout, color = palette.ink, maxLines = 30, overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.padding(Space.l))
                    }
                }
            }
            if (sessions.isNotEmpty()) {
                SectionHeader(stringResource(R.string.view_sessions), sessions.size)
                sessions.take(20).forEach { session ->
                    SessionRow(session) { vm.open(Route.Session(session)) }
                    Hairline(start = 72.dp)
                }
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SessionScreen(transcript: TranscriptState, vm: AppViewModel) {
    val target = transcript.session
    val session = target.item
    PollWhileResumed(if (session.working) 4_000 else 15_000, key = session.sessionId) { vm.refreshTranscript() }
    val palette = AppTheme.palette
    var draft by rememberSaveable(session.sessionId) { mutableStateOf("") }
    val messages = transcript.messages.value.orEmpty()
    Scaffold(
        containerColor = palette.background,
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = { vm.back() }) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = stringResource(R.string.back)) }
                },
                title = {
                    BarTitle(
                        session.shownTitle,
                        listOfNotNull(
                            if (session.working) stringResource(R.string.working) else null,
                            target.machineName,
                            baseName(session.projectDir).ifBlank { null },
                        ).joinToString(" · "),
                    )
                },
                actions = {
                    IconButton(onClick = vm::refreshTranscript) { Icon(Icons.Filled.Refresh, contentDescription = stringResource(R.string.refresh)) }
                },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = palette.background),
            )
        },
    ) { padding ->
        Column(Modifier.padding(padding).fillMaxSize().consumeWindowInsets(padding).imePadding()) {
            transcript.messages.error?.let { Notice(it, action = stringResource(R.string.retry), onAction = vm::refreshTranscript) }
            if (transcript.messages.value == null && transcript.messages.loading) LinearProgressIndicator(Modifier.fillMaxWidth())
            LazyColumn(Modifier.weight(1f).fillMaxWidth(), reverseLayout = true) {
                transcript.outgoing.asReversed().forEach { outgoing ->
                    item(key = "o:" + outgoing.id) {
                        Bubble(outgoing.text, mine = true, time = "", failed = outgoing.failed, pending = outgoing.failed == null)
                    }
                }
                val shown = messages.withIndex().toList().asReversed()
                items(shown, key = { "m:${it.index}" }) { (_, message) -> TranscriptLine(message) }
                if (transcript.messages.value?.isEmpty() == true) {
                    item { EmptyState(AppIcons.Code, stringResource(R.string.session_empty)) }
                }
            }
            if (session.canSend) {
                Composer(
                    value = draft,
                    onValueChange = { draft = it },
                    placeholder = stringResource(R.string.session_placeholder),
                    onSend = {
                        vm.sendToSession(draft)
                        draft = ""
                    },
                )
            } else {
                Text(
                    stringResource(R.string.session_read_only),
                    style = AppTheme.type.caption, color = palette.ink3,
                    modifier = Modifier.fillMaxWidth().navigationBarsPadding().padding(Space.l),
                )
            }
        }
    }
}

@Composable
private fun TranscriptLine(message: SessionMessage) {
    val role = message.role.lowercase()
    when {
        !message.toolName.isNullOrBlank() || message.kind == "tool" || role == "tool" -> WorkStep(
            title = message.toolName?.ifBlank { null } ?: message.content.lineSequence().firstOrNull().orEmpty(),
            detail = listOfNotNull(message.toolInput?.ifBlank { null }, message.toolOutput?.ifBlank { null }, message.content.ifBlank { null })
                .joinToString("\n\n"),
            failed = message.toolStatus == "error" || message.toolStatus == "failed",
        )
        message.content.isBlank() -> Unit
        role == "user" -> Bubble(message.content, mine = true, time = clockTime(message.timestamp, withDate = false))
        else -> Bubble(message.content, mine = false, time = clockTime(message.timestamp, withDate = false))
    }
}
