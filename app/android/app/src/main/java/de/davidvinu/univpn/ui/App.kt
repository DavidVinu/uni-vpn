package de.davidvinu.univpn.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import de.davidvinu.univpn.AppModel
import de.davidvinu.univpn.MainActivity
import de.davidvinu.univpn.Screen

@Composable
fun App(model: AppModel, vpnOn: Boolean, activity: MainActivity) {
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
        when {
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
