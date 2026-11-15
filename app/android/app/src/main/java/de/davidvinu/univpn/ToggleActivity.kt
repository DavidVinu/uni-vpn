package de.davidvinu.univpn

import android.app.Activity
import android.content.Intent
import android.net.VpnService
import android.os.Bundle

/** The widget's tap: no window, it turns the VPN on or off, or opens the app when needed. */
class ToggleActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (UniVpnService.isOn.value) {
            UniVpnService.stop(this)
        } else if (VpnService.prepare(this) != null || !core.setupDone) {
            // The system prompt, or setup first: both need the app, as with the tile.
            startActivity(Intent(this, MainActivity::class.java))
        } else {
            try {
                UniVpnService.start(this)
            } catch (_: IllegalStateException) {
                startActivity(Intent(this, MainActivity::class.java))
            }
        }
        finish()
    }
}
