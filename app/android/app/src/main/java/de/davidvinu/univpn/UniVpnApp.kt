package de.davidvinu.univpn

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager

class UniVpnApp : Application() {
    lateinit var core: Core
        private set

    override fun onCreate() {
        super.onCreate()
        core = Core(this)
        getSystemService(NotificationManager::class.java).createNotificationChannel(
            NotificationChannel(UniVpnService.CHANNEL, getString(R.string.channel), NotificationManager.IMPORTANCE_LOW)
                .apply { setShowBadge(false) },
        )
    }
}

val android.content.Context.core: Core get() = (applicationContext as UniVpnApp).core
