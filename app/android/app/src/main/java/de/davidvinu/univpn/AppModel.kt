package de.davidvinu.univpn

import android.app.Application
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.core.os.ConfigurationCompat
import androidx.lifecycle.AndroidViewModel
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.util.Locale

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
        val json = reply.json
        if (!reply.ok || json == null) {
            unreachable = true
            return
        }
        unreachable = false
        status = json
        core.setupDone = !json.optBoolean("setup_needed", true)
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
