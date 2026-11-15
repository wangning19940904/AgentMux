package com.agentmux.mobile.ui

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.agentmux.mobile.hub.AccountIdentity
import com.agentmux.mobile.hub.HubClient
import com.agentmux.mobile.hub.HubException
import com.agentmux.mobile.hub.HubLink
import com.agentmux.mobile.hub.HubStore
import com.agentmux.mobile.hub.MailOverview
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** What the app shows. */
sealed interface Screen {
    /** No hub yet: scan or paste its link. */
    data object Connect : Screen
    data class SignIn(val link: HubLink) : Screen
    data class Home(val link: HubLink, val account: AccountIdentity, val mailbox: MailOverview?) : Screen
}

data class UiState(
    val screen: Screen = Screen.Connect,
    val busy: Boolean = false,
    val error: String? = null,
)

class AppViewModel(application: Application) : AndroidViewModel(application) {
    private val store = HubStore(application)
    private var client: HubClient? = null

    private val _state = MutableStateFlow(UiState())
    val state: StateFlow<UiState> = _state.asStateFlow()

    init {
        store.link?.let { link ->
            client = HubClient(link, store)
            _state.value = UiState(screen = Screen.SignIn(link))
            resume()
        }
    }

    /** A link from the QR scanner, a deep link or the paste field. */
    fun useLink(text: String) {
        val link = try {
            HubLink.parse(text)
        } catch (e: IllegalArgumentException) {
            _state.update { it.copy(error = e.message) }
            return
        }
        store.link = link
        client = HubClient(link, store)
        _state.value = UiState(screen = Screen.SignIn(link))
        resume()
    }

    fun signIn(login: String, password: String) = launchWith { client ->
        val account = client.signIn(login.trim(), password)
        showHome(client, account)
    }

    fun refresh() = launchWith { client ->
        val screen = _state.value.screen as? Screen.Home ?: return@launchWith
        _state.update { it.copy(screen = screen.copy(mailbox = client.mailbox())) }
    }

    fun signOut() = launchWith { client ->
        runCatching { client.signOut() }
        _state.update { it.copy(screen = Screen.SignIn(client.link)) }
    }

    fun forgetHub() {
        store.link = null
        client = null
        _state.value = UiState(screen = Screen.Connect)
    }

    fun dismissError() = _state.update { it.copy(error = null) }

    /** Opens on the home screen when the stored session is still good. */
    private fun resume() = launchWith(quiet = true) { client ->
        val session = client.session()
        val account = session.account
        if (session.authenticated && account != null) showHome(client, account)
    }

    private suspend fun showHome(client: HubClient, account: AccountIdentity) {
        _state.update { it.copy(screen = Screen.Home(client.link, account, null)) }
        val mailbox = client.mailbox()
        _state.update { it.copy(screen = Screen.Home(client.link, account, mailbox)) }
    }

    private fun launchWith(quiet: Boolean = false, block: suspend (HubClient) -> Unit) {
        val client = client ?: return
        viewModelScope.launch {
            _state.update { it.copy(busy = true, error = null) }
            try {
                block(client)
            } catch (e: HubException.Unauthorized) {
                _state.update { it.copy(screen = Screen.SignIn(client.link), error = if (quiet) null else e.message) }
            } catch (e: HubException) {
                _state.update { it.copy(error = e.message) }
            } finally {
                _state.update { it.copy(busy = false) }
            }
        }
    }
}
