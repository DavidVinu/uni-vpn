package de.davidvinu.univpn

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.os.SystemClock
import android.provider.Settings
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.viewModels
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import de.davidvinu.univpn.ui.App
import de.davidvinu.univpn.ui.UniVpnTheme

class MainActivity : ComponentActivity() {
    private val model: AppModel by viewModels()

    private var promptAt = 0L
    private val vpnPermission = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        if (result.resultCode == RESULT_OK) {
            started()
        } else if (SystemClock.elapsedRealtime() - promptAt < 1500) {
            // No one can tap Cancel that fast: Android said no without asking, because another
            // app's VPN is always on or the phone's administrator forbids VPNs.
            model.vpnRefused = true
        }
    }
    private val notificationPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    /** The switch: on needs Android's one-time "connection request" prompt (as in Tailscale). */
    fun turnOn() {
        val prompt = VpnService.prepare(this)
        if (prompt == null) {
            started()
        } else {
            promptAt = SystemClock.elapsedRealtime()
            vpnPermission.launch(prompt)
        }
    }

    /** Android's VPN list: always-on and "Block connections without VPN" are set there. */
    fun openVpnSettings() {
        try {
            startActivity(Intent(Settings.ACTION_VPN_SETTINGS))
        } catch (_: ActivityNotFoundException) {
            // A phone without that page: nothing to open.
        }
    }

    /** Then the notification permission, one system prompt after the other. */
    private fun started() {
        UniVpnService.start(this)
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    fun turnOff() = UniVpnService.stop(this)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setContent {
            UniVpnTheme {
                val lifecycle = LocalLifecycleOwner.current.lifecycle
                LaunchedEffect(Unit) {
                    lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) { model.pollWhileVisible() }
                }
                val vpnOn by UniVpnService.isOn.collectAsState()
                BackHandler(enabled = model.stack.size > 1) { model.back() }
                App(model, vpnOn, this)
            }
        }
    }
}
