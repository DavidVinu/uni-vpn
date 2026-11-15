package de.davidvinu.univpn

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File

/**
 * The check before an update is offered (ApkUpdate.matches) on a real Android: this app's own
 * APK is this app, signed like it; another version code is not the one on offer.
 */
@RunWith(AndroidJUnit4::class)
class ApkUpdateTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext

    @Test
    fun theInstalledApkMatchesItself() {
        val apk = File(context.packageCodePath)
        val sum = ApkUpdate.sha256(apk)
        assertTrue(ApkUpdate.matches(context, apk, ApkRelease(BuildConfig.VERSION_CODE.toLong(), apk.length(), sum)))
        assertFalse(ApkUpdate.matches(context, apk, ApkRelease(BuildConfig.VERSION_CODE + 1L, apk.length(), sum)))
    }

    @Test
    fun anotherAppDoesNotMatch() {
        // The test APK: another package, signed with the same debug key.
        val other = File(InstrumentationRegistry.getInstrumentation().context.packageCodePath)
        assertFalse(ApkUpdate.matches(context, other, ApkRelease(BuildConfig.VERSION_CODE.toLong(), other.length(), ApkUpdate.sha256(other))))
    }
}
