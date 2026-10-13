package de.davidvinu.univpn

import android.icu.text.PluralRules
import org.json.JSONArray
import org.json.JSONObject
import java.util.Locale

/**
 * The core's translations (/locales.json, uni_vpn/locales/<lang>.json), so phone and computer
 * say the same thing. Keys starting with "phone." replace a text that speaks of a computer.
 */
class Strings(private val locales: JSONObject?, setting: String, systemLocales: List<Locale>) {
    val languages: List<Pair<String, String>> = locales?.optJSONArray("languages")?.let { array ->
        (0 until array.length()).map { array.getJSONArray(it).let { pair -> pair.getString(0) to pair.getString(1) } }
    } ?: listOf("en" to "English")

    private val catalogs = locales?.optJSONObject("catalogs")
    /** The core's texts arrived: before the core first answers there are none (App.kt). */
    val loaded: Boolean get() = catalogs != null
    val lang: String = if (setting.isNotEmpty() && catalogs?.has(setting) == true) setting else systemLanguage(systemLocales)

    /** Like the status page: the first system language uni-vpn has, else English. */
    private fun systemLanguage(system: List<Locale>): String {
        val codes = languages.map { it.first }
        for (locale in system) {
            if (locale.language == "zh") {
                val hant = locale.script == "Hant" || (locale.script != "Hans" && locale.country in setOf("TW", "HK", "MO"))
                return if (hant) "zh-Hant" else "zh-Hans"
            }
            if (locale.language in codes) return locale.language
        }
        return "en"
    }

    private fun lookup(key: String): String? =
        catalogs?.optJSONObject(lang)?.optString(key)?.takeIf { it.isNotEmpty() }
            ?: catalogs?.optJSONObject("en")?.optString(key)?.takeIf { it.isNotEmpty() }

    fun t(key: String, args: Map<String, Any?> = emptyMap()): String {
        val text = lookup("phone.$key") ?: lookup(key) ?: key
        return PLACEHOLDER.replace(text) { m -> if (args.containsKey(m.groupValues[1])) arg(args[m.groupValues[1]]) else m.value }
    }

    /** A message_t or error_t object: {"key", "args"}. */
    fun t(value: JSONObject): String {
        val args = value.optJSONObject("args")
        val map = args?.keys()?.asSequence()?.associateWith { args.get(it) } ?: emptyMap()
        return t(value.optString("key"), map)
    }

    private fun arg(value: Any?): String = when (value) {
        is JSONArray -> (0 until value.length()).joinToString("\n") { arg(value.get(it)) }
        is JSONObject -> if (value.has("key")) t(value) else value.toString()
        null, JSONObject.NULL -> ""
        else -> value.toString()
    }

    fun plural(key: String, n: Int): String {
        val form = PluralRules.forLocale(Locale.forLanguageTag(lang)).select(n.toDouble())
        val chosen = if (lookup("$key.$form") != null) form else "other"
        return t("$key.$chosen", mapOf("n" to n))
    }

    /** The text of the core's message, or of an API error, in the user's language. */
    fun message(text: String?, translated: JSONObject?): String =
        translated?.takeIf { it.has("key") }?.let { t(it) } ?: text.orEmpty()

    companion object {
        private val PLACEHOLDER = Regex("""\{(\w+)\}""")
    }
}
