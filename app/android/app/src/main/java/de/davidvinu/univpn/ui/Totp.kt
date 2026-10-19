package de.davidvinu.univpn.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.QrCodeScanner
import androidx.compose.material.icons.outlined.Visibility
import androidx.compose.material.icons.outlined.VisibilityOff
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import de.davidvinu.univpn.AppModel
import de.davidvinu.univpn.Scanner
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import org.json.JSONObject

/**
 * The setup key of the second factor, role model Google Authenticator: scan the QR code of the
 * university's security page, or type the key. A valid key shows the current test code with the
 * time it has left, for the security page's "test" step. [mfa] is the sign-in mode being set up:
 * a key whose codes it can't use is refused before the user registers it.
 */
@Composable
fun TotpEntry(model: AppModel, mfa: String, secret: String, onSecret: (String) -> Unit, onValid: (Boolean) -> Unit, error: String?) {
    val context = androidx.compose.ui.platform.LocalContext.current
    var shown by rememberSaveable { mutableStateOf(false) }
    var code by remember { mutableStateOf<String?>(null) }
    var left by remember { mutableIntStateOf(0) }
    var checkError by remember { mutableStateOf<String?>(null) }
    var checks by remember { mutableIntStateOf(0) }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    FilledTonalButton(onClick = {
        Scanner.scanQr(context, { checkError = model.t("phone.scan_failed") }) { value -> if (value != null) onSecret(value) }
    }, modifier = Modifier.fillMaxWidth()) {
        Icon(Icons.Outlined.QrCodeScanner, contentDescription = null)
        Text("  " + model.t("phone.scan_qr"))
    }
    OutlinedTextField(
        value = secret,
        onValueChange = onSecret,
        label = { Text(model.t("setup.token_secret")) },
        singleLine = true,
        visualTransformation = if (shown) VisualTransformation.None else PasswordVisualTransformation(),
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
        trailingIcon = {
            IconButton(onClick = { shown = !shown }) {
                Icon(if (shown) Icons.Outlined.VisibilityOff else Icons.Outlined.Visibility, contentDescription = model.t("setup.show_secret"))
            }
        },
        modifier = Modifier.fillMaxWidth(),
    )
    LaunchedEffect(secret, mfa, checks) {
        code = null
        onValid(false)
        checkError = null
        val trimmed = secret.trim()
        if (trimmed.isEmpty()) return@LaunchedEffect
        delay(300)
        val result = model.post("/api/totp/check", JSONObject().put("secret", trimmed).put("mfa", mfa))
        if (!result.ok) {
            checkError = result.error
            return@LaunchedEffect
        }
        onValid(true)
        code = result.json?.optString("code")
        // By the clock: a delay stops while the phone sleeps or the app is frozen in the
        // background, so counting delays would show an old code as current.
        val remaining = (result.json?.optInt("remaining", 30) ?: 30) * 1000L
        val end = System.currentTimeMillis() + remaining
        while (true) {
            // A clock set back meanwhile (corrected by hand) ends the code too.
            val rest = end - System.currentTimeMillis()
            left = if (rest > remaining) 0 else ((rest + 999) / 1000).coerceAtLeast(0).toInt()
            if (left == 0) break
            delay(1000)
        }
        // The next code only while the app is on screen, like the page: each check marks its
        // sign-in window as used (the next connect waits for a new one), and in the background
        // the VPN keeps the app running. Back on screen, a fresh code at once.
        lifecycle.currentStateFlow.first { it.isAtLeast(Lifecycle.State.STARTED) }
        checks++ // the next code
    }
    ErrorText(checkError ?: error)
    code?.let {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            CircularProgressIndicator(progress = { left / 30f }, modifier = Modifier.padding(4.dp))
            // In halves: 123 456, or 1234 5678 (Marburg).
            Text(it.take(it.length / 2) + " " + it.drop(it.length / 2), style = MaterialTheme.typography.headlineMedium)
            Text(model.t("setup.check_code"), color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
        }
    }
}
