package com.agentmux.mobile.ui

import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Person
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationBarItemDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import com.agentmux.mobile.R
import com.agentmux.mobile.ui.components.PageBox
import com.agentmux.mobile.ui.screens.AgentDetailScreen
import com.agentmux.mobile.ui.screens.AgentsScreen
import com.agentmux.mobile.ui.screens.ConnectScreen
import com.agentmux.mobile.ui.screens.DevicesScreen
import com.agentmux.mobile.ui.screens.InboxScreen
import com.agentmux.mobile.ui.screens.MeScreen
import com.agentmux.mobile.ui.screens.NewChatScreen
import com.agentmux.mobile.ui.screens.SessionScreen
import com.agentmux.mobile.ui.screens.SignInScreen
import com.agentmux.mobile.ui.screens.ThreadScreen
import com.agentmux.mobile.ui.theme.AppIcons
import com.agentmux.mobile.ui.theme.AppTheme
import kotlinx.coroutines.delay

@Composable
fun AgentMuxApp(state: UiState, vm: AppViewModel) {
    val snackbar = remember { SnackbarHostState() }
    LaunchedEffect(state.error) {
        state.error?.let {
            snackbar.showSnackbar(it)
            vm.dismissError()
        }
    }
    PageBox {
        when (val phase = state.phase) {
            Phase.Connect -> Shell(snackbar) { ConnectScreen(onLink = vm::useLink) }
            is Phase.SignIn -> Shell(snackbar) { SignInScreen(phase, state.busy, vm::signIn, vm::forgetHub) }
            is Phase.Ready -> SignedIn(state, phase, vm, snackbar)
        }
    }
}

@Composable
private fun Shell(snackbar: SnackbarHostState, content: @Composable () -> Unit) {
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        containerColor = AppTheme.palette.background,
    ) { padding ->
        Box(Modifier.padding(padding).fillMaxSize()) { content() }
    }
}

@Composable
private fun SignedIn(state: UiState, phase: Phase.Ready, vm: AppViewModel, snackbar: SnackbarHostState) {
    BackHandler(enabled = state.stack.isNotEmpty()) { vm.back() }
    val top = state.stack.lastOrNull()
    AnimatedContent(
        targetState = top,
        transitionSpec = {
            if (targetState != null) {
                slideInHorizontally { it / 3 } + fadeIn() togetherWith fadeOut()
            } else {
                fadeIn() togetherWith slideOutHorizontally { it / 3 } + fadeOut()
            }
        },
        label = "page",
    ) { route ->
        when (route) {
            null -> Tabs(state, phase, vm, snackbar)
            is Route.Thread -> state.thread?.let { ThreadScreen(it, state.inbox.value?.me?.address, vm) } ?: Box(Modifier.fillMaxSize())
            Route.NewChat -> NewChatScreen(state, vm)
            is Route.Agent -> AgentDetailScreen(route.agent, state, vm)
            is Route.Session -> state.transcript?.let { SessionScreen(it, vm) } ?: Box(Modifier.fillMaxSize())
        }
    }
}

@Composable
private fun Tabs(state: UiState, phase: Phase.Ready, vm: AppViewModel, snackbar: SnackbarHostState) {
    val palette = AppTheme.palette
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        containerColor = palette.background,
        bottomBar = {
            NavigationBar(containerColor = palette.surface, tonalElevation = 0.dp) {
                state.tabs.forEach { tab ->
                    val (icon, label) = tabLook(tab)
                    NavigationBarItem(
                        selected = state.tab == tab,
                        onClick = { vm.selectTab(tab) },
                        icon = {
                            val unread = if (tab == Tab.Inbox) state.inbox.value?.unread ?: 0 else 0
                            BadgedBox(badge = { if (unread > 0) Badge { Text(unread.toString()) } }) {
                                Icon(icon, contentDescription = null)
                            }
                        },
                        label = { Text(label) },
                        colors = NavigationBarItemDefaults.colors(
                            selectedIconColor = palette.accent,
                            selectedTextColor = palette.accent,
                            indicatorColor = palette.accentSoft,
                            unselectedIconColor = palette.ink2,
                            unselectedTextColor = palette.ink2,
                        ),
                    )
                }
            }
        },
    ) { padding ->
        Box(Modifier.padding(padding).fillMaxSize()) {
            when (if (state.tab in state.tabs) state.tab else Tab.Inbox) {
                Tab.Inbox -> InboxScreen(state, vm)
                Tab.Agents -> AgentsScreen(state, vm)
                Tab.Devices -> DevicesScreen(state, vm)
                Tab.Me -> MeScreen(state, phase, vm)
            }
        }
    }
}

@Composable
private fun tabLook(tab: Tab): Pair<ImageVector, String> = when (tab) {
    Tab.Inbox -> AppIcons.Chat to stringResource(R.string.tab_inbox)
    Tab.Agents -> AppIcons.Agent to stringResource(R.string.tab_agents)
    Tab.Devices -> AppIcons.Devices to stringResource(R.string.tab_devices)
    Tab.Me -> Icons.Filled.Person to stringResource(R.string.tab_me)
}

/**
 * Runs [refresh] now and then every [everyMillis] while the app is on
 * screen, so a page stays current without a push channel.
 */
@Composable
fun PollWhileResumed(everyMillis: Long, key: Any? = Unit, refresh: () -> Unit) {
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val current = rememberUpdatedState(refresh)
    LaunchedEffect(lifecycle, key) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) {
            while (true) {
                current.value()
                delay(everyMillis)
            }
        }
    }
}
