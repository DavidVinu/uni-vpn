package de.davidvinu.univpn.ui

import android.content.ActivityNotFoundException
import android.content.Intent
import androidx.activity.compose.BackHandler
import androidx.core.net.toUri
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.asPaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.outlined.Close
import androidx.compose.material.icons.outlined.Language
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import de.davidvinu.univpn.AppModel
import de.davidvinu.univpn.MainActivity
import de.davidvinu.univpn.Scanner
import de.davidvinu.univpn.Screen
import de.davidvinu.univpn.UniVpnService
import kotlinx.coroutines.launch
import org.json.JSONObject

private val TOTP_MODES = setOf("totp_field", "totp_append")

/** Role model: Android's own Settings app and Tailscale's settings, a plain list in groups. */
@Composable
fun SettingsScreen(model: AppModel) {
    val status = model.status ?: return
    val scope = rememberCoroutineScope()
    val sync = status.optJSONObject("sync")
    // A Remove that did not go through, and the device it was for.
    var removeError by remember { mutableStateOf<String?>(null) }
    var removeFor by remember { mutableStateOf("") }
    LaunchedEffect(Unit) {
        // Like the desktop: opening Settings brings in changes from the other devices.
        if (sync?.optBoolean("on") == true) model.post("/api/sync/refresh", timeoutMs = 30_000)
    }
    Column(Modifier.fillMaxSize()) {
        Bar(model, model.t("settings.title"))
        LazyColumn(contentPadding = WindowInsets.navigationBars.asPaddingValues()) {
            item {
                Section(model.t("settings.account"))
                val change = { model.changingAccount = true }
                SettingRow(model.t("app.university"), status.optString("university_name"), change)
                SettingRow(model.t("app.username"), status.optString("user"), change)
                SettingRow(model.t("app.password"), "••••••••", { model.passwordDialog = true })
                if (status.optString("mfa") in TOTP_MODES) {
                    SettingRow(model.t("app.second_factor"), null, { model.open(Screen.SecondFactor) })
                }
                Section(model.t("settings.websites"))
                val count = status.optJSONArray("domains")?.length() ?: 0
                SettingRow(model.t("settings.through"), model.strings.plural("settings.domain_count", count), { model.open(Screen.Websites) })
            }
            if (sync?.optBoolean("available") == true) {
                val devices = sync.optJSONArray("devices")
                val rows = if (sync.optBoolean("on") && devices != null) (0 until devices.length()).map { devices.getJSONObject(it) } else emptyList()
                item {
                    Section(model.t("settings.devices"))
                    // Why the last sync or Remove did not go through, like Chrome's sync error under
                    // the account.
                    val problem = sync.optJSONObject("error")?.takeIf { sync.optBoolean("on") }?.let { model.strings.t(it) }
                    // The core goes on after the app stopped waiting (a slow network): once the
                    // device is gone, so is the error.
                    val failed = removeError?.takeIf { rows.any { it.optString("id") == removeFor } }
                    Column(Modifier.padding(horizontal = 16.dp)) { ErrorText(failed ?: problem) }
                }
                items(rows) { device ->
                    ListItem(
                        headlineContent = { Text(device.optString("name")) },
                        supportingContent = if (device.optBoolean("this")) { { Text(model.t("sync.this")) } } else null,
                        // On every row, this phone's too (turns syncing off here), as on the desktop.
                        trailingContent = {
                            TextButton(onClick = {
                                val id = device.optString("id")
                                scope.launch {
                                    val error = model.post("/api/sync/remove", JSONObject().put("device", id), 30_000).error
                                    removeFor = id
                                    removeError = error
                                }
                            }) { Text(model.t("sync.remove"), color = MaterialTheme.colorScheme.error) }
                        },
                    )
                }
                item {
                    ListItem(
                        headlineContent = { Text(model.t("sync.add"), color = MaterialTheme.colorScheme.primary) },
                        leadingContent = { Icon(Icons.Filled.Add, contentDescription = null, tint = MaterialTheme.colorScheme.primary) },
                        modifier = Modifier.clickable { model.open(Screen.Pair("offer")) },
                    )
                }
            }
            item {
                Section(model.t("settings.general"))
                val setting = status.optString("language")
                val name = model.strings.languages.firstOrNull { it.first == setting }?.second ?: model.t("settings.language_system")
                SettingRow(model.t("settings.language"), name, { model.open(Screen.Language) })
                Section(model.t("settings.about"))
                SettingRow(model.t("settings.log"), null, { model.open(Screen.Log) })
                SettingRow(model.t("phone.licenses"), null, { model.open(Screen.Licenses) })
                SettingRow(model.t("settings.version"), status.optString("version"))
            }
        }
    }
    if (model.passwordDialog) PasswordDialog(model)
}

@Composable
private fun PasswordDialog(model: AppModel) {
    val scope = rememberCoroutineScope()
    var password by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    AlertDialog(
        onDismissRequest = { model.passwordDialog = false },
        title = { Text(model.t("app.password")) },
        text = {
            Column {
                PasswordField(model, password) { password = it }
                ErrorText(error)
            }
        },
        dismissButton = { TextButton(onClick = { model.passwordDialog = false }) { Text(model.t("app.back")) } },
        confirmButton = {
            TextButton(enabled = password.isNotEmpty(), onClick = {
                scope.launch {
                    val result = model.post("/api/password", JSONObject().put("password", password))
                    if (result.ok) model.passwordDialog = false else error = result.error
                }
            }) { Text(model.t("app.save")) }
        },
    )
}

/** The websites that go through the university: a list to add to and remove from. */
@Composable
fun WebsitesScreen(model: AppModel) {
    val status = model.status ?: return
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val domains = status.optJSONArray("domains")?.let { a -> (0 until a.length()).map { a.getString(it) } } ?: emptyList()
    var adding by rememberSaveable { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    fun save(list: List<String>, then: () -> Unit = {}) {
        scope.launch {
            val result = model.post("/api/domains", JSONObject().put("text", list.joinToString("\n")))
            error = result.error
            if (result.ok) {
                UniVpnService.refresh(context)
                then()
            }
        }
    }
    // The field to add a website sits at the bottom: above Android's navigation bar or keyboard.
    Column(Modifier.fillMaxSize().navigationBarsPadding().imePadding()) {
        Bar(model, model.t("settings.through")) { Info(model, model.t("settings.domains_help")) }
        LazyColumn(Modifier.weight(1f)) {
            items(domains) { host ->
                ListItem(
                    headlineContent = { Text(host) },
                    leadingContent = { Icon(Icons.Outlined.Language, contentDescription = null) },
                    trailingContent = {
                        IconButton(onClick = { save(domains - host) }) {
                            Icon(Icons.Outlined.Close, contentDescription = model.t("sync.remove"))
                        }
                    },
                )
            }
        }
        Column(Modifier.padding(16.dp)) {
            ErrorText(error)
            Row(verticalAlignment = Alignment.CenterVertically) {
                val add = { if (adding.isNotBlank()) save(domains + adding.trim()) { adding = "" } }
                OutlinedTextField(
                    value = adding,
                    onValueChange = { adding = it },
                    placeholder = { Text("uni-heidelberg.de") },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, imeAction = ImeAction.Done, autoCorrectEnabled = false),
                    keyboardActions = KeyboardActions(onDone = { add() }),
                    modifier = Modifier.weight(1f),
                )
                IconButton(onClick = add) {
                    Icon(Icons.Filled.Add, contentDescription = model.t("app.save"))
                }
            }
        }
    }
}

@Composable
fun SecondFactorScreen(model: AppModel, @Suppress("UNUSED_PARAMETER") activity: MainActivity) {
    val status = model.status ?: return
    val scope = rememberCoroutineScope()
    var secret by rememberSaveable { mutableStateOf("") }
    var valid by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    // One code from the user's app where the core can add itself in the MFA portal, as on the
    // desktop; null while the list loads.
    var auto by remember { mutableStateOf<Boolean?>(if (model.enrollSentAt > 0) true else null) }
    var byHand by rememberSaveable { mutableStateOf(false) }
    // The university's portal and steps for the key, as on the setup page and the desktop guide.
    var uni by remember { mutableStateOf<University?>(null) }
    val context = LocalContext.current
    LaunchedEffect(Unit) {
        val list = model.get("/universities.json")?.optJSONArray("universities")
        val found = list?.let { a -> (0 until a.length()).map { a.getJSONObject(it) } }
            ?.firstOrNull { it.optString("id") == status.optString("university") }
        uni = found?.let { University(it) }
        if (auto != null) return@LaunchedEffect
        auto = uni?.enroll == true
    }
    val autoPage = auto == true && !byHand

    fun leave() {
        if (auto == true && byHand) {
            byHand = false
            error = null
        } else {
            model.cancelEnroll()
            model.back()
        }
    }
    BackHandler { leave() }
    Column(Modifier.fillMaxSize().navigationBarsPadding().imePadding().verticalScroll(rememberScrollState())) {
        Bar(model, model.t("app.second_factor"), onBack = { leave() }) {
            Info(model, if (autoPage) model.t("setup.auto_help") else mfaSteps(model, uni).joinToString("\n") + "\n\n" + model.t("setup.totp_help"))
        }
        Page {
            when {
                auto == null -> CircularProgressIndicator()
                autoPage -> AutoSecondFactor(
                    model,
                    model.enrollError,
                    onFirstCode = { model.sendEnrollCode(it) },
                    onByHand = {
                        model.cancelEnroll()
                        byHand = true
                    },
                    onDone = { model.back() },
                    onFailed = { model.enrollError = it },
                )
                else -> {
                    uni?.portal?.takeIf { it.isNotEmpty() }?.let { portal ->
                        TextButton(onClick = {
                            try {
                                context.startActivity(Intent(Intent.ACTION_VIEW, portal.toUri()))
                            } catch (_: ActivityNotFoundException) {
                                // No browser on this phone or profile: nothing to open.
                            }
                        }) { Text(model.t("setup.mfa_portal")) }
                    }
                    TotpEntry(model, secret, { secret = it }, { valid = it }, error)
                    Actions {
                        Button(enabled = valid, onClick = {
                            scope.launch {
                                val result = model.post("/api/totp", JSONObject().put("secret", secret.trim()))
                                if (result.ok) model.back() else error = result.error
                            }
                        }) { Text(model.t("app.save")) }
                    }
                }
            }
        }
    }
}

@Composable
fun LanguageScreen(model: AppModel) {
    val status = model.status ?: return
    val scope = rememberCoroutineScope()
    val setting = status.optString("language")
    Column(Modifier.fillMaxSize()) {
        Bar(model, model.t("settings.language"))
        LazyColumn(contentPadding = WindowInsets.navigationBars.asPaddingValues()) {
            items(listOf("" to model.t("settings.language_system")) + model.strings.languages) { (code, name) ->
                ListItem(
                    headlineContent = { Text(name) },
                    leadingContent = { RadioButton(selected = code == setting, onClick = null) },
                    modifier = Modifier.clickable {
                        scope.launch { model.post("/api/language", JSONObject().put("language", code)) }
                    },
                )
            }
        }
    }
}

@Composable
fun LogScreen(model: AppModel) {
    val lines = model.status?.optJSONArray("log_tail")?.let { a -> (0 until a.length()).map { a.getString(it) } } ?: emptyList()
    Column(Modifier.fillMaxSize()) {
        Bar(model, model.t("settings.log"))
        Text(
            lines.joinToString("\n"),
            fontFamily = FontFamily.Monospace,
            fontSize = 11.sp,
            modifier = Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).horizontalScroll(rememberScrollState())
                .navigationBarsPadding().padding(16.dp),
        )
    }
}

/** The licences of openconnect, OpenSSL and the rest the app carries (assets/licenses). */
@Composable
fun LicensesScreen(model: AppModel) {
    val context = LocalContext.current
    val texts = remember {
        val assets = context.assets
        (assets.list("licenses") ?: emptyArray()).sorted().map { name ->
            name to assets.open("licenses/$name").bufferedReader().use { it.readText() }
        }
    }
    var open by rememberSaveable { mutableStateOf<String?>(null) }
    // Android's back closes the text first, like the arrow.
    BackHandler(enabled = open != null) { open = null }
    Column(Modifier.fillMaxSize()) {
        Bar(model, open ?: model.t("phone.licenses"), onBack = { if (open != null) open = null else model.back() })
        val shown = texts.firstOrNull { it.first == open }
        if (shown != null) {
            Text(
                shown.second,
                fontFamily = FontFamily.Monospace,
                fontSize = 11.sp,
                modifier = Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).navigationBarsPadding().padding(16.dp),
            )
        } else {
            LazyColumn(contentPadding = WindowInsets.navigationBars.asPaddingValues()) {
                items(texts) { (name, _) -> SettingRow(name.replace(Regex("-(LICENSE|COPYING|Copyright).*"), ""), null, { open = name }) }
            }
        }
    }
}

/** Scans the code another device shows ("Use settings from another device" on a computer). */
fun scanPairing(context: android.content.Context, model: AppModel, owner: String, onError: (String?) -> Unit) {
    Scanner.scanQr(context, { onError(model.t("phone.scan_failed")) }) { value ->
        if (value == null || !value.startsWith("UNIVPN:")) return@scanQr
        // The answer comes as model.pairResult (PairScreen).
        onError(null)
        model.enterPairCode(value.removePrefix("UNIVPN:"), owner, inSetup = false)
    }
}
