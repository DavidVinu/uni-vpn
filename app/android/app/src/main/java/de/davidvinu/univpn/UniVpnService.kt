package de.davidvinu.univpn

import android.app.Notification
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ProxyInfo
import androidx.core.net.toUri
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
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
        want(this, true)
        if (poll?.isActive == true) return
        poll = scope.launch {
            // The rule names the core's port: Android gets it once our own core holds it, not
            // while another app listens there (Core.ready).
            while (!core.ready()) {
                core.ensureRunning()
                delay(250)
            }
            applyRule(force = true)
            // Websites can change on another device (syncing): the rule follows.
            while (isActive) {
                delay(30_000)
                applyRule(force = false)
            }
        }
    }

    /** (Re)establishes the VPN when the website list changed, so browsers reload the rule. */
    @Synchronized
    private fun applyRule(force: Boolean) {
        if (stopped) return
        // Android does not tell a running VPN when the user turns "Block connections without
        // VPN" on or off: Home's warning follows it here, not only when the VPN starts.
        if (tun != null) blocking.value = isLockdownEnabled
        val status = core.get("/status.json").json
        if (status == null && core.portTaken) {
            // Our core ended and another app took its port (Core.ready): the rule would hand that
            // app the browsers. It comes back with the next answer from our own core.
            synchronized(tunLock) {
                tun?.close()
                tun = null
                pacVersion = null
            }
            blocking.value = false
            return
        }
        // Without an answer the list is unknown: keep the rule as it is (the first start still
        // hands Android the address, which answers once the core listens).
        if (status == null && !force) return
        val domains = status?.optJSONArray("domains")?.join(",") ?: ""
        val version = Integer.toHexString(domains.hashCode())
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

    private fun notification(): Notification =
        Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_tile)
            .setContentTitle(getString(R.string.app_name))
            .setContentIntent(openApp())
            .setOngoing(true)
            .setCategory(Notification.CATEGORY_SERVICE)
            .build()

    private fun openApp(): PendingIntent = PendingIntent.getActivity(
        this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE,
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
        VpnTileService.update(this)
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
        VpnTileService.update(this)
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
            VpnTileService.update(context)
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

private fun org.json.JSONArray.join(separator: String) =
    (0 until length()).joinToString(separator) { optString(it) }
