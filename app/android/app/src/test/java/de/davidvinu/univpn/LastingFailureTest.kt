package de.davidvinu.univpn

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** When Core gives up on a Keystore key that keeps failing (Core.secretsKey). */
class LastingFailureTest {
    private fun failure() = LastingFailure(patienceMs = 60_000, gapMs = 10_000, pauseMs = 600_000)

    @Test
    fun aMinuteOfFailuresEverySecondCounts() {
        val f = failure()
        for (t in 0L until 60_000L step 1_000) assertFalse("at $t", f.failed(t))
        assertTrue(f.failed(60_000))
    }

    @Test
    fun failuresFarApartStartAfresh() {
        val f = failure()
        assertFalse(f.failed(0))
        // The app was closed for an hour: one more failure is not a minute of them.
        assertFalse(f.failed(3_600_000))
        // Nor are failures 20 s apart.
        for (t in 3_620_000L..3_700_000L step 20_000) assertFalse("at $t", f.failed(t))
        // A minute of them every second, from the last of those, is.
        for (t in 3_701_000L until 3_760_000L step 1_000) assertFalse("at $t", f.failed(t))
        assertTrue(f.failed(3_760_000))
    }

    @Test
    fun closingAndOpeningTheAppKeepsTheCount() {
        val f = failure()
        // 25 s of failures, until "not running" says to close the app and open it again.
        for (t in 0L..25_000L step 1_000) assertFalse("at $t", f.failed(t))
        // Opened again 30 s later: the count goes on from 25 s.
        for (t in 55_000L until 90_000L step 1_000) assertFalse("at $t", f.failed(t))
        assertTrue(f.failed(90_000))
    }

    @Test
    fun theCountSurvivesARestartOfTheApp() {
        // What Core kept: 50 s of failures, the last at 100 s.
        val f = LastingFailure(patienceMs = 60_000, gapMs = 10_000, pauseMs = 600_000, failing = 50_000, last = 100_000)
        assertFalse(f.failed(130_000))
        for (t in 131_000L until 140_000L step 1_000) assertFalse("at $t", f.failed(t))
        assertTrue(f.failed(140_000))
    }

    @Test
    fun aRestartOfThePhoneStartsAfresh() {
        // elapsedRealtime starts again at 0.
        val f = LastingFailure(patienceMs = 60_000, gapMs = 10_000, pauseMs = 600_000, failing = 59_000, last = 900_000)
        assertFalse(f.failed(5_000))
        assertFalse(f.failed(6_000))
    }

    @Test
    fun aSuccessStartsAfresh() {
        val f = failure()
        for (t in 0L until 50_000L step 1_000) f.failed(t)
        f.ok()
        assertFalse(f.failed(61_000))
        assertFalse(f.failed(62_000))
    }
}
