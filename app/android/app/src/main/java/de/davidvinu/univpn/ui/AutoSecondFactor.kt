package de.davidvinu.univpn.ui

import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.KeyboardType
import de.davidvinu.univpn.AppModel

private val CODE_RE = Regex("^[0-9]{6,8}$")

/**
 * The second factor with one code, as on the desktop (index.html step2a): the user types the
 * code their authenticator app shows, the core signs in with it, adds itself as a code generator
 * in the MFA portal and may ask for the next code (status.json "enroll"). Role model: Microsoft
 * Authenticator's "sign in to add your work account". [onFirstCode] sends the first code; the
 * next ones go to /api/totp/auto.
 */
@Composable
fun AutoSecondFactor(
    model: AppModel,
    error: String?,
    onFirstCode: (String) -> Unit,
    onByHand: () -> Unit,
    onDone: () -> Unit,
    onFailed: (String) -> Unit,
) {
    val status = model.status ?: return
    val enroll = model.enroll
    val running = model.enrollSentAt > 0
    val settled = System.currentTimeMillis() - model.enrollSentAt > 1500
    // Until the status catches up with the code just sent, what that code started.
    val stage = when {
        !running -> ""
        !settled -> model.enrollPending
        else -> enroll?.optString("stage").orEmpty()
    }
    val busy = model.setupBusy || stage == "connecting" || stage == "working"
    val stuck = stage == "connecting" && status.optString("state") in setOf("offline", "blocked", "error")
    var code by rememberSaveable { mutableStateOf("") }
    var seen by rememberSaveable { mutableStateOf("") }
    var invalid by remember { mutableStateOf<String?>(null) }
    // The field and Next give way to the spinner: "Set it up by hand instead" moves up under the
    // finger (App.kt doubleTapGuard).
    LaunchedEffect(busy) { model.screenChanged() }

    if (running) {
        val now = enroll?.optString("stage")
        LaunchedEffect(now, settled) {
            if (!settled) return@LaunchedEffect
            when (now) {
                "done" -> {
                    model.enrollSentAt = 0
                    onDone()
                }
                null, "failed" -> {
                    model.enrollSentAt = 0
                    // No run at all: the core started again meanwhile, the code is used up.
                    onFailed(enroll?.optJSONObject("error_t")?.let { model.strings.t(it) } ?: model.t("enroll.failed"))
                }
            }
        }
    }
    // A code went out or a new one is asked for: an empty field for the next one.
    LaunchedEffect(stage) {
        if (stage == seen) return@LaunchedEffect
        if (stage in setOf("connecting", "code", "working")) code = ""
        seen = stage
    }

    fun next() {
        if (model.setupBusy) return
        val typed = code.replace(Regex("\\s"), "")
        if (!CODE_RE.matches(typed)) {
            invalid = model.t("enroll.enter_code")
            return
        }
        invalid = null
        // The answer to the portal's question, else the first code.
        if (running && enroll?.optString("stage") == "code") model.sendEnrollCode(typed) else onFirstCode(typed)
    }

    val text = when {
        stage == "code" -> enroll?.optJSONObject("error_t")?.let { model.strings.t(it) } ?: ""
        stuck -> model.strings.message(status.optString("message"), status.optJSONObject("message_t"))
        busy -> model.t(if (stage == "working") "setup.auto_working" else "setup.waiting")
        else -> model.t("setup.auto_sub")
    }
    // Read out when it changes, like the page's aria-live line: "type the next code" asks for
    // something. A new sentence is a new text: one changed in place is not read.
    key(text) { Text(text, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite }) }
    if (busy) {
        CircularProgressIndicator()
    } else {
        OutlinedTextField(
            value = code,
            onValueChange = { code = it },
            label = { Text(model.t("setup.code")) },
            placeholder = { Text("123456") },
            singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
            modifier = Modifier.fillMaxWidth(),
        )
    }
    ErrorText(invalid ?: error)
    if (!busy) {
        Actions {
            Button(onClick = { next() }) { Text(model.t("app.next")) }
        }
    }
    TextButton(enabled = !model.setupBusy, onClick = onByHand) { Text(model.t("setup.by_hand")) }
}
