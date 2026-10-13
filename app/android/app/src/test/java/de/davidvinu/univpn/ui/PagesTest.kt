package de.davidvinu.univpn.ui

import androidx.compose.runtime.AbstractApplier
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Composition
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.MonotonicFrameClock
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.Recomposer
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.LocalSaveableStateRegistry
import androidx.compose.runtime.saveable.SaveableStateRegistry
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import de.davidvinu.univpn.Screen
import de.davidvinu.univpn.Visit
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Test

private class NoNodes : AbstractApplier<Unit>(Unit) {
    override fun insertTopDown(index: Int, instance: Unit) {}
    override fun insertBottomUp(index: Int, instance: Unit) {}
    override fun remove(index: Int, count: Int) {}
    override fun move(from: Int, to: Int, count: Int) {}
    override fun onClear() {}
}

private object Frames : MonotonicFrameClock {
    override suspend fun <R> withFrameNanos(onFrame: (Long) -> R): R = onFrame(System.nanoTime())
}

/** What App.kt reads from AppModel for Pages: the stack (as AppModel keeps it) and setup. */
private class Model {
    val stack = mutableStateListOf(Visit(Screen.Home, "home"))
    var ready by mutableStateOf(true) // false: the spinner before the core answers
    var setup by mutableStateOf(false)
    fun open(screen: Screen) { stack.add(Visit(screen)) }
    fun home() { stack.clear(); stack.add(Visit(Screen.Home, "home")) }
}

/**
 * What a screen saves for turning the phone belongs to its visit (App.kt Pages): turning keeps
 * it, a later visit (also one after Android ended the app) starts afresh. Each run of [activity]
 * is one activity: the same model is turning the phone, a new one is Android having ended the app.
 */
class PagesTest {
    private var made = 0
    /** The field each page shows: a screen's (saved, made on its first showing) or setup's step. */
    private val shown = mutableMapOf<String, String>()
    private var step: MutableState<String>? = null

    @Composable
    private fun Field(name: String) {
        val value by rememberSaveable { mutableStateOf("$name-" + (++made)) }
        shown[name] = value
    }

    @Composable
    private fun Root(m: Model) {
        if (!m.ready) return
        Pages(setup = m.setup, visit = m.stack.last().id, setupFlow = {
            val s = rememberSaveable { mutableStateOf("university") }
            step = s
            shown["setup"] = s.value
        }) {
            // As in App.kt: one call per screen.
            when (m.stack.last().screen) {
                Screen.Home -> Field("home")
                Screen.Settings -> Field("settings")
                is Screen.Pair -> Field("pair")
                else -> Field("other")
            }
        }
    }

    private fun activity(saved: Map<String, List<Any?>>?, m: Model, vararg actions: () -> Unit): Map<String, List<Any?>> =
        runBlocking {
            withContext(Frames) {
                val registry = SaveableStateRegistry(saved) { true }
                val recomposer = Recomposer(coroutineContext)
                val job = launch { recomposer.runRecomposeAndApplyChanges() }
                val composition = Composition(NoNodes(), recomposer)
                composition.setContent { CompositionLocalProvider(LocalSaveableStateRegistry provides registry) { Root(m) } }
                suspend fun settle() = repeat(10) { Snapshot.sendApplyNotifications(); delay(5) }
                settle()
                for (action in actions) {
                    Snapshot.withMutableSnapshot { action() }
                    settle()
                }
                registry.performSave().also {
                    composition.dispose()
                    recomposer.cancel()
                    job.cancel()
                }
            }
        }

    @Test
    fun turningKeepsAddADeviceAndAfterAndroidEndedTheAppItStartsAfresh() {
        val m = Model()
        val saved = activity(null, m, { m.open(Screen.Settings) }, { m.open(Screen.Pair("offer")) })
        val code = shown["pair"]
        activity(saved, m)
        assertEquals(code, shown["pair"])
        val restarted = Model()
        activity(saved, restarted, { restarted.open(Screen.Settings) }, { restarted.open(Screen.Pair("offer")) })
        assertNotEquals("the code of a core that is gone", code, shown["pair"])
    }

    @Test
    fun setupGoesOnAfterAndroidEndedTheApp() {
        val m = Model().apply { setup = true }
        val saved = activity(null, m, { step!!.value = "sign_in" })
        // Saved again while the spinner still shows, then the core answers.
        val waiting = Model().apply { setup = true; ready = false }
        val savedAgain = activity(saved, waiting)
        activity(savedAgain, Model().apply { setup = true })
        assertEquals("sign_in", shown["setup"])
    }

    @Test
    fun anAccountChangeStartsAtTheUniversity() {
        val m = Model()
        val saved = activity(null, m, { m.open(Screen.Settings) }, { m.setup = true }, { step!!.value = "sign_in" })
        activity(saved, m)
        assertEquals("turning the phone keeps the page", "sign_in", shown["setup"])
        // Left for Home and changed again, in the same run or after Android ended the app.
        activity(saved, m, { m.setup = false; m.home() }, { m.open(Screen.Settings) }, { m.setup = true })
        assertEquals("university", shown["setup"])
        val restarted = Model()
        activity(saved, restarted, { restarted.open(Screen.Settings) }, { restarted.setup = true })
        assertEquals("university", shown["setup"])
    }
}
