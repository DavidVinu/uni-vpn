package de.davidvinu.univpn

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.util.Log

/**
 * Turns Uni VPN back on after the phone restarted or the app was updated, when it was on
 * before: otherwise the websites stop working without the user having done anything.
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        if (!UniVpnService.wanted(context) || !context.core.setupDone || VpnService.prepare(context) != null) return
        try {
            UniVpnService.start(context)
        } catch (e: IllegalStateException) {
            // Android may still refuse a start from the background: the switch stays the way in.
            Log.e("uni-vpn", "VPN not turned back on", e)
        }
    }
}
