package com.agentmux.mobile.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Badge
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
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
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.agentmux.mobile.R
import com.agentmux.mobile.hub.MailConversation
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AgentMuxApp(state: UiState, vm: AppViewModel) {
    val snackbar = remember { SnackbarHostState() }
    LaunchedEffect(state.error) {
        state.error?.let {
            snackbar.showSnackbar(it)
            vm.dismissError()
        }
    }
    Scaffold(
        topBar = { TopAppBar(title = { Text(stringResource(R.string.app_name)) }) },
        snackbarHost = { SnackbarHost(snackbar) },
    ) { padding ->
        Column(Modifier.padding(padding).fillMaxSize()) {
            if (state.busy) LinearProgressIndicator(Modifier.fillMaxWidth())
            when (val screen = state.screen) {
                Screen.Connect -> ConnectScreen(onLink = vm::useLink)
                is Screen.SignIn -> SignInScreen(screen, state.busy, vm::signIn, vm::forgetHub)
                is Screen.Home -> HomeScreen(screen, vm::refresh, vm::signOut)
            }
        }
    }
}

@Composable
private fun ConnectScreen(onLink: (String) -> Unit) {
    val scanner = rememberLauncherForActivityResult(ScanContract()) { result ->
        result.contents?.let(onLink)
    }
    val scanPrompt = stringResource(R.string.scan_prompt)
    var pasted by rememberSaveable { mutableStateOf("") }
    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text(stringResource(R.string.connect_intro), style = MaterialTheme.typography.bodyLarge)
        Button(
            onClick = {
                scanner.launch(
                    ScanOptions()
                        .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                        .setPrompt(scanPrompt)
                        .setBeepEnabled(false)
                        .setOrientationLocked(false),
                )
            },
            modifier = Modifier.fillMaxWidth(),
        ) { Text(stringResource(R.string.scan_qr)) }
        Text(stringResource(R.string.or_paste), style = MaterialTheme.typography.bodyMedium)
        OutlinedTextField(
            value = pasted,
            onValueChange = { pasted = it },
            label = { Text("agentmux://hub?url=…") },
            modifier = Modifier.fillMaxWidth(),
        )
        OutlinedButton(onClick = { onLink(pasted) }, enabled = pasted.isNotBlank()) {
            Text(stringResource(R.string.connect))
        }
    }
}

@Composable
private fun SignInScreen(
    screen: Screen.SignIn,
    busy: Boolean,
    onSignIn: (String, String) -> Unit,
    onForget: () -> Unit,
) {
    var login by rememberSaveable { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text(screen.link.url, style = MaterialTheme.typography.titleMedium)
        screen.link.fingerprint?.let {
            Text(
                stringResource(R.string.pinned_certificate, it.take(16)),
                style = MaterialTheme.typography.bodySmall,
            )
        }
        OutlinedTextField(
            value = login, onValueChange = { login = it },
            label = { Text(stringResource(R.string.login)) },
            singleLine = true, modifier = Modifier.fillMaxWidth(),
        )
        OutlinedTextField(
            value = password, onValueChange = { password = it },
            label = { Text(stringResource(R.string.password)) },
            singleLine = true,
            visualTransformation = PasswordVisualTransformation(),
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
            modifier = Modifier.fillMaxWidth(),
        )
        Button(
            onClick = { onSignIn(login, password) },
            enabled = !busy && login.isNotBlank() && password.isNotEmpty(),
            modifier = Modifier.fillMaxWidth(),
        ) { Text(stringResource(R.string.sign_in)) }
        TextButton(onClick = onForget) { Text(stringResource(R.string.use_another_hub)) }
    }
}

@Composable
private fun HomeScreen(screen: Screen.Home, onRefresh: () -> Unit, onSignOut: () -> Unit) {
    Column(Modifier.fillMaxSize()) {
        Row(
            Modifier.fillMaxWidth().padding(16.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            Column(Modifier.weight(1f)) {
                Text(screen.account.shownName, style = MaterialTheme.typography.titleMedium)
                Text(screen.link.url, style = MaterialTheme.typography.bodySmall)
            }
            TextButton(onClick = onRefresh) { Text(stringResource(R.string.refresh)) }
            TextButton(onClick = onSignOut) { Text(stringResource(R.string.sign_out)) }
        }
        HorizontalDivider()
        val mailbox = screen.mailbox
        when {
            mailbox == null -> Unit
            mailbox.conversations.isEmpty() ->
                Text(stringResource(R.string.no_messages), Modifier.padding(16.dp))
            else -> LazyColumn {
                items(mailbox.conversations, key = { it.peer.address }) { conversation ->
                    ConversationRow(conversation)
                    HorizontalDivider()
                }
            }
        }
    }
}

@Composable
private fun ConversationRow(conversation: MailConversation) {
    Row(Modifier.fillMaxWidth().padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            val machine = conversation.peer.machine?.takeIf { it.isNotBlank() }?.let { " · $it" }.orEmpty()
            Text(conversation.peer.shownName + machine, style = MaterialTheme.typography.titleSmall)
            Text(
                conversation.last.text,
                style = MaterialTheme.typography.bodyMedium,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
            )
        }
        if (conversation.unread > 0) {
            Badge { Text(conversation.unread.toString()) }
        }
    }
}
