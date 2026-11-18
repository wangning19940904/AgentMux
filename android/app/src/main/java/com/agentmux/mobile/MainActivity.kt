package com.agentmux.mobile

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.viewModels
import androidx.compose.runtime.getValue
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.agentmux.mobile.hub.HubLink
import com.agentmux.mobile.ui.AgentMuxApp
import com.agentmux.mobile.ui.AppViewModel
import com.agentmux.mobile.ui.theme.AgentMuxTheme

class MainActivity : ComponentActivity() {
    private val vm: AppViewModel by viewModels()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        if (savedInstanceState == null) handleLink(intent)
        setContent {
            val state by vm.state.collectAsStateWithLifecycle()
            AgentMuxTheme { AgentMuxApp(state, vm) }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleLink(intent)
    }

    /** A hub link opened from the camera app or a browser. */
    private fun handleLink(intent: Intent?) {
        val data = intent?.data ?: return
        if (data.scheme == HubLink.SCHEME) vm.useLink(data.toString())
    }
}
