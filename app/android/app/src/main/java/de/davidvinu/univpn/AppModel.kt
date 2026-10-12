package de.davidvinu.univpn

import android.app.Application
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.core.os.ConfigurationCompat
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.util.Locale

/** Stages of status.json "enroll" while the setup with one code runs (internal/daemon/enroll.go). */
private val ENROLL_RUNNING = setOf("connecting", "code", "working")

/** Where the app is. Setup is not a screen here: it shows whenever setup is needed or asked for. */
sealed interface Screen {
    data object Home : Screen
    data object Settings : Screen
    data object Websites : Screen
    data object SecondFactor : Screen
    data object Language : Screen
    data object Log : Screen
    data object Licenses : Screen
    data class Pair(val mode: String) : Screen
}

class AppModel(app: Application) : AndroidViewModel(app) {
    val core = app.core
    var status by mutableStateOf<JSONObject?>(null)
        private set
    /** The core did not answer the last poll. */
    var unreachable by mutableStateOf(false)
        private set
    /** Another app holds the core's port (Core.portTaken). */
    var portTaken by mutableStateOf(false)
        private set
    /** Android turned the VPN down without asking: another app's VPN is always on, or a rule. */
    var vpnRefused by mutableStateOf(false)
    var strings by mutableStateOf(Strings(null, "", emptyList()))
        private set
    private var locales: JSONObject? = null
    private var stringsFor = ""

    val stack = mutableStateListOf<Screen>(Screen.Home)
    val screen get() = stack.last()

    /** Setup shows: needed, or the user changes university or username from Settings. */
    var changingAccount by mutableStateOf(false)
    /** Settings opens with the password dialog (the Home screen's "Update password"). */
    var passwordDialog by mutableStateOf(false)
    /** Setup finished in this run and its last page ("All set") is still open. */
    var setupFinishing by mutableStateOf(false)
    /** The setup request: kept here, not in the screen, so turning the phone does not lose it. */
    var setupBusy by mutableStateOf(false)
        private set
    /** When setup was accepted and the first sign-in started; 0 while none is awaited. */
    var setupSentAt by mutableLongStateOf(0L)
    var setupFailure by mutableStateOf<Result?>(null)

    /** [withCode]: the body carries a code from the user's app instead of the setup key. */
    fun sendSetup(body: JSONObject, withCode: Boolean = false) {
        setupBusy = true
        setupSentAt = 0
        viewModelScope.launch {
            val result = post("/api/setup", body, timeoutMs = 60_000)
            setupBusy = false
            when {
                !result.ok -> setupFailure = result
                withCode -> enrolling("connecting")
                else -> setupSentAt = System.currentTimeMillis()
            }
        }
    }

    // The second factor with one code (index.html step2a): the core signs in with a code from
    // the user's app and adds itself in the MFA portal. Kept here, like setup, for turning the
    // phone.
    /** When the last code went to the core; 0 while no setup with a code runs. */
    var enrollSentAt by mutableLongStateOf(0L)
    /** What that code started, until status.json catches up. */
    var enrollPending by mutableStateOf("")
        private set
    var enrollError by mutableStateOf<String?>(null)
    val enroll: JSONObject? get() = status?.optJSONObject("enroll")
    private var enrollChecked = false

    private fun enrolling(stage: String) {
        enrollPending = stage
        enrollSentAt = System.currentTimeMillis()
    }

    /** /api/totp/auto: the first code in Settings, or the next one the MFA portal asks for. */
    fun sendEnrollCode(code: String) {
        val answer = enrollSentAt > 0 && enroll?.optString("stage") == "code"
        val connected = status?.optString("state") == "connected"
        setupBusy = true
        enrollError = null
        viewModelScope.launch {
            val result = post("/api/totp/auto", JSONObject().put("code", code))
            setupBusy = false
            if (result.ok) enrolling(if (answer || connected) "working" else "connecting") else enrollError = result.error
        }
    }

    /** Leaving the page stops a setup with a code that still runs (index.html stopAuto). */
    fun cancelEnroll() {
        enrollError = null
        if (enrollSentAt == 0L) return
        enrollSentAt = 0
        viewModelScope.launch { post("/api/totp/auto", JSONObject().put("cancel", true)) }
    }

    /** The app opened again while a setup with a code runs: its page again, like the desktop. */
    private fun resumeEnroll(json: JSONObject) {
        val stage = json.optJSONObject("enroll")?.optString("stage").orEmpty()
        if (enrollSentAt > 0 || stage !in ENROLL_RUNNING || json.optBoolean("setup_needed")) return
        enrolling(stage)
        open(Screen.SecondFactor)
    }

    fun open(screen: Screen) {
        stack.add(screen)
    }

    fun back(): Boolean {
        if (stack.size <= 1) return false
        stack.removeAt(stack.lastIndex)
        return true
    }

    fun home() {
        stack.clear()
        stack.add(Screen.Home)
    }

    val setupNeeded get() = status?.optBoolean("setup_needed", false) == true

    /** Polls status.json while the app is on screen, like the desktop apps. */
    suspend fun pollWhileVisible() {
        withContext(Dispatchers.IO) { core.ensureRunning() }
        while (true) {
            refresh()
            delay(if (unreachable) 500 else 1500)
        }
    }

    suspend fun refresh() {
        val reply = withContext(Dispatchers.IO) { core.get("/status.json") }
        portTaken = core.portTaken
        val json = reply.json
        if (!reply.ok || json == null) {
            unreachable = true
            return
        }
        unreachable = false
        status = json
        core.setupDone = !json.optBoolean("setup_needed", true)
        if (!enrollChecked) {
            enrollChecked = true
            resumeEnroll(json)
        }
        val setting = json.optString("language", "")
        val system = ConfigurationCompat.getLocales(getApplication<Application>().resources.configuration).let { list ->
            (0 until list.size()).mapNotNull { list[it] }
        }
        val key = setting + "|" + system.joinToString(",") { it.toLanguageTag() }
        if (locales == null) {
            locales = withContext(Dispatchers.IO) { core.get("/locales.json").json }
        }
        if (key != stringsFor && locales != null) {
            strings = Strings(locales, setting, system.ifEmpty { listOf(Locale.getDefault()) })
            stringsFor = key
        }
    }

    /** POST to the core; returns the error text in the user's language, null on success. */
    suspend fun post(path: String, body: JSONObject? = null, timeoutMs: Int = 10_000): Result {
        val reply = withContext(Dispatchers.IO) { core.post(path, body, timeoutMs) }
        refresh()
        return Result(reply, errorText(reply))
    }

    class Result(val reply: Core.Reply, val error: String?) {
        val ok get() = error == null
        val json get() = reply.json
    }

    fun errorText(reply: Core.Reply): String? {
        if (reply.ok) return null
        if (reply.status == 0) return strings.t("msg.not_running")
        val json = reply.json ?: return reply.text
        return strings.message(json.optString("error"), json.optJSONObject("error_t")).ifEmpty { reply.text }
    }

    suspend fun get(path: String): JSONObject? = withContext(Dispatchers.IO) { core.get(path).json }

    fun t(key: String, vararg args: Pair<String, Any?>) = strings.t(key, args.toMap())
}
