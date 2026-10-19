package de.davidvinu.univpn.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.WindowInsetsSides
import androidx.compose.foundation.layout.displayCutout
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.only
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import de.davidvinu.univpn.AppModel
import de.davidvinu.univpn.MainActivity
import de.davidvinu.univpn.R
import de.davidvinu.univpn.Screen
import java.util.UUID

@Composable
fun App(model: AppModel, vpnOn: Boolean, activity: MainActivity) {
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
        // Turned sideways, the camera cutout sits at one side: nothing goes under it. The top
        // bars keep clear of it themselves and do not add it twice.
        val setupShown = model.setupNeeded || model.changingAccount || model.setupFinishing
        LaunchedEffect(model.visit, setupShown) { model.screenChanged() }
        Box(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.displayCutout.only(WindowInsetsSides.Horizontal)).doubleTapGuard(model)) {
            when {
                // Instead of a spinner that never ends. The core's texts need the core: this one
                // comes with the app. The switch stays, so Uni VPN can still be turned off here.
                model.portTaken -> Column(
                    Modifier.fillMaxSize().padding(24.dp),
                    verticalArrangement = Arrangement.spacedBy(24.dp, Alignment.CenterVertically),
                    horizontalAlignment = Alignment.CenterHorizontally,
                ) {
                    Text(stringResource(R.string.port_in_use), textAlign = TextAlign.Center)
                    if (vpnOn || model.core.setupDone) {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(16.dp)) {
                            Text(stringResource(R.string.app_name), style = MaterialTheme.typography.titleMedium)
                            Switch(checked = vpnOn, onCheckedChange = { on -> model.screenChanged(); if (on) activity.turnOn() else activity.turnOff() })
                        }
                    }
                }
                model.status == null -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    CircularProgressIndicator()
                }
                else -> Pages(
                    setup = model.setupNeeded || model.changingAccount || model.setupFinishing,
                    visit = model.visit,
                    setupFlow = { SetupFlow(model, activity) },
                ) {
                    when (val screen = model.screen) {
                        Screen.Home -> HomeScreen(model, vpnOn, activity)
                        Screen.Settings -> SettingsScreen(model)
                        Screen.Websites -> WebsitesScreen(model)
                        Screen.SecondFactor -> SecondFactorScreen(model, activity)
                        Screen.Language -> LanguageScreen(model)
                        Screen.Log -> LogScreen(model)
                        Screen.Licenses -> LicensesScreen(model)
                        is Screen.Pair -> PairScreen(model, screen.mode, activity)
                    }
                }
            }
        }
    }
    // Wherever the switch was: Home, or the page about the busy port. It needs the core's texts,
    // so it waits for them when the core has not answered yet.
    if (model.vpnRefused && model.strings.loaded) {
        DialogShown(model)
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

/**
 * The setup assistant, or the screen on top of the stack. What each saves for turning the phone
 * belongs to its visit: a screen's is its stack entry ([visit], AppModel.Visit).
 */
@Composable
internal fun Pages(setup: Boolean, visit: String, setupFlow: @Composable () -> Unit, screen: @Composable () -> Unit) {
    // The setup assistant's visit, like a screen's but saved: after Android ended the app during
    // setup, setup goes on where it was. Once something else shows, what it saved is not for the
    // next time (an account change from Settings starts at the university).
    var setupVisit by rememberSaveable { mutableStateOf(UUID.randomUUID().toString()) }
    if (setup) {
        key(setupVisit) { setupFlow() }
    } else {
        LaunchedEffect(Unit) { setupVisit = UUID.randomUUID().toString() }
        key(visit) { screen() }
    }
}

/** Drops a tap that starts within the double-tap time after the screen changed. */
internal fun Modifier.doubleTapGuard(model: AppModel): Modifier = pointerInput(Unit) {
    awaitPointerEventScope {
        while (true) {
            val event = awaitPointerEvent(PointerEventPass.Initial)
            if (model.justChanged) {
                event.changes.forEach { it.consume() }
            }
        }
    }
}
