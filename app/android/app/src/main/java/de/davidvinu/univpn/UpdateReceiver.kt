package de.davidvinu.univpn

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.os.Build
import android.util.Log

/**
 * Android's installer answers here (ApkUpdate.install). After a tap it asks the user first, from
 * the app the user just tapped Update in; without one (ApkUpdate.installUnattended) it does not
 * ask, or the update is left on offer. On success Android ends the app and starts the new one
 * (BootReceiver turns the VPN back on).
 */
class UpdateReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)
        if (status == PackageInstaller.STATUS_PENDING_USER_ACTION && intent.getBooleanExtra(ApkUpdate.EXTRA_UNATTENDED, false)) {
            // Android would ask after all, with no one looking: Home and the notification still
            // offer the update, and a tap puts it in.
            val id = intent.getIntExtra(PackageInstaller.EXTRA_SESSION_ID, -1)
            if (id >= 0) runCatching { context.packageManager.packageInstaller.abandonSession(id) }
            Log.i("uni-vpn", "update left on offer: Android would ask")
        } else if (status == PackageInstaller.STATUS_PENDING_USER_ACTION) {
            val confirm = if (Build.VERSION.SDK_INT >= 33) {
                intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
            } else {
                @Suppress("DEPRECATION")
                intent.getParcelableExtra(Intent.EXTRA_INTENT)
            }
            try {
                // Not exported: only Android's installer sends this, so the intent is its own.
                confirm?.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)?.let { context.startActivity(it) }
            } catch (e: Exception) {
                Log.e("uni-vpn", "installer did not open", e)
            }
            // The installer's own screen shows now; the next answer comes after it.
        } else if (status != PackageInstaller.STATUS_SUCCESS) {
            Log.i("uni-vpn", "update not installed: $status ${intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)}")
        }
        ApkUpdate.installing.value = false
    }
}
