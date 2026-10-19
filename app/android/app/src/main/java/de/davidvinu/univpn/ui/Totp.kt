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
import de.davidvinu.univpn.AppModel
import de.davidvinu.univpn.Scanner
import kotlinx.coroutines.delay
import org.json.JSONObject

/**
 * The setup key of the second factor, role model Google Authenticator: scan the QR code of the
 * university's security page, or type the key. A valid key shows the current test code with the
 * time it has left, for the security page's "test" step.
 */
@Composable
fun TotpEntry(model: AppModel, secret: String, onSecret: (String) -> Unit, onValid: (Boolean) -> Unit, error: String?) {
    val context = androidx.compose.ui.platform.LocalContext.current
    var shown by rememberSaveable { mutableStateOf(false) }
    var code by remember { mutableStateOf<String?>(null) }
    var left by remember { mutableIntStateOf(0) }
    var checkError by remember { mutableStateOf<String?>(null) }
    var checks by remember { mutableIntStateOf(0) }
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
    LaunchedEffect(secret, checks) {
        code = null
        onValid(false)
        checkError = null
        val trimmed = secret.trim()
        if (trimmed.isEmpty()) return@LaunchedEffect
        delay(300)
        val result = model.post("/api/totp/check", JSONObject().put("secret", trimmed))
        if (!result.ok) {
            checkError = result.error
            return@LaunchedEffect
        }
        onValid(true)
        code = result.json?.optString("code")
        left = result.json?.optInt("remaining", 30) ?: 30
        while (left > 0) {
            delay(1000)
            left--
        }
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
