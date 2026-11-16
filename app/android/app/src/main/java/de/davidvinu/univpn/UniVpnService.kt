package de.davidvinu.univpn

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ProxyInfo
import androidx.core.net.toUri
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.os.SystemClock
import android.system.OsConstants
import android.util.Log
import androidx.core.content.edit
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/**
 * The VPN carries no traffic itself: it hands the phone the core's proxy rule (proxy.pac), so
 * browsers send only the listed university websites to the core, which opens the tunnel on
 * demand. Everything else, and the core's own connection, stays on the normal network.
 */
class UniVpnService : VpnService() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var tun: ParcelFileDescriptor? = null
    private var pacVersion: String? = null
    private var poll: Job? = null
    /** Set by Stop, so a rule refresh that was already running does not bring the VPN back. */
    @Volatile private var stopped = false
    /** Guards swapping [tun] only, so Stop on the main thread never waits for the network. */
    private val tunLock = Any()

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                shutDown()
                return START_NOT_STICKY
            }
            ACTION_REFRESH -> if (tun != null) scope.launch { applyRule(force = false) }
            else -> start()
        }
        return START_STICKY
    }

    private fun start() {
        if (Build.VERSION.SDK_INT >= 34) {
            // Android 14 lets VPN apps keep a "system exempted" foreground service.
            startForeground(NOTIFICATION, notification(), ServiceInfo.FOREGROUND_SERVICE_TYPE_SYSTEM_EXEMPTED)
        } else {
            startForeground(NOTIFICATION, notification())
        }
        running.value = true
        stopped = false
        answeredAt = SystemClock.elapsedRealtime()
        want(this, true)
        Toggles.update(this)
        if (poll?.isActive == true) return
        poll = scope.launch {
            while (isActive) {
                // The rule names the core's port: Android gets it once our own core holds it,
                // not while another app listens there (Core.ready).
                var waited = 0
                while (!core.ready()) {
                    // A release whose core does not start on this phone: the next one still
                    // reaches it (ApkUpdate).
                    if (waited++ % 120 == 0) update()
                    core.ensureRunning()
                    delay(250)
                }
                applyRule(force = true)
                // Websites can change on another device (syncing): the rule follows, every
                // 30 s. Our core ending (a crash, Android ending it, Repair) takes the rule down
                // within a second, not at the next round: another app could take the free ports
                // before the next core does and answer the browsers. The next core brings it back.
                var seconds = 0
                while (isActive && core.ready()) {
                    if (seconds % 30 == 0) update()
                    delay(1_000)
                    if (++seconds % 30 == 0) applyRule(force = false)
                }
                withdraw()
            }
        }
    }

    /**
     * No rule until our own core holds its ports again. With [applyRule]'s lock: a refresh that
     * is still establishing the old rule cannot bring it back afterwards.
     */
    @Synchronized
    private fun withdraw() {
        synchronized(tunLock) {
            tun?.close()
            tun = null
            pacVersion = null
        }
        blocking.value = false
    }

    /** (Re)establishes the VPN when the rule changed, so browsers reload it. */
    @Synchronized
    private fun applyRule(force: Boolean) {
        if (stopped) return
        // Android does not tell a running VPN when the user turns "Block connections without
        // VPN" on or off: Home's warning follows it here, not only when the VPN starts.
        if (tun != null) blocking.value = isLockdownEnabled
        val status = core.get("/status.json").json
        if (status != null) answeredAt = SystemClock.elapsedRealtime()
        status?.let { ApkUpdate.setAutomatic(this, it.optBoolean("auto_update", true)) }
        if (status == null && core.portTaken) {
            // Our core ended and another app took its port (Core.ready): the rule would hand that
            // app the browsers. It comes back with the next answer from our own core.
            withdraw()
            return
        }
        // Without an answer the rule is unknown: keep it as it is (the first start still hands
        // Android the address, which answers once the core listens).
        val rule = if (status != null) core.get("/proxy.pac").takeIf { it.ok } else null
        if (rule == null && !force) return
        // The rule itself: new websites or another university's security page, a new address.
        val version = Integer.toHexString((rule?.text ?: "").hashCode())
        if (!force && version == pacVersion && tun != null) return
        val builder = Builder()
            .setSession(getString(R.string.app_name))
            // An address nothing uses (TEST-NET-1) and no routes worth the name: nothing is
            // tunnelled, so the phone keeps its own DNS and every other connection as it is.
            .addAddress("192.0.2.1", 32)
            .addRoute("192.0.2.2", 32)
            // Without these Android blocks the families the VPN does not configure, and an
            // IPv6-only mobile network would lose the internet.
            .allowFamily(OsConstants.AF_INET)
            .allowFamily(OsConstants.AF_INET6)
            .setHttpProxy(ProxyInfo.buildPacProxy(core.pacUrl(version).toUri()))
            .setBlocking(false)
            .setMetered(false)
            .setConfigureIntent(openApp())
        builder.addDisallowedApplication(packageName)
        val fd = try {
            builder.establish()
        } catch (e: Exception) {
            Log.e("uni-vpn", "VPN not established", e)
            null
        }
        if (fd == null) {
            // The permission was taken back (another VPN app, for example).
            shutDown()
            return
        }
        synchronized(tunLock) {
            if (stopped) {
                fd.close()
                return
            }
            tun?.close()
            tun = fd
            pacVersion = version
        }
        blocking.value = isLockdownEnabled
    }

    /** A newer version of the app is out (ApkUpdate): the notification offers it. */
    @Volatile private var updateShown = false
    /** When the core last answered, or [start] ran since (SystemClock.elapsedRealtime). */
    @Volatile private var answeredAt = 0L
    private var updateCheck: Job? = null

    /**
     * Most users never open the app again once it is set up: a new version goes in while the
     * VPN is not in use (Android 12 and later), else the notification says that there is one.
     * Beside the rule, so a download does not hold up the next website list.
     */
    private fun update() {
        if (updateCheck?.isActive == true) return
        updateCheck = scope.launch {
            ApkUpdate.check(this@UniVpnService)
            if (ApkUpdate.offered.value != null) {
                val status = core.get("/status.json").json
                if (status != null) answeredAt = SystemClock.elapsedRealtime()
                // Without an answer only once the core has not answered for a minute (a release
                // whose core does not start on this phone), not for one request that failed.
                if (status != null || SystemClock.elapsedRealtime() - answeredAt > 60_000) {
                    ApkUpdate.installUnattended(this@UniVpnService, status)
                }
            }
            val offered = ApkUpdate.offered.value != null
            // Under the lock Stop takes before it removes the notification: never brought back
            // after that.
            synchronized(tunLock) {
                if (offered == updateShown || stopped) return@launch
                updateShown = offered
                getSystemService(NotificationManager::class.java).notify(NOTIFICATION, notification())
            }
        }
    }

    private fun notification(): Notification =
        Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_tile)
            .setContentTitle(getString(R.string.app_name))
            .apply { if (updateShown) setContentText(getString(R.string.update)) }
            .setContentIntent(if (updateShown) openApp(MainActivity.ACTION_UPDATE) else openApp())
            .setOngoing(true)
            .setCategory(Notification.CATEGORY_SERVICE)
            .build()

    private fun openApp(action: String? = null): PendingIntent = PendingIntent.getActivity(
        this,
        if (action == null) 0 else 1,
        Intent(this, MainActivity::class.java).setAction(action).apply { if (action != null) putExtra(MainActivity.EXTRA_KEY, ApkUpdate.tapKey) },
        // Updated: the key is new after the app was restarted.
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    private fun shutDown() {
        stopped = true
        poll?.cancel()
        running.value = false
        blocking.value = false
        want(this, false)
        scope.launch { core.post("/api/disconnect") }
        synchronized(tunLock) {
            tun?.close()
            tun = null
            pacVersion = null
        }
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
        Toggles.update(this)
    }

    override fun onRevoke() = shutDown()

    override fun onDestroy() {
        stopped = true
        running.value = false
        blocking.value = false
        synchronized(tunLock) {
            tun?.close()
            tun = null
        }
        scope.cancel()
        Toggles.update(this)
        super.onDestroy()
    }

    companion object {
        const val CHANNEL = "connection"
        private const val NOTIFICATION = 1
        private const val ACTION_STOP = "de.davidvinu.univpn.STOP"
        private const val ACTION_REFRESH = "de.davidvinu.univpn.REFRESH"

        private val running = MutableStateFlow(false)
        val isOn: StateFlow<Boolean> = running

        /**
         * Android's "Block connections without VPN" is on for Uni VPN. This VPN carries nothing,
         * so the other apps reach nothing: Home says so.
         */
        private val blocking = MutableStateFlow(false)
        val lockdown: StateFlow<Boolean> = blocking

        /** On as the user left it, so it comes back after a restart or an update (BootReceiver). */
        fun wanted(context: Context) = context.getSharedPreferences("vpn", Context.MODE_PRIVATE).getBoolean("on", false)

        private fun want(context: Context, on: Boolean) =
            context.getSharedPreferences("vpn", Context.MODE_PRIVATE).edit { putBoolean("on", on) }

        /** Call only after VpnService.prepare returned null. */
        fun start(context: Context) {
            context.startForegroundService(Intent(context, UniVpnService::class.java))
        }

        fun stop(context: Context) {
            if (!running.value) return
            context.startService(Intent(context, UniVpnService::class.java).setAction(ACTION_STOP))
        }

        /** The website list changed here. */
        fun refresh(context: Context) {
            if (!running.value) return
            context.startService(Intent(context, UniVpnService::class.java).setAction(ACTION_REFRESH))
        }
    }
}
