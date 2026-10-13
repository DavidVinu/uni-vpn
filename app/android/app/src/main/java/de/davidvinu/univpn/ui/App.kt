package de.davidvinu.univpn.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import de.davidvinu.univpn.AppModel
import de.davidvinu.univpn.MainActivity
import de.davidvinu.univpn.R
import de.davidvinu.univpn.Screen

@Composable
fun App(model: AppModel, vpnOn: Boolean, activity: MainActivity) {
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
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
                        Switch(checked = vpnOn, onCheckedChange = { on -> if (on) activity.turnOn() else activity.turnOff() })
                    }
                }
            }
            model.status == null -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                CircularProgressIndicator()
            }
            model.setupNeeded || model.changingAccount || model.setupFinishing -> SetupFlow(model, activity)
            else -> when (val screen = model.screen) {
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
