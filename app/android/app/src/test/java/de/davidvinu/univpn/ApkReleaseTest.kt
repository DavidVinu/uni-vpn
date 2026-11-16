package de.davidvinu.univpn

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/** uni-vpn-apk.json as ci.yml writes it, and what the app refuses to act on (ApkUpdate). */
class ApkReleaseTest {
    private val sum = "0123456789abcdef".repeat(4)

    @Test
    fun readsWhatCiWrites() {
        val release = ApkRelease.parse("""{"version_code": 512, "version": "0.1.512", "size": 17713833, "sha256": "$sum"}""")!!
        assertEquals(512L, release.versionCode)
        assertEquals(17713833L, release.size)
        assertEquals(sum, release.sha256)
    }

    @Test
    fun refusesAnythingElse() {
        for (text in listOf(
            "",
            "not json",
            """{"size": 1, "sha256": "$sum"}""",
            """{"version_code": 0, "size": 1, "sha256": "$sum"}""",
            """{"version_code": 5, "size": 0, "sha256": "$sum"}""",
            """{"version_code": 5, "size": ${ApkUpdate.MAX_APK + 1}, "sha256": "$sum"}""",
            """{"version_code": 5, "size": 1, "sha256": "${sum.uppercase()}"}""",
            """{"version_code": 5, "size": 1, "sha256": "${sum.drop(1)}"}""",
            """{"version_code": "five", "size": 1, "sha256": "$sum"}""",
        )) {
            assertNull(text, ApkRelease.parse(text))
        }
    }

    @Test
    fun digestIsSha256InLowercaseHex() {
        val file = File.createTempFile("apk", ".bin").apply { writeText("abc"); deleteOnExit() }
        assertEquals("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", ApkUpdate.sha256(file))
    }

    /** Most users never open the app again: Android 12 and later put an update in without a tap. */
    @Test
    fun anUpdateGoesInWithoutATapWhileNoOneUsesTheApp() {
        val day = 24 * 60 * 60 * 1000L
        for (state in listOf("idle", "offline", "blocked", "error", "auth_failed", "keyring", null)) {
            assertTrue("$state", ApkUpdate.unattended(31, onScreen = false, state = state, tried = null, sha256 = sum, sinceTried = 0))
        }
        // Tried yesterday, or another version: again.
        assertTrue(ApkUpdate.unattended(36, false, "idle", sum, sum, day))
        assertTrue(ApkUpdate.unattended(36, false, "idle", "f".repeat(64), sum, 1))
        // A clock set back counts as a day gone.
        assertTrue(ApkUpdate.unattended(36, false, "idle", sum, sum, -1))
    }

    @Test
    fun anUpdateWaitsWhileTheAppIsShownOrTheTunnelIsInUse() {
        // Android 10 and 11 always ask: the tap only.
        assertFalse(ApkUpdate.unattended(30, false, "idle", null, sum, 0))
        // On screen, it would close under the user.
        assertFalse(ApkUpdate.unattended(36, true, "idle", null, sum, 0))
        for (state in listOf("connecting", "connected", "disconnecting")) {
            assertFalse(state, ApkUpdate.unattended(36, false, state, null, sum, 0))
        }
        // Tried today already (Android wanted to ask): the offer stays, not tried every 30 s.
        assertFalse(ApkUpdate.unattended(36, false, "idle", sum, sum, 60_000))
    }
}
