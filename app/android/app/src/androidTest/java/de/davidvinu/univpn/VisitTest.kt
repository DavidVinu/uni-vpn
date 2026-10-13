package de.davidvinu.univpn

import android.app.Application
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Test
import org.junit.runner.RunWith

/**
 * What a screen saves for turning the phone belongs to its visit (App.kt): a later visit, also
 * one after Android ended the app, must not get it. Add a device would show a code that is gone.
 */
@RunWith(AndroidJUnit4::class)
class VisitTest {
    private val app = InstrumentationRegistry.getInstrumentation().targetContext.applicationContext as Application

    @Test
    fun everyVisitOfAScreenIsANewOne() {
        val model = AppModel(app)
        model.open(Screen.Settings)
        val settings = model.visit
        model.open(Screen.Pair("offer"))
        val pair = model.visit
        model.back()
        assertEquals(settings, model.visit)
        model.open(Screen.Pair("offer"))
        assertNotEquals(pair, model.visit)
        // Android ended the app: the new model starts at Home, Add a device again is a new visit.
        val restarted = AppModel(app)
        assertEquals(Screen.Home, restarted.screen)
        restarted.open(Screen.Settings)
        restarted.open(Screen.Pair("offer"))
        assertNotEquals(pair, restarted.visit)
    }
}
