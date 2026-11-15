package de.davidvinu.univpn

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** When Core gives up on a Keystore key that keeps failing (Core.secretsKey). */
class LastingFailureTest {
    @Test
    fun aMinuteOfFailuresEverySecondCounts() {
        val f = LastingFailure(patienceMs = 60_000, gapMs = 10_000)
        for (t in 0L until 60_000L step 1_000) assertFalse("at $t", f.failed(t))
        assertTrue(f.failed(60_000))
    }

    @Test
    fun failuresFarApartStartAfresh() {
        val f = LastingFailure(patienceMs = 60_000, gapMs = 10_000)
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
    fun aSuccessStartsAfresh() {
        val f = LastingFailure(patienceMs = 60_000, gapMs = 10_000)
        for (t in 0L until 50_000L step 1_000) f.failed(t)
        f.ok()
        assertFalse(f.failed(61_000))
    }
}
