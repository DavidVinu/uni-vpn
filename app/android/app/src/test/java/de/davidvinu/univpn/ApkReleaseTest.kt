package de.davidvinu.univpn

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
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
}
