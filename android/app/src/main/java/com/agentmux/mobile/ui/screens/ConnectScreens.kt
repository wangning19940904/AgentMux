package com.agentmux.mobile.ui.screens

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.agentmux.mobile.R
import com.agentmux.mobile.ui.Phase
import com.agentmux.mobile.ui.components.Group
import com.agentmux.mobile.ui.components.InfoRow
import com.agentmux.mobile.ui.theme.AppIcons
import com.agentmux.mobile.ui.theme.AppTheme
import com.agentmux.mobile.ui.theme.Space
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions

/** The AgentMux mark, as the desktop app and the launcher show it. */
@Composable
private fun AppMark() {
    Image(
        painterResource(R.drawable.agentmux_mark),
        contentDescription = null,
        modifier = Modifier.size(76.dp).clip(RoundedCornerShape(20.dp)),
    )
}

@Composable
fun ConnectScreen(onLink: (String) -> Unit) {
    val palette = AppTheme.palette
    val scanner = rememberLauncherForActivityResult(ScanContract()) { result -> result.contents?.let(onLink) }
    val scanPrompt = stringResource(R.string.scan_prompt)
    var pasted by rememberSaveable { mutableStateOf("") }
    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).imePadding().padding(Space.xl),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Spacer(Modifier.height(40.dp))
        AppMark()
        Spacer(Modifier.height(Space.l))
        Text(stringResource(R.string.connect_title), style = AppTheme.type.largeTitle, color = palette.ink, textAlign = TextAlign.Center)
        Spacer(Modifier.height(Space.s))
        Text(stringResource(R.string.connect_intro), style = AppTheme.type.callout, color = palette.ink2, textAlign = TextAlign.Center)
        Spacer(Modifier.height(Space.xl))
        Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Space.m)) {
            listOf(R.string.connect_step_1, R.string.connect_step_2, R.string.connect_step_3).forEachIndexed { index, step ->
                Row(verticalAlignment = Alignment.Top) {
                    Box(
                        Modifier.size(24.dp).clip(CircleShape).background(palette.accentSoft),
                        contentAlignment = Alignment.Center,
                    ) { Text("${index + 1}", style = AppTheme.type.micro, color = palette.accent) }
                    Spacer(Modifier.width(Space.m))
                    Text(stringResource(step), style = AppTheme.type.callout, color = palette.ink)
                }
            }
        }
        Spacer(Modifier.height(Space.xl))
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
            modifier = Modifier.fillMaxWidth().height(52.dp),
            shape = RoundedCornerShape(14.dp),
        ) {
            Icon(AppIcons.Scan, contentDescription = null, modifier = Modifier.size(20.dp))
            Spacer(Modifier.width(Space.s))
            Text(stringResource(R.string.scan_qr), style = AppTheme.type.bodyStrong)
        }
        Spacer(Modifier.height(Space.xl))
        Text(stringResource(R.string.or_paste), style = AppTheme.type.caption, color = palette.ink2, modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(Space.s))
        OutlinedTextField(
            value = pasted,
            onValueChange = { pasted = it },
            placeholder = { Text("agentmux://hub?url=…") },
            singleLine = true,
            shape = RoundedCornerShape(12.dp),
            modifier = Modifier.fillMaxWidth(),
        )
        Spacer(Modifier.height(Space.s))
        OutlinedButton(
            onClick = { onLink(pasted) },
            enabled = pasted.isNotBlank(),
            modifier = Modifier.fillMaxWidth(),
            shape = RoundedCornerShape(12.dp),
        ) { Text(stringResource(R.string.connect)) }
    }
}

@Composable
fun SignInScreen(
    phase: Phase.SignIn,
    busy: Boolean,
    onSignIn: (String, String) -> Unit,
    onForget: () -> Unit,
) {
    val palette = AppTheme.palette
    var login by rememberSaveable { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    val ready = !busy && login.isNotBlank() && password.isNotEmpty()
    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).imePadding().padding(vertical = Space.xl),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Spacer(Modifier.height(24.dp))
        AppMark()
        Spacer(Modifier.height(Space.l))
        Text(stringResource(R.string.sign_in_title), style = AppTheme.type.largeTitle, color = palette.ink)
        Spacer(Modifier.height(Space.xs))
        Text(stringResource(R.string.sign_in_intro), style = AppTheme.type.callout, color = palette.ink2,
            textAlign = TextAlign.Center, modifier = Modifier.padding(horizontal = Space.xl))
        Spacer(Modifier.height(Space.xl))
        Group {
            InfoRow(
                title = phase.link.url,
                subtitle = phase.link.fingerprint?.let { stringResource(R.string.pinned_certificate, it.take(16)) }
                    ?: stringResource(R.string.public_certificate),
                icon = if (phase.link.fingerprint != null) AppIcons.Shield else Icons.Filled.Lock,
                iconTint = palette.positive,
            )
        }
        Spacer(Modifier.height(Space.l))
        Column(Modifier.padding(horizontal = Space.l), verticalArrangement = Arrangement.spacedBy(Space.m)) {
            OutlinedTextField(
                value = login, onValueChange = { login = it },
                label = { Text(stringResource(R.string.login)) },
                singleLine = true, shape = RoundedCornerShape(12.dp),
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Next),
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = password, onValueChange = { password = it },
                label = { Text(stringResource(R.string.password)) },
                singleLine = true, shape = RoundedCornerShape(12.dp),
                visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, imeAction = ImeAction.Go),
                keyboardActions = KeyboardActions(onGo = { if (ready) onSignIn(login, password) }),
                modifier = Modifier.fillMaxWidth(),
            )
            Button(
                onClick = { onSignIn(login, password) },
                enabled = ready,
                modifier = Modifier.fillMaxWidth().height(52.dp),
                shape = RoundedCornerShape(14.dp),
                colors = ButtonDefaults.buttonColors(),
            ) { Text(if (busy) stringResource(R.string.signing_in) else stringResource(R.string.sign_in), style = AppTheme.type.bodyStrong) }
        }
        Spacer(Modifier.height(Space.s))
        TextButton(onClick = onForget) { Text(stringResource(R.string.use_another_hub)) }
    }
}
