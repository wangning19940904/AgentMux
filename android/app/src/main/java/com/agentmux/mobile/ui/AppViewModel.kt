package com.agentmux.mobile.ui

import android.app.Application
import android.os.Build
import android.provider.Settings
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.agentmux.mobile.BuildConfig
import com.agentmux.mobile.hub.AccountIdentity
import com.agentmux.mobile.hub.AgentInstance
import com.agentmux.mobile.hub.AgentSession
import com.agentmux.mobile.hub.FleetRead
import com.agentmux.mobile.hub.HubClient
import com.agentmux.mobile.hub.HubException
import com.agentmux.mobile.hub.HubLink
import com.agentmux.mobile.hub.HubStore
import com.agentmux.mobile.hub.MailDirectory
import com.agentmux.mobile.hub.MailMessage
import com.agentmux.mobile.hub.MailOverview
import com.agentmux.mobile.hub.MailParty
import com.agentmux.mobile.hub.OnMachine
import com.agentmux.mobile.hub.PhoneDevice
import com.agentmux.mobile.hub.PhoneIdentity
import com.agentmux.mobile.hub.SessionMessage
import java.util.UUID
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** Where the app is: no hub yet, signing in, or signed in. */
sealed interface Phase {
    data object Connect : Phase
    data class SignIn(val link: HubLink) : Phase
    data class Ready(val link: HubLink, val account: AccountIdentity) : Phase
}

enum class Tab { Inbox, Agents, Devices, Me }

/** A page pushed above the tabs. */
sealed interface Route {
    data class Thread(val peer: MailParty) : Route
    data object NewChat : Route
    data class Agent(val agent: OnMachine<AgentInstance>) : Route
    data class Session(val session: OnMachine<AgentSession>) : Route
}

/** A value read from the hub: kept while it refreshes, with the last failure. */
data class Loadable<T>(val value: T? = null, val loading: Boolean = false, val error: String? = null)

/** A message this phone is sending, shown until the hub has it. */
data class Outgoing(val id: String, val text: String, val failed: String? = null)

data class ThreadState(
    val peer: MailParty,
    val messages: Loadable<List<MailMessage>> = Loadable(loading = true),
    val outgoing: List<Outgoing> = emptyList(),
)

data class TranscriptState(
    val session: OnMachine<AgentSession>,
    val messages: Loadable<List<SessionMessage>> = Loadable(loading = true),
    val outgoing: List<Outgoing> = emptyList(),
)

/** A computer or server, from the hub's fleet (administrators) or the paired devices (members). */
data class MachineRow(
    val id: String,
    val name: String,
    val online: Boolean,
    val version: String?,
    val lastSeenAt: String?,
    val hubRole: String?,
)

data class UiState(
    val phase: Phase = Phase.Connect,
    val busy: Boolean = false,
    val error: String? = null,
    val tab: Tab = Tab.Inbox,
    val stack: List<Route> = emptyList(),
    val inbox: Loadable<MailOverview> = Loadable(),
    val directory: Loadable<MailDirectory> = Loadable(),
    val thread: ThreadState? = null,
    val agents: Loadable<FleetRead<AgentInstance>> = Loadable(),
    val sessions: Loadable<FleetRead<AgentSession>> = Loadable(),
    val transcript: TranscriptState? = null,
    val machines: Loadable<List<MachineRow>> = Loadable(),
    val phones: Loadable<List<PhoneDevice>> = Loadable(),
) {
    val account: AccountIdentity? get() = (phase as? Phase.Ready)?.account
    val admin: Boolean get() = account?.isAdmin == true
    val tabs: List<Tab> get() = if (admin) Tab.entries else listOf(Tab.Inbox, Tab.Devices, Tab.Me)
}

class AppViewModel(application: Application) : AndroidViewModel(application) {
    private val store = HubStore(application)
    private var client: HubClient? = null

    private val _state = MutableStateFlow(UiState())
    val state: StateFlow<UiState> = _state.asStateFlow()

    /** What this phone calls itself to the hub. */
    val deviceName: String = run {
        val named = runCatching {
            Settings.Global.getString(application.contentResolver, Settings.Global.DEVICE_NAME)
        }.getOrNull()
        named?.takeIf { it.isNotBlank() } ?: listOf(Build.MANUFACTURER, Build.MODEL)
            .filter { !it.isNullOrBlank() }.distinct().joinToString(" ")
    }
    private val userAgent = PhoneIdentity.userAgent(BuildConfig.VERSION_NAME, deviceName, Build.VERSION.RELEASE ?: "")

    init {
        store.link?.let { link ->
            client = newClient(link)
            _state.value = UiState(phase = Phase.SignIn(link))
            resume()
        }
    }

    private fun newClient(link: HubLink) = HubClient(link, store, userAgent = userAgent)

    // Connecting and signing in.

    /** A link from the QR scanner, a deep link or the paste field. */
    fun useLink(text: String) {
        val link = try {
            HubLink.parse(text)
        } catch (e: IllegalArgumentException) {
            _state.update { it.copy(error = e.message) }
            return
        }
        store.link = link
        client = newClient(link)
        _state.value = UiState(phase = Phase.SignIn(link))
        resume()
    }

    fun signIn(login: String, password: String) = action { client ->
        val account = client.signIn(login.trim(), password)
        enter(client, account)
    }

    fun signOut() = action { client ->
        runCatching { client.signOut() }
        _state.value = UiState(phase = Phase.SignIn(client.link))
    }

    fun forgetHub() {
        val client = client
        if (client != null) viewModelScope.launch { runCatching { client.signOut() } }
        store.link = null
        this.client = null
        _state.value = UiState(phase = Phase.Connect)
    }

    fun dismissError() = _state.update { it.copy(error = null) }

    /** Opens on the tabs when the stored session is still good. */
    private fun resume() = action(quiet = true) { client ->
        val session = client.session()
        val account = session.account
        if (session.authenticated && account != null) enter(client, account)
    }

    private fun enter(client: HubClient, account: AccountIdentity) {
        _state.update { UiState(phase = Phase.Ready(client.link, account)) }
        refreshInbox()
    }

    // Navigation.

    fun selectTab(tab: Tab) = _state.update { it.copy(tab = tab, stack = emptyList(), thread = null, transcript = null) }

    fun open(route: Route) {
        _state.update { state ->
            when (route) {
                is Route.Thread -> state.copy(stack = state.stack.filterNot { it is Route.NewChat } + route, thread = ThreadState(route.peer))
                is Route.Session -> state.copy(stack = state.stack + route, transcript = TranscriptState(route.session))
                else -> state.copy(stack = state.stack + route)
            }
        }
        when (route) {
            is Route.Thread -> refreshThread()
            is Route.Session -> refreshTranscript()
            Route.NewChat -> refreshDirectory()
            is Route.Agent -> if (_state.value.directory.value == null) refreshDirectory()
        }
    }

    /** Goes back one page; false when already at the tabs. */
    fun back(): Boolean {
        val stack = _state.value.stack
        if (stack.isEmpty()) return false
        val leaving = stack.last()
        _state.update { state ->
            state.copy(
                stack = stack.dropLast(1),
                thread = if (leaving is Route.Thread) null else state.thread,
                transcript = if (leaving is Route.Session) null else state.transcript,
            )
        }
        if (leaving is Route.Thread) refreshInbox()
        return true
    }

    // Inbox and threads.

    fun refreshInbox() = load({ it.inbox }, { s, v -> s.copy(inbox = v) }) { it.mailbox() }

    fun refreshDirectory() = load({ it.directory }, { s, v -> s.copy(directory = v) }) { it.directory() }

    fun refreshThread() {
        val peer = _state.value.thread?.peer ?: return
        val client = client ?: return
        viewModelScope.launch {
            updateThread(peer.address) { it.copy(messages = it.messages.copy(loading = true)) }
            try {
                val messages = client.messages(peer.address)
                updateThread(peer.address) { it.copy(messages = Loadable(messages)) }
            } catch (e: HubException.Unauthorized) {
                signedOut(e)
            } catch (e: HubException) {
                updateThread(peer.address) { it.copy(messages = it.messages.copy(loading = false, error = e.message)) }
            }
        }
    }

    fun send(text: String) {
        val peer = _state.value.thread?.peer ?: return
        val body = text.trim()
        if (body.isEmpty()) return
        val outgoing = Outgoing(UUID.randomUUID().toString(), body)
        updateThread(peer.address) { it.copy(outgoing = it.outgoing + outgoing) }
        deliver(peer.address, outgoing)
    }

    fun retry(outgoing: Outgoing) {
        val peer = _state.value.thread?.peer ?: return
        updateThread(peer.address) { thread ->
            thread.copy(outgoing = thread.outgoing.map { if (it.id == outgoing.id) it.copy(failed = null) else it })
        }
        deliver(peer.address, outgoing)
    }

    private fun deliver(address: String, outgoing: Outgoing) {
        val client = client ?: return
        viewModelScope.launch {
            try {
                val sent = client.sendMessage(address, outgoing.text)
                updateThread(address) { thread ->
                    val messages = thread.messages.value.orEmpty().filterNot { it.id == sent.id } + sent
                    thread.copy(messages = thread.messages.copy(value = messages), outgoing = thread.outgoing.filterNot { it.id == outgoing.id })
                }
            } catch (e: HubException.Unauthorized) {
                signedOut(e)
            } catch (e: HubException) {
                updateThread(address) { thread ->
                    thread.copy(outgoing = thread.outgoing.map { if (it.id == outgoing.id) it.copy(failed = e.message) else it })
                }
            }
        }
    }

    private fun updateThread(address: String, change: (ThreadState) -> ThreadState) = _state.update { state ->
        val thread = state.thread
        if (thread == null || thread.peer.address != address) state else state.copy(thread = change(thread))
    }

    // Agents and sessions.

    fun refreshAgents() = load({ it.agents }, { s, v -> s.copy(agents = v) }) { it.agents() }

    fun refreshSessions() = load({ it.sessions }, { s, v -> s.copy(sessions = v) }) { it.sessions() }

    fun refreshTranscript() {
        val target = _state.value.transcript?.session ?: return
        val client = client ?: return
        viewModelScope.launch {
            updateTranscript(target) { it.copy(messages = it.messages.copy(loading = true)) }
            try {
                val messages = client.sessionMessages(target.machineId, target.item)
                updateTranscript(target) { it.copy(messages = Loadable(messages)) }
            } catch (e: HubException.Unauthorized) {
                signedOut(e)
            } catch (e: HubException) {
                updateTranscript(target) { it.copy(messages = it.messages.copy(loading = false, error = e.message)) }
            }
        }
    }

    fun sendToSession(text: String) {
        val target = _state.value.transcript?.session ?: return
        val client = client ?: return
        val body = text.trim()
        if (body.isEmpty()) return
        val outgoing = Outgoing(UUID.randomUUID().toString(), body)
        updateTranscript(target) { it.copy(outgoing = it.outgoing + outgoing) }
        viewModelScope.launch {
            try {
                client.sendSessionMessage(target.machineId, target.item, body)
                updateTranscript(target) { it.copy(outgoing = it.outgoing.filterNot { o -> o.id == outgoing.id }) }
                refreshTranscript()
            } catch (e: HubException.Unauthorized) {
                signedOut(e)
            } catch (e: HubException) {
                updateTranscript(target) { t ->
                    t.copy(outgoing = t.outgoing.map { if (it.id == outgoing.id) it.copy(failed = e.message) else it })
                }
            }
        }
    }

    private fun updateTranscript(target: OnMachine<AgentSession>, change: (TranscriptState) -> TranscriptState) =
        _state.update { state ->
            val transcript = state.transcript
            if (transcript == null || transcript.session != target) state else state.copy(transcript = change(transcript))
        }

    /** The mailbox address to message an agent at, when the directory lists it. */
    fun addressOf(agent: OnMachine<AgentInstance>): MailParty? {
        val parties = _state.value.directory.value?.parties.orEmpty().filter { it.isAgent }
        return parties.firstOrNull { it.address == "agent:${agent.item.id}@${agent.machineId}" }
            ?: parties.firstOrNull { it.address.substringAfter(':').substringBefore('@') == agent.item.id }
            ?: parties.firstOrNull { it.name == agent.item.name && (it.machine == null || it.machine == agent.machineName) }
    }

    // Devices.

    fun refreshDevices() {
        val admin = _state.value.admin
        load({ it.machines }, { s, v -> s.copy(machines = v) }) { client ->
            if (admin) {
                client.fleet().machines.orEmpty().map { MachineRow(it.id, it.name, it.online, it.version, it.lastSeenAt, it.hubRole) }
            } else {
                client.pairedDevices().map { MachineRow(it.id, it.name.ifBlank { it.hostname.orEmpty() }, it.online, it.version, it.lastSeenAt, null) }
            }
        }
        load({ it.phones }, { s, v -> s.copy(phones = v) }) { it.phones() }
    }

    fun signOutPhone(phone: PhoneDevice) = action { client ->
        client.signOutPhone(phone.id)
        _state.update { state ->
            state.copy(phones = state.phones.copy(value = state.phones.value?.filterNot { it.id == phone.id }))
        }
    }

    // Plumbing.

    /** Reads one value into the state, keeping the old one while it loads. */
    private fun <T> load(
        get: (UiState) -> Loadable<T>,
        set: (UiState, Loadable<T>) -> UiState,
        fetch: suspend (HubClient) -> T,
    ) {
        val client = client ?: return
        viewModelScope.launch {
            _state.update { set(it, get(it).copy(loading = true)) }
            try {
                val value = fetch(client)
                _state.update { set(it, Loadable(value)) }
            } catch (e: HubException.Unauthorized) {
                signedOut(e)
            } catch (e: HubException) {
                _state.update { set(it, get(it).copy(loading = false, error = e.message)) }
            }
        }
    }

    /** Runs something the person asked for; a failure shows as a message. */
    private fun action(quiet: Boolean = false, block: suspend (HubClient) -> Unit) {
        val client = client ?: return
        viewModelScope.launch {
            _state.update { it.copy(busy = true, error = null) }
            try {
                block(client)
            } catch (e: HubException.Unauthorized) {
                signedOut(if (quiet) null else e)
            } catch (e: HubException) {
                _state.update { it.copy(error = e.message) }
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }

    private fun signedOut(e: HubException?) {
        val link = client?.link ?: return
        _state.value = UiState(phase = Phase.SignIn(link), error = e?.message)
    }
}
