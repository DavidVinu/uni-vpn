package de.davidvinu.univpn.ui

import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext

val Brand = Color(0xFF0B57D0)
val Ok = Color(0xFF1E8E3E)
val Warn = Color(0xFFB06000)

/** The phone's own colours where it has them (Material You), else the app icon's blue. */
@Composable
fun UniVpnTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    val context = LocalContext.current
    val scheme = when {
        Build.VERSION.SDK_INT >= 31 -> if (dark) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
        dark -> darkColorScheme(primary = Color(0xFFA8C7FA))
        else -> lightColorScheme(primary = Brand)
    }
    MaterialTheme(colorScheme = scheme, content = content)
}
