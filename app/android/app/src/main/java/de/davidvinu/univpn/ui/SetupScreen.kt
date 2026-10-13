package de.davidvinu.univpn.ui

import android.content.Intent
import androidx.activity.compose.BackHandler
import androidx.core.net.toUri
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.colorResource
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.outlined.QrCodeScanner
import androidx.compose.material.icons.outlined.Visibility
import androidx.compose.material.icons.outlined.VisibilityOff
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ExposedDropdownMenuAnchorType
import androidx.compose.material3.ExposedDropdownMenuBox
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import de.davidvinu.univpn.AppModel
import de.davidvinu.univpn.MainActivity
import de.davidvinu.univpn.R
import de.davidvinu.univpn.Scanner
import de.davidvinu.univpn.Strings
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import org.json.JSONObject
import java.text.Normalizer
import java.util.UUID

private const val WELCOME = -1
private const val CODE = -2
private const val UNIVERSITY = 0
private const val SIGN_IN = 1
private const val SECOND_FACTOR = 2
private const val CONNECTING = 3
private const val DONE = 4

private val TOTP_MODES = setOf("totp_field", "totp_append")
private val USER_RE = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,63}(@[A-Za-z0-9][A-Za-z0-9.-]{0,252})?$")
private val PROFILE_FIELDS = setOf("university", "host", "usergroup", "authgroup", "mfa")

/** One university of /universities.json. */
private class University(val json: JSONObject) {
    val id: String = json.optString("id")
    val name: String = json.optString("name")
    val country: String = json.optString("country")
    val mfa: String = json.optString("mfa")
    val portal: String = json.optString("mfa_portal_url")
    /** The core can add itself in the MFA portal with one code from the user's app (enroll.go). */
    val enroll: Boolean = json.optString("mfa_enroll") == "linotp" && portal.isNotEmpty()
    val steps: List<String> = json.optJSONArray("mfa_steps")?.let { a -> (0 until a.length()).map { a.getString(it) } } ?: emptyList()
    val search: List<String> = listOf(id, name, json.optString("host")) +
        (json.optJSONArray("aliases")?.let { a -> (0 until a.length()).map { a.getString(it) } } ?: emptyList())
}

/** Like the status page's search: no accents, umlaut spellings folded. */
private fun fold(s: String) = Normalizer.normalize(s, Normalizer.Form.NFKD).replace(Regex("\\p{Mn}+"), "").lowercase()
    .replace("ue", "u").replace("oe", "o").replace("ae", "a")

/**
 * The setup assistant of the desktop app, on a phone. Role models: WhatsApp and Signal (a phone
 * joins by scanning the QR code another device shows), eduroam CAT (find your institution),
 * Google Authenticator (scan the setup key), Tailscale (the system's VPN prompt at the end).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SetupFlow(model: AppModel, activity: MainActivity) {
    val status = model.status ?: return
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val syncAvailable = status.optJSONObject("sync")?.optBoolean("available") == true
    var step by rememberSaveable {
        mutableIntStateOf(if (model.setupFinishing) DONE else if (model.changingAccount || !syncAvailable) UNIVERSITY else WELCOME)
    }
    var universities by remember { mutableStateOf<List<University>>(emptyList()) }
    var uniId by rememberSaveable { mutableStateOf(if (model.changingAccount) status.optString("university") else "") }
    var query by rememberSaveable { mutableStateOf("") }
    // Not listed: its gateway settings as they are (as on the desktop), or changing only the
    // username would ask for the VPN address again.
    val current = status.takeIf { model.changingAccount && it.optString("university") == "other" }
    var host by rememberSaveable {
        mutableStateOf(current?.let { it.optString("host") + it.optString("usergroup").let { g -> if (g.isEmpty()) "" else "/$g" } } ?: "")
    }
    var groups by rememberSaveable { mutableStateOf(listOfNotNull(current?.optString("authgroup")?.ifEmpty { null })) }
    var group by rememberSaveable { mutableStateOf(groups.firstOrNull() ?: "") }
    // The account's own group: shown and kept, also when it is the only one.
    var ownGroup by rememberSaveable { mutableStateOf(groups.isNotEmpty()) }
    var otherMfa by rememberSaveable { mutableStateOf(current?.optString("mfa")?.ifEmpty { "none" } ?: "") }
    var detecting by remember { mutableStateOf(false) }
    // The gateway check whose answer counts (index.html detectSeq): one for an address, group or
    // university chosen before counts for nothing.
    var detectSeq by remember { mutableIntStateOf(0) }
    var user by rememberSaveable { mutableStateOf(if (model.changingAccount) status.optString("user") else "") }
    var password by rememberSaveable { mutableStateOf("") }
    var secret by rememberSaveable { mutableStateOf("") }
    var secretValid by remember { mutableStateOf(false) }
    // "Set it up by hand instead" on the page for one code.
    var byHand by rememberSaveable { mutableStateOf(false) }
    var pairCode by rememberSaveable { mutableStateOf("") }
    // Whose answer model.pairResult is (it survives turning the phone, like the page).
    val pairOwner = rememberSaveable { UUID.randomUUID().toString() }
    var error by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(Unit) {
        // Again until it arrives: without it every university would look "Not listed" (the core
        // was away at the first try, restarting after Repair for example).
        while (universities.isEmpty()) {
            val list = model.get("/universities.json")?.optJSONArray("universities")
            if (list != null) universities = (0 until list.length()).map { University(list.getJSONObject(it)) }
            if (universities.isEmpty()) delay(1000)
        }
    }
    val uni = universities.firstOrNull { it.id == uniId }
    val isOther = uniId == "other"
    val mfa = if (isOther) otherMfa else uni?.mfa ?: "totp_field"
    val needsTotp = mfa in TOTP_MODES
    val total = if (needsTotp) 4 else 3
    val auto = needsTotp && uni?.enroll == true

    fun go(next: Int) {
        error = null
        step = next
    }

    // Another address or university, or back to the start: a check still on its way is not
    // for it.
    fun dropDetect() {
        detectSeq++
        detecting = false
    }

    // Asks the gateway of the address what it needs, like the page's detect().
    fun detectGateway(picked: String) {
        val seq = ++detectSeq
        val address = host
        detecting = true
        error = null
        scope.launch {
            val found = detect(model, address, picked)
            if (seq != detectSeq) return@launch
            detecting = false
            error = found.error
            found.groups?.let { group = found.group; groups = it; ownGroup = false }
            // Like the desktop page: whatever the check found, the choice shows, so a gateway
            // that does not answer can still be set up. Not after SAML: Next asks again.
            otherMfa = found.mfa ?: otherMfa.ifEmpty { "totp_field" }
        }
    }

    fun finishChange() {
        model.changingAccount = false
        model.setupFinishing = false
        model.home()
    }

    // The account for /api/setup, without the second factor.
    fun account(): JSONObject {
        val profile = JSONObject()
        if (isOther) {
            val parts = host.trim().replace(Regex("^https?://", RegexOption.IGNORE_CASE), "").split("/", limit = 2)
            profile.put("host", parts[0]).put("mfa", otherMfa)
            if (parts.size > 1 && parts[1].isNotEmpty()) profile.put("usergroup", parts[1])
            if ((groups.size > 1 || ownGroup) && group.isNotEmpty()) profile.put("authgroup", group)
        }
        return JSONObject().put("user", user.trim()).put("password", password).put("university", uniId).put("profile", profile)
    }

    fun submit() {
        if (model.setupBusy) return
        model.setupFinishing = true
        go(CONNECTING)
        model.sendSetup(account().put("secret", if (needsTotp) secret.trim() else ""))
    }

    // The first code from the user's app goes with the account, also when changing it.
    fun submitCode(code: String) {
        if (model.setupBusy) return
        model.setupFinishing = true
        error = null
        model.enrollError = null
        model.sendSetup(account().put("code", code), withCode = true)
    }

    // The answer lands in the model, so turning the phone meanwhile loses nothing.
    LaunchedEffect(model.setupFailure) {
        val result = model.setupFailure ?: return@LaunchedEffect
        model.setupFailure = null
        // An account sent before already set the phone up: the page stays with the error,
        // not Home with the old account.
        if (!model.changingAccount && model.setupNeeded) model.setupFinishing = false
        val field = result.json?.optString("field")
        step = when {
            field in PROFILE_FIELDS -> UNIVERSITY
            field == "totp" && needsTotp -> SECOND_FACTOR
            else -> SIGN_IN
        }
        error = result.error
    }

    // Another device's code, the same way. Linked, but some settings could not be used here:
    // the answer says so, and if the account was one of them this phone is set up by hand (as
    // on the desktop).
    LaunchedEffect(model.pairResult) {
        val result = model.pairResult?.takeIf { model.pairResultFor == pairOwner } ?: return@LaunchedEffect
        model.pairResult = null
        if (!result.ok && !model.pairLinked) {
            error = result.error
            return@LaunchedEffect
        }
        // Linked without an answer: what went wrong is the note, none when the core did not
        // answer at all ("not running" would be wrong, it took the account).
        val note = if (!result.ok) {
            result.error.takeIf { result.reply.status != 0 }
        } else {
            result.json?.let { model.strings.message(it.optString("error"), it.optJSONObject("error_t")) }?.ifEmpty { null }
        }
        go(if (model.setupNeeded) UNIVERSITY else DONE)
        error = note
    }

    // While connecting: the first sign-in tells whether everything was right (setupProgress).
    if (step == CONNECTING && model.setupSentAt > 0) {
        val state = status.optString("state")
        val settled = System.currentTimeMillis() - model.setupSentAt > 1500
        LaunchedEffect(state, settled, status.optBoolean("busy")) {
            when {
                state == "connected" -> {
                    model.setupSentAt = 0
                    if (model.changingAccount) finishChange() else go(DONE)
                }
                state in setOf("auth_failed", "keyring") && settled -> {
                    model.setupSentAt = 0
                    val totp = status.optString("error_kind") == "totp"
                    step = if (totp && needsTotp) SECOND_FACTOR else SIGN_IN
                    error = model.strings.message(status.optString("message"), status.optJSONObject("message_t"))
                }
                state == "idle" && !status.optBoolean("busy") && settled -> {
                    model.setupSentAt = System.currentTimeMillis()
                    model.post("/api/connect")
                }
            }
        }
    }

    Column(
        Modifier.fillMaxSize().navigationBarsPadding().imePadding().verticalScroll(rememberScrollState()),
    ) {
        val back: (() -> Unit)? = when (step) {
            SIGN_IN -> { { go(UNIVERSITY) } }
            SECOND_FACTOR -> when {
                auto && byHand -> { { byHand = false; go(SECOND_FACTOR) } }
                auto && model.setupBusy -> null
                auto -> { { model.cancelEnroll(); go(SIGN_IN) } }
                else -> { { go(SIGN_IN) } }
            }
            CODE -> { { go(WELCOME) } }
            UNIVERSITY -> if (model.changingAccount) { { finishChange() } } else if (syncAvailable) { { dropDetect(); go(WELCOME) } } else null
            CONNECTING -> if (!model.setupBusy) { { model.setupSentAt = 0; go(if (needsTotp) SECOND_FACTOR else SIGN_IN) } } else null
            else -> null
        }
        // Android's back button goes where the arrow goes, not out of the app.
        BackHandler(enabled = back != null) { back?.invoke() }
        if (step != WELCOME && step != DONE) {
            // Without a second factor its step is skipped: connecting is the last one.
            val n = if (step == CONNECTING && !needsTotp) total else step + 1
            Bar(model, if (step >= 0 && step < DONE) model.t("setup.step", "n" to n, "total" to total) else "", onBack = back)
        }
        when (step) {
            WELCOME -> Column(
                Modifier.fillMaxWidth().padding(24.dp).padding(top = 96.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                Image(
                    painterResource(R.drawable.ic_launcher_foreground),
                    contentDescription = null,
                    modifier = Modifier.size(112.dp).clip(RoundedCornerShape(28.dp)).background(colorResource(R.color.icon_background)),
                )
                Text("Uni VPN", style = MaterialTheme.typography.headlineMedium)
                Spacer(Modifier.height(48.dp))
                Button(onClick = {
                    Scanner.scanQr(context, { error = model.t("phone.scan_failed") }) { value ->
                        val code = value?.removePrefix("UNIVPN:")
                        if (code != null && value.startsWith("UNIVPN:")) {
                            pairCode = code
                            go(CODE)
                            // Scanned codes go on by themselves.
                            model.enterPairCode(code, pairOwner)
                        }
                    }
                }, modifier = Modifier.fillMaxWidth()) {
                    Icon(Icons.Outlined.QrCodeScanner, contentDescription = null)
                    Text("  " + model.t("phone.scan_code"))
                }
                ErrorText(error)
                Info(model, model.t("phone.scan_code_help"))
                TextButton(onClick = { go(CODE) }) { Text(model.t("sync.enter")) }
                TextButton(onClick = { go(UNIVERSITY) }) { Text(model.t("phone.setup_here")) }
            }

            CODE -> Page {
                Text(model.t("sync.use_other"), style = MaterialTheme.typography.headlineSmall)
                OutlinedTextField(
                    value = pairCode,
                    onValueChange = { pairCode = it.uppercase() },
                    label = { Text(model.t("sync.code")) },
                    placeholder = { Text("XXXX-XXXX-XXXX") },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Characters, autoCorrectEnabled = false),
                    modifier = Modifier.fillMaxWidth(),
                )
                ErrorText(error)
                if (model.pairBusy) CircularProgressIndicator()
                Actions {
                    Button(enabled = !model.pairBusy && pairCode.isNotBlank(), onClick = {
                        error = null
                        model.enterPairCode(pairCode, pairOwner)
                    }) { Text(model.t("app.next")) }
                }
            }

            // Changing the account waits for the list: without it the account's university would
            // look "Not listed".
            UNIVERSITY -> if (model.changingAccount && universities.isEmpty()) Page {
                CircularProgressIndicator()
            } else Page {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(model.t("app.university"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.weight(1f))
                    Info(model, model.t("setup.find_help"))
                }
                OutlinedTextField(
                    value = if (uni != null && query.isEmpty()) uni.name else if (isOther && query.isEmpty()) model.t("setup.not_listed") else query,
                    onValueChange = { query = it; uniId = ""; dropDetect() },
                    label = { Text(model.t("setup.find")) },
                    placeholder = { Text(model.t("setup.find_placeholder")) },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                if (uniId.isEmpty()) {
                    val q = fold(query.trim())
                    val found = universities.filter { u -> q.isEmpty() || u.search.any { fold(it).contains(q) } }.take(8)
                    Column {
                        for (u in found) {
                            ListItem(
                                headlineContent = { Text(u.name) },
                                trailingContent = { Text(u.country, color = MaterialTheme.colorScheme.onSurfaceVariant) },
                                modifier = Modifier.clickable { uniId = u.id; query = ""; dropDetect(); error = if (u.mfa == "saml") model.t("setup.saml") else null },
                            )
                        }
                        ListItem(
                            headlineContent = { Text(model.t("setup.not_listed"), color = MaterialTheme.colorScheme.primary) },
                            modifier = Modifier.clickable { uniId = "other"; query = ""; dropDetect(); error = null },
                        )
                    }
                }
                if (isOther) {
                    OutlinedTextField(
                        value = host,
                        onValueChange = { host = it; otherMfa = ""; groups = emptyList(); ownGroup = false; dropDetect() },
                        label = { Text(model.t("setup.vpn_address")) },
                        placeholder = { Text("vpn.example.edu") },
                        singleLine = true,
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, autoCorrectEnabled = false),
                        modifier = Modifier.fillMaxWidth(),
                    )
                    if (groups.size > 1 || ownGroup) {
                        Choice(model.t("setup.group"), groups.map { it to it }, group) { picked ->
                            group = picked
                            detectGateway(picked)
                        }
                    }
                    if (otherMfa.isNotEmpty()) {
                        Choice(
                            model.t("app.second_factor"),
                            listOf("none", "totp_field", "totp_append", "duo_push").map { it to model.t("setup.mfa.$it") },
                            otherMfa,
                        ) { otherMfa = it }
                    }
                }
                ErrorText(error)
                Actions {
                    if (detecting) CircularProgressIndicator()
                    Button(enabled = !detecting, onClick = {
                        when {
                            uniId.isEmpty() -> error = model.t("setup.choose")
                            uni?.mfa == "saml" -> error = model.t("setup.saml")
                            isOther && host.isBlank() -> error = model.t("setup.enter_address")
                            isOther && otherMfa.isEmpty() -> detectGateway("")
                            else -> go(SIGN_IN)
                        }
                    }) { Text(model.t("app.next")) }
                }
            }

            SIGN_IN -> Page {
                Text(model.t("setup.sign_in"), style = MaterialTheme.typography.headlineSmall)
                OutlinedTextField(
                    value = user,
                    onValueChange = { user = it },
                    label = { Text(model.t("app.username")) },
                    placeholder = { Text("ab123") },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Email, autoCorrectEnabled = false),
                    modifier = Modifier.fillMaxWidth(),
                )
                PasswordField(model, password) { password = it }
                ErrorText(error)
                Actions {
                    Button(onClick = {
                        when {
                            !USER_RE.matches(user.trim()) -> error = model.t("setup.enter_user")
                            password.isEmpty() -> error = model.t("setup.enter_password")
                            needsTotp -> {
                                byHand = false
                                go(SECOND_FACTOR)
                            }
                            else -> submit()
                        }
                    }) { Text(model.t("app.next")) }
                }
            }

            SECOND_FACTOR -> if (auto && !byHand) Page {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(model.t("app.second_factor"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.weight(1f))
                    Info(model, model.t("setup.auto_help"))
                }
                AutoSecondFactor(
                    model,
                    error ?: model.enrollError,
                    onFirstCode = { submitCode(it) },
                    onByHand = {
                        model.cancelEnroll()
                        byHand = true
                        go(SECOND_FACTOR)
                    },
                    onDone = { if (model.changingAccount) finishChange() else go(DONE) },
                    onFailed = { text ->
                        // The VPN refused the password: back to it, as after any sign-in.
                        if (model.status?.optString("error_kind") == "password") {
                            go(SIGN_IN)
                            error = text
                        } else {
                            model.enrollError = text
                        }
                    },
                )
            } else Page {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(model.t("app.second_factor"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.weight(1f))
                    Info(model, mfaSteps(model, uni).joinToString("\n") + "\n\n" + model.t("setup.totp_help"))
                }
                if (uni?.portal?.isNotEmpty() == true) {
                    TextButton(onClick = { context.startActivity(Intent(Intent.ACTION_VIEW, uni.portal.toUri())) }) {
                        Text(model.t("setup.mfa_portal"))
                    }
                }
                TotpEntry(model, secret, { secret = it }, { secretValid = it }, error)
                Actions {
                    Button(enabled = secretValid, onClick = { submit() }) { Text(model.t("app.next")) }
                }
            }

            CONNECTING -> Column(
                Modifier.fillMaxWidth().padding(24.dp).padding(top = 64.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                CircularProgressIndicator()
                Text(model.t("look.connecting"), style = MaterialTheme.typography.headlineSmall)
                val state = status.optString("state")
                val stuck = state in setOf("offline", "blocked", "error")
                val text = if (stuck || status.optString("message_id") == "waiting_for_code") {
                    model.strings.message(status.optString("message"), status.optJSONObject("message_t"))
                } else {
                    model.t("setup.waiting")
                }
                Text(text, textAlign = TextAlign.Center, color = MaterialTheme.colorScheme.onSurfaceVariant)
                if (stuck && model.setupSentAt > 0) {
                    TextButton(onClick = { model.setupSentAt = 0; if (model.changingAccount) finishChange() else go(DONE) }) {
                        Text(model.t("setup.finish"))
                    }
                }
            }

            DONE -> Column(
                Modifier.fillMaxWidth().padding(24.dp).padding(top = 96.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                Icon(Icons.Filled.CheckCircle, contentDescription = null, tint = Ok, modifier = Modifier.size(72.dp))
                Text(model.t("setup.all_set"), style = MaterialTheme.typography.headlineMedium)
                val hasDomains = (status.optJSONArray("domains")?.length() ?: 0) > 0
                Text(
                    model.t(if (hasDomains) "setup.all_set_sub" else "setup.all_set_no_domains"),
                    textAlign = TextAlign.Center,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                // Settings another device has that could not be used here.
                ErrorText(error)
                Spacer(Modifier.height(24.dp))
                // Tailscale: the system's VPN prompt follows the sign-in.
                Button(onClick = {
                    activity.turnOn()
                    model.setupFinishing = false
                    model.changingAccount = false
                    model.home()
                }, modifier = Modifier.fillMaxWidth()) { Text(model.t("app.done")) }
            }
        }
    }
}

/**
 * What the gateway of an address that is not listed needs (/api/detect): [groups] is null when
 * it did not say (then [error] says why), [mfa] the second factor to suggest, "" when the group
 * needs a web page (SAML), null without an answer.
 */
internal class Detected(val error: String?, val group: String = "", val groups: List<String>? = null, val mfa: String? = null)

private suspend fun detect(model: AppModel, host: String, group: String): Detected {
    val result = model.post("/api/detect", JSONObject().put("host", host.trim()).put("group", group), timeoutMs = 30_000)
    val d = result.json
    if (!result.ok || d == null) return Detected(result.error)
    return detected(d, model.strings)
}

/** The answer of /api/detect, read like the page's detect(). */
internal fun detected(d: JSONObject, strings: Strings): Detected {
    // The core always sends "error", empty when the gateway answered (as the page reads it).
    val problem = if (d.isNull("error")) "" else d.optString("error")
    if (problem.isNotEmpty()) return Detected(strings.message(problem, d.optJSONObject("error_t")))
    val groups = d.optJSONArray("groups")?.let { a -> (0 until a.length()).map { a.getString(it) } } ?: emptyList()
    // No way on with this group: its groups show all the same, another one may do without a
    // web page.
    if (d.optBoolean("saml")) return Detected(strings.t("setup.saml"), d.optString("group"), groups, "")
    return Detected(null, d.optString("group"), groups, d.optJSONObject("suggestion")?.optString("mfa")?.takeIf { it.isNotEmpty() } ?: "totp_field")
}

/** A university's own steps, translated where the catalog has them, else the general ones. */
private fun mfaSteps(model: AppModel, uni: University?): List<String> {
    val portal = model.t("setup.mfa_portal")
    if (uni == null || uni.steps.isEmpty()) return (1..3).map { "$it. " + model.t("setup.generic_step.$it", "portal" to portal) }
    return uni.steps.mapIndexed { i, text ->
        val key = "uni.${uni.id}.mfa.${i + 1}"
        val translated = model.t(key).takeIf { it != key } ?: text
        "${i + 1}. " + translated.replace("{portal}", portal)
    }
}

@Composable
fun PasswordField(model: AppModel, value: String, onChange: (String) -> Unit) {
    var shown by rememberSaveable { mutableStateOf(false) }
    OutlinedTextField(
        value = value,
        onValueChange = onChange,
        label = { Text(model.t("app.password")) },
        singleLine = true,
        visualTransformation = if (shown) VisualTransformation.None else PasswordVisualTransformation(),
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
        trailingIcon = {
            IconButton(onClick = { shown = !shown }) {
                Icon(if (shown) Icons.Outlined.VisibilityOff else Icons.Outlined.Visibility, contentDescription = model.t("app.show_password"))
            }
        },
        modifier = Modifier.fillMaxWidth(),
    )
}

/** A drop-down field (Material's exposed drop-down menu). */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun Choice(label: String, options: List<Pair<String, String>>, selected: String, onPick: (String) -> Unit) {
    var open by remember { mutableStateOf(false) }
    ExposedDropdownMenuBox(expanded = open, onExpandedChange = { open = it }) {
        OutlinedTextField(
            value = options.firstOrNull { it.first == selected }?.second ?: "",
            onValueChange = {},
            readOnly = true,
            label = { Text(label) },
            modifier = Modifier.fillMaxWidth().menuAnchor(ExposedDropdownMenuAnchorType.PrimaryNotEditable),
        )
        ExposedDropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            for ((value, text) in options) {
                DropdownMenuItem(text = { Text(text) }, onClick = { open = false; onPick(value) })
            }
        }
    }
}
