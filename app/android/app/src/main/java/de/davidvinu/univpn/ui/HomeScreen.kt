package de.davidvinu.univpn.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.asPaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.outlined.Language
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import de.davidvinu.univpn.AppModel
import de.davidvinu.univpn.MainActivity
import de.davidvinu.univpn.Screen
import de.davidvinu.univpn.UniVpnService
import kotlinx.coroutines.launch

/** Message ids whose state name says it all (as on the status page). */
private val QUIET = setOf("not_connected", "disconnected", "connecting", "connected", "disconnecting", "setup_needed")
private val FIX = mapOf("password" to "main.update_password", "totp" to "main.update_totp", "repair" to "main.repair")

/**
 * Role model Tailscale's main screen: the app's name with settings in the top bar, one switch
 * with the state above everything, the list below (here: the websites that go through the
 * university, where Tailscale lists the devices).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun HomeScreen(model: AppModel, vpnOn: Boolean, activity: MainActivity) {
    val status = model.status ?: return
    val scope = rememberCoroutineScope()
    val lockdown by UniVpnService.lockdown.collectAsState()
    val state = status.optString("state")
    val domains = status.optJSONArray("domains")?.let { a -> (0 until a.length()).map { a.getString(it) } } ?: emptyList()
    Column(Modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("Uni VPN") },
            actions = {
                IconButton(onClick = { model.open(Screen.Settings) }) {
                    Icon(Icons.Outlined.Settings, contentDescription = model.t("settings.title"))
                }
            },
        )
        // The app draws behind Android's navigation bar: the last row scrolls above it.
        LazyColumn(Modifier.fillMaxSize(), contentPadding = WindowInsets.navigationBars.asPaddingValues()) {
            item {
                Card(
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp),
                    colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainerHigh),
                ) {
                    Row(Modifier.padding(20.dp), verticalAlignment = Alignment.CenterVertically) {
                        Box(Modifier.size(12.dp).background(if (vpnOn) stateColor(state) else Color.Gray, CircleShape))
                        Spacer(Modifier.width(16.dp))
                        Column(Modifier.weight(1f)) {
                            Text(
                                if (vpnOn) model.t("look.$state") else model.t("phone.off"),
                                style = MaterialTheme.typography.titleLarge,
                            )
                            val line = when {
                                !vpnOn -> ""
                                status.optString("message_id") !in QUIET ->
                                    model.strings.message(status.optString("message"), status.optJSONObject("message_t"))
                                state == "idle" -> model.t("main.hint.idle")
                                state == "connected" -> model.t("main.hint.connected")
                                else -> ""
                            }
                            if (line.isNotEmpty()) {
                                Text(line, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                        }
                        Spacer(Modifier.width(12.dp))
                        Switch(
                            checked = vpnOn,
                            onCheckedChange = { on -> if (on) activity.turnOn() else activity.turnOff() },
                            modifier = Modifier.semantics { contentDescription = "Uni VPN" },
                        )
                    }
                    val fix = FIX[status.optString("action")]
                    val stopped = vpnOn && state in setOf("auth_failed", "keyring", "error") && !status.optBoolean("busy")
                    if (vpnOn && (fix != null || stopped)) {
                        Row(
                            Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 12.dp),
                            horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End),
                        ) {
                            if (stopped) {
                                TextButton(onClick = { scope.launch { model.post("/api/connect") } }) { Text(model.t("main.try_again")) }
                            }
                            if (fix != null) {
                                Button(
                                    enabled = !status.optBoolean("repairing"),
                                    onClick = {
                                        when (status.optString("action")) {
                                            "password" -> { model.passwordDialog = true; model.open(Screen.Settings) }
                                            "totp" -> model.open(Screen.SecondFactor)
                                            else -> scope.launch {
                                                kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { model.core.restart() }
                                                model.refresh()
                                            }
                                        }
                                    },
                                ) { Text(model.t(fix)) }
                            }
                        }
                    }
                    if (vpnOn && lockdown) {
                        Column(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 12.dp)) {
                            Text(model.t("phone.lockdown"), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.error)
                            TextButton(onClick = { activity.openVpnSettings() }, modifier = Modifier.align(Alignment.End)) {
                                Text(model.t("phone.vpn_settings"))
                            }
                        }
                    }
                }
            }
            item { Section(model.t("settings.through")) }
            items(domains) { host ->
                ListItem(
                    headlineContent = { Text(host) },
                    leadingContent = { Icon(Icons.Outlined.Language, contentDescription = null) },
                )
            }
            item {
                ListItem(
                    headlineContent = { Text(model.t("phone.edit_websites"), color = MaterialTheme.colorScheme.primary) },
                    leadingContent = { Icon(Icons.Filled.Add, contentDescription = null, tint = MaterialTheme.colorScheme.primary) },
                    modifier = Modifier.clickable { model.open(Screen.Websites) },
                )
            }
        }
    }
    if (model.vpnRefused) {
        AlertDialog(
            onDismissRequest = { model.vpnRefused = false },
            text = { Text(model.t("phone.vpn_refused")) },
            dismissButton = { TextButton(onClick = { model.vpnRefused = false }) { Text(model.t("app.back")) } },
            confirmButton = {
                TextButton(onClick = {
                    model.vpnRefused = false
                    activity.openVpnSettings()
                }) { Text(model.t("phone.vpn_settings")) }
            },
        )
    }
}

@Composable
fun stateColor(state: String): Color = when (state) {
    "connected" -> Ok
    "connecting", "disconnecting", "offline", "blocked" -> Warn
    "auth_failed", "keyring", "error" -> MaterialTheme.colorScheme.error
    else -> Color.Gray
}
