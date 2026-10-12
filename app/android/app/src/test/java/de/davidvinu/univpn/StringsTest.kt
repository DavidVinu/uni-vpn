package de.davidvinu.univpn

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test
import java.util.Locale

class StringsTest {
    private val locales = JSONObject(
        """
        {"languages": [["en", "English"], ["de", "Deutsch"], ["zh-Hans", "简体中文"], ["zh-Hant", "繁體中文"]],
         "catalogs": {
           "en": {"look.idle": "Not connected", "msg.not_running": "Restart the computer",
                  "phone.msg.not_running": "Open the app again", "setup.step": "Step {n} of {total}",
                  "app.lines": "{lines}", "msg.x": "Line {line}"},
           "de": {"look.idle": "Nicht verbunden"},
           "zh-Hans": {"look.idle": "未连接"},
           "zh-Hant": {"look.idle": "未連線"}}}
        """,
    )

    @Test
    fun followsTheSettingThenTheSystemThenEnglish() {
        assertEquals("de", Strings(locales, "de", listOf(Locale.US)).lang)
        assertEquals("de", Strings(locales, "", listOf(Locale.GERMANY, Locale.US)).lang)
        assertEquals("en", Strings(locales, "", listOf(Locale.JAPAN)).lang)
        assertEquals("zh-Hant", Strings(locales, "", listOf(Locale.TAIWAN)).lang)
        assertEquals("zh-Hans", Strings(locales, "", listOf(Locale.CHINA)).lang)
        assertEquals("zh-Hant", Strings(locales, "", listOf(Locale.forLanguageTag("zh-Hant-CN"))).lang)
    }

    @Test
    fun phoneTextsReplaceComputerTexts() {
        val s = Strings(locales, "de", emptyList())
        assertEquals("Nicht verbunden", s.t("look.idle"))
        assertEquals("Open the app again", s.t("msg.not_running")) // English fallback of the phone text
        assertEquals("Step 2 of 4", s.t("setup.step", mapOf("n" to 2, "total" to 4)))
        assertEquals("unknown.key", s.t("unknown.key"))
    }

    @Test
    fun messagesWithNestedArguments() {
        val s = Strings(locales, "en", emptyList())
        val message = JSONObject("""{"key": "app.lines", "args": {"lines": [{"key": "msg.x", "args": {"line": 3}}, "plain"]}}""")
        assertEquals("Line 3\nplain", s.t(message))
        assertEquals("fallback", s.message("fallback", null))
    }
}
