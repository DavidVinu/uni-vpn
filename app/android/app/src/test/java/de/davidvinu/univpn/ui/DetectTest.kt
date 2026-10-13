package de.davidvinu.univpn.ui

import de.davidvinu.univpn.Strings
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** The answer of /api/detect for an address that is not listed, read like the page (SetupScreen.kt). */
class DetectTest {
    private val strings = Strings(
        JSONObject(
            """
            {"languages": [["en", "English"]],
             "catalogs": {"en": {"setup.saml": "Needs a web page", "detect.not_cisco": "Not a university VPN"}}}
            """,
        ),
        "en",
        emptyList(),
    )

    @Test
    fun aSamlDefaultGroupStillShowsTheGroupsAndNoSecondFactor() {
        // Stanford's gateway (tests/fixtures/detect/stanford.xml): the default group needs a web
        // page, the group Stanford is a password form.
        val d = detected(
            JSONObject(
                """
                {"ok": true, "saml": true, "groups": ["CardinalKey", "Stanford"], "group": "CardinalKey",
                 "error": "", "error_t": null, "suggestion": {"mfa": "saml"}}
                """,
            ),
            strings,
        )
        assertEquals("Needs a web page", d.error)
        assertEquals(listOf("CardinalKey", "Stanford"), d.groups)
        assertEquals("CardinalKey", d.group)
        assertEquals("", d.mfa)
    }

    @Test
    fun anEmptyErrorIsNoError() {
        val d = detected(
            JSONObject("""{"ok": true, "saml": false, "groups": ["A", "B"], "group": "B", "error": "", "error_t": null, "suggestion": {"mfa": "none"}}"""),
            strings,
        )
        assertNull(d.error)
        assertEquals(listOf("A", "B"), d.groups)
        assertEquals("B", d.group)
        assertEquals("none", d.mfa)
    }

    @Test
    fun aGatewayErrorKeepsWhatWasThere() {
        val d = detected(
            JSONObject("""{"ok": true, "groups": [], "error": "Not a university VPN", "error_t": {"key": "detect.not_cisco", "args": {}}}"""),
            strings,
        )
        assertEquals("Not a university VPN", d.error)
        assertNull(d.groups)
        assertNull(d.mfa)
    }
}
