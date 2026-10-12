package de.davidvinu.univpn

import android.annotation.SuppressLint
import android.app.PendingIntent
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService

/** Quick Settings tile: one tap turns Uni VPN on or off (role model: Tailscale's tile). */
class VpnTileService : TileService() {
    override fun onStartListening() = render()

    override fun onClick() {
        if (UniVpnService.isOn.value) {
            UniVpnService.stop(this)
        } else if (VpnService.prepare(this) != null || !core.setupDone) {
            // The system prompt, or setup first: both need the app.
            val intent = Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            if (Build.VERSION.SDK_INT >= 34) {
                startActivityAndCollapse(PendingIntent.getActivity(this, 0, intent, PendingIntent.FLAG_IMMUTABLE))
            } else {
                @Suppress("DEPRECATION")
                @SuppressLint("StartActivityAndCollapseDeprecated") // the PendingIntent form needs Android 14
                startActivityAndCollapse(intent)
            }
            return
        } else {
            UniVpnService.start(this)
        }
        render(!UniVpnService.isOn.value)
    }

    private fun render(on: Boolean = UniVpnService.isOn.value) {
        val tile = qsTile ?: return
        tile.state = if (on) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
        tile.updateTile()
    }

    companion object {
        fun update(context: Context) =
            requestListeningState(context, ComponentName(context, VpnTileService::class.java))
    }
}
