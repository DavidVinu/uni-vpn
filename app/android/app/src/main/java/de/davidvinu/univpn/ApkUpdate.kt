package de.davidvinu.univpn

import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.content.pm.SigningInfo
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.Build
import android.provider.Settings
import android.util.Log
import androidx.core.content.edit
import androidx.core.net.toUri
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import java.util.UUID
import java.util.concurrent.atomic.AtomicInteger

/** The release's uni-vpn-apk.json: the version code, size and SHA-256 of its uni-vpn.apk. */
class ApkRelease(val versionCode: Long, val size: Long, val sha256: String) {
    companion object {
        private val SHA256 = Regex("[0-9a-f]{64}")

        fun parse(text: String): ApkRelease? = try {
            val json = JSONObject(text)
            val code = json.getLong("version_code")
            val size = json.getLong("size")
            val sha256 = json.getString("sha256")
            if (code > 0 && size in 1..ApkUpdate.MAX_APK && SHA256.matches(sha256)) ApkRelease(code, size, sha256) else null
        } catch (_: Exception) {
            null
        }
    }
}

/**
 * Updates for the app from the download page, which no store updates (role model: Signal's
 * website build). Once a day, while the VPN is on or the app is open, the release's
 * uni-vpn-apk.json says whether a newer uni-vpn.apk is out; on Wi-Fi it comes down at once and
 * is checked, on mobile data only when the user taps. Home and the VPN's notification then
 * offer it, and one tap hands it to Android's installer. On Android 12 and later a file that
 * came down also goes in without a tap while the app is not on screen and the VPN is not in
 * use ([installUnattended]), as the computers install once the tunnel is idle: most users
 * never open the app again. Off in the Play build (the store updates it) and while Automatic
 * updates is off.
 */
object ApkUpdate {
    private const val RELEASE = "https://github.com/DavidVinu/uni-vpn/releases/latest/download/"
    private const val TAG = "uni-vpn"
    private const val DAY = 24 * 60 * 60 * 1000L
    /** After a check that did not get through: again in 10 minutes, not tomorrow. */
    private const val RETRY = 10 * 60 * 1000L
    internal const val MAX_APK = 200L shl 20
    /** On the installer's answer (UpdateReceiver): no one tapped, so Android must not ask. */
    const val EXTRA_UNATTENDED = "de.davidvinu.univpn.UNATTENDED"
    /** The core's states (status.json state) while the tunnel is in use: an update waits. */
    private val BUSY = setOf("connecting", "connected", "disconnecting")

    /** MainActivity is on screen (started and not stopped): an update would close it under the user. */
    val shown = AtomicInteger(0)

    /** A newer version is out: Home and the notification offer it. */
    private val newer = MutableStateFlow<ApkRelease?>(null)
    val offered: StateFlow<ApkRelease?> = newer
    /** The tap is being handled: downloading on mobile data, or handing the file to Android. */
    val installing = MutableStateFlow(false)
    /**
     * Goes with the notification's "Update Uni VPN" (MainActivity.ACTION_UPDATE): the activity is
     * exported, and another app's intent must not start a download.
     */
    val tapKey: String = UUID.randomUUID().toString()
    /** The tap's work outlives the screen: leaving the app mid-download does not leave it busy. */
    private val taps = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    /** The SHA-256 of a release whose download did not match it. */
    @Volatile private var mismatched: String? = null

    private val lock = Any()
    @Volatile private var loaded = false

    private fun prefs(context: Context) = context.getSharedPreferences("update", Context.MODE_PRIVATE)

    private fun apk(context: Context) = File(context.cacheDir, "update/uni-vpn.apk")

    /** What the last check found, after Android ended the app. */
    private fun load(context: Context) {
        if (loaded) return
        loaded = true
        val prefs = prefs(context)
        val release = ApkRelease.parse(prefs.getString("release", null) ?: "")
        if (release != null && release.versionCode > BuildConfig.VERSION_CODE) {
            if (prefs.getBoolean("automatic", true)) newer.value = release
        } else {
            // Installed by now (or never found): the file is not needed any more.
            apk(context).delete()
            prefs.edit { remove("release") }
        }
    }

    /**
     * The user's Automatic updates (status.json auto_update), kept for when the core does not
     * answer. True when it was just switched on: then check right away, as the computers do.
     */
    fun setAutomatic(context: Context, on: Boolean): Boolean {
        if (!BuildConfig.SELF_UPDATE) return false
        val prefs = prefs(context)
        if (prefs.getBoolean("automatic", true) == on) return false
        prefs.edit {
            putBoolean("automatic", on)
            if (on) remove("checked")
        }
        if (!on) newer.value = null
        return on
    }

    /** Checks once a day; cheap otherwise. Blocks: call off the main thread. */
    fun check(context: Context) {
        // A debug build is signed with its own key: no release goes over it.
        if (!BuildConfig.SELF_UPDATE || BuildConfig.DEBUG) return
        synchronized(lock) {
            load(context)
            val prefs = prefs(context)
            if (!prefs.getBoolean("automatic", true)) return
            val now = System.currentTimeMillis()
            // A clock set back counts as due.
            if (now - prefs.getLong("checked", 0) in 0 until DAY) return
            prefs.edit { putLong("checked", now - DAY + RETRY) }
            val release = try {
                ApkRelease.parse(String(fetch(RELEASE + "uni-vpn-apk.json", 64 * 1024), Charsets.UTF_8))
            } catch (e: Exception) {
                Log.i(TAG, "no update check: $e")
                return
            } ?: return
            if (release.versionCode <= BuildConfig.VERSION_CODE || release.sha256 == prefs.getString("refused", null)) {
                newer.value = null
                apk(context).delete()
                prefs.edit { remove("release"); remove("misses"); putLong("checked", now) }
                return
            }
            prefs.edit { putString("release", JSONObject().put("version_code", release.versionCode).put("size", release.size).put("sha256", release.sha256).toString()) }
            newer.value = release
            if (!unmetered(context) || download(context, release) != null || newer.value == null) {
                prefs.edit { remove("misses"); putLong("checked", now) }
            } else {
                // A file that does not match yet (uni-vpn.apk of the next release, uploaded just
                // before its uni-vpn-apk.json): again in 10 minutes, then 20, 40 and so on, at
                // most a day, so a release whose uni-vpn-apk.json never came is not downloaded
                // all day.
                val misses = prefs.getInt("misses", 0)
                prefs.edit { putInt("misses", misses + 1); putLong("checked", now - DAY + minOf(RETRY shl minOf(misses, 8), DAY)) }
            }
        }
    }

    /** The tap on "Update Uni VPN": the file (downloaded now on mobile data) to Android's installer. */
    fun start(context: Context) {
        if (!installing.compareAndSet(expect = false, update = true)) return
        val app = context.applicationContext
        taps.launch {
            val handed = runCatching { prepare(app)?.let { install(app, it) } == true }.getOrDefault(false)
            // Else the installer answers (UpdateReceiver).
            if (!handed) {
                // A file that did not match: offered again by the next check (with its back-off),
                // not downloaded again at every tap.
                val release = newer.value
                if (release != null && release.sha256 == mismatched) newer.value = null
                installing.value = false
            }
        }
    }

    /**
     * Android installs without asking only for an app the user allowed to install apps (Settings,
     * "Install unknown apps"; the first tap on Update walks through it). Until then Home asks for
     * it once ([askToAllow]), and [installUnattended] would only be refused.
     */
    fun mayInstall(context: Context): Boolean =
        // Android throws without REQUEST_INSTALL_PACKAGES, which the Play build does not declare.
        runCatching { context.packageManager.canRequestPackageInstalls() }.getOrDefault(false)

    /** Home's "Let Uni VPN update itself": where an update could go in without a tap, once allowed. */
    fun askToAllow(context: Context): Boolean =
        BuildConfig.SELF_UPDATE && Build.VERSION.SDK_INT >= 31 && prefs(context).getBoolean("automatic", true) && !mayInstall(context)

    /** Android's page with this app's "Install unknown apps" switch. */
    fun allowIntent(context: Context): Intent =
        Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, "package:${context.packageName}".toUri())

    /**
     * Android 12 and later: the version on offer, once it came down, goes to Android's installer
     * without a tap while the app is not on screen and the core ([status], null when it does not
     * answer) has no tunnel, connection or pairing in use (role model: Signal's download build).
     * Android puts it in without asking (setRequireUserAction in [install]) once installs from
     * this app are allowed ([mayInstall]), and BootReceiver turns the VPN back on; should Android
     * want to ask after all, the offer stays as it is (UpdateReceiver). Once a day per version.
     * Blocks: call off the main thread.
     */
    fun installUnattended(context: Context, status: JSONObject?) {
        if (!BuildConfig.SELF_UPDATE || BuildConfig.DEBUG) return
        val release = newer.value ?: return
        val prefs = prefs(context)
        val now = System.currentTimeMillis()
        if (!unattended(Build.VERSION.SDK_INT, shown.get() > 0, idle(status), prefs.getString("unattended", null), release.sha256, now - prefs.getLong("unattended_at", 0))) return
        if (!mayInstall(context)) return
        if (!installing.compareAndSet(expect = false, update = true)) return
        var handed = false
        try {
            synchronized(lock) {
                if (!prefs.getBoolean("automatic", true) || newer.value !== release) return
                val file = apk(context)
                // Only a file that is here already: on mobile data nothing comes down without a tap.
                if (file.length() != release.size) return
                prefs.edit { putString("unattended", release.sha256); putLong("unattended_at", now) }
                if (!sums(file, release) || !matches(context, file, release)) return
                handed = install(context.applicationContext, file, unattended = true)
            }
        } finally {
            // Else the installer answers (UpdateReceiver).
            if (!handed) installing.value = false
        }
    }

    /** [installUnattended]'s rule, apart from Android: [tried] is the version last tried, [sinceTried] milliseconds ago. */
    internal fun unattended(sdk: Int, onScreen: Boolean, idle: Boolean, tried: String?, sha256: String, sinceTried: Long): Boolean =
        sdk >= 31 && !onScreen && idle && (tried != sha256 || sinceTried !in 0 until DAY)

    /**
     * Nothing of the core's in use: no tunnel, no browser connection, no sign-in under way, no
     * device being added (a code that is done or ran out stays in status.json until its screen
     * closes). A core that does not answer is idle (a release whose core does not start on this
     * phone: the update may be what helps).
     */
    internal fun idle(status: JSONObject?): Boolean = status == null ||
        status.optString("state") !in BUSY && status.optInt("active_connections") == 0 &&
        !status.optBoolean("busy") && status.optJSONObject("sync")?.optJSONObject("pairing")?.optString("state") != "waiting"

    /** The checked file of the version on offer, downloading it now when needed; null when it does not come. */
    private fun prepare(context: Context): File? {
        synchronized(lock) {
            load(context)
            val release = newer.value ?: return null
            return download(context, release)
        }
    }

    private fun unmetered(context: Context): Boolean {
        val manager = context.getSystemService(ConnectivityManager::class.java) ?: return false
        // The app is outside its own VPN: this is the network it downloads over.
        val caps = manager.getNetworkCapabilities(manager.activeNetwork) ?: return false
        return caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)
    }

    private fun download(context: Context, release: ApkRelease): File? {
        val file = apk(context)
        if (!sums(file, release)) {
            file.parentFile?.mkdirs()
            val part = File(file.path + ".part")
            try {
                open(RELEASE + "uni-vpn.apk").use { input ->
                    part.outputStream().use { out -> copy(input, out, release.size) }
                }
            } catch (e: Exception) {
                Log.i(TAG, "update not downloaded: $e")
                part.delete()
                return null
            }
            if (!sums(part, release)) {
                Log.i(TAG, "update download does not match uni-vpn-apk.json")
                part.delete()
                mismatched = release.sha256
                return null
            }
            mismatched = null
            file.delete()
            if (!part.renameTo(file)) return null
        }
        if (matches(context, file, release)) return file
        // The release's own file, but Android would not put it over this copy (a build signed
        // with another key): not offered, and not downloaded again, until the next release.
        Log.i(TAG, "update is not for this copy")
        file.delete()
        newer.value = null
        prefs(context).edit { putString("refused", release.sha256); remove("release") }
        return null
    }

    private fun sums(file: File, release: ApkRelease) =
        file.length() == release.size && runCatching { sha256(file) }.getOrNull() == release.sha256

    /** At most [limit] bytes; more fails, as the file would not match anyway. */
    private fun copy(input: InputStream, out: java.io.OutputStream, limit: Long) {
        val buffer = ByteArray(64 * 1024)
        var total = 0L
        while (true) {
            val n = input.read(buffer)
            if (n < 0) return
            total += n
            if (total > limit) throw IOException("larger than uni-vpn-apk.json says")
            out.write(buffer, 0, n)
        }
    }

    private fun open(url: String): InputStream {
        val conn = URL(url).openConnection() as HttpURLConnection
        conn.connectTimeout = 15_000
        conn.readTimeout = 30_000
        conn.useCaches = false
        conn.setRequestProperty("User-Agent", "uni-vpn")
        if (conn.responseCode != 200) {
            conn.disconnect()
            throw IOException("HTTP ${conn.responseCode}")
        }
        return conn.inputStream
    }

    private fun fetch(url: String, limit: Int): ByteArray = open(url).use { input ->
        val out = java.io.ByteArrayOutputStream()
        copy(input, out, limit.toLong())
        out.toByteArray()
    }

    internal fun sha256(file: File): String {
        val digest = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buffer = ByteArray(64 * 1024)
            while (true) {
                val n = input.read(buffer)
                if (n < 0) break
                digest.update(buffer, 0, n)
            }
        }
        return digest.digest().joinToString("") { "%02x".format(it) }
    }

    /**
     * The file is this app at the version on offer, signed like this copy: Android would refuse
     * anything else after asking the user, so it is not offered.
     */
    internal fun matches(context: Context, file: File, release: ApkRelease): Boolean = try {
        sameApp(context, file, release)
    } catch (e: Exception) {
        Log.i(TAG, "update file not readable: $e")
        false
    }

    private fun sameApp(context: Context, file: File, release: ApkRelease): Boolean {
        val pm = context.packageManager
        // Android 10 reads an archive's signatures only with GET_SIGNATURES.
        @Suppress("DEPRECATION")
        val info = pm.getPackageArchiveInfo(file.path, PackageManager.GET_SIGNING_CERTIFICATES or PackageManager.GET_SIGNATURES) ?: return false
        if (info.packageName != context.packageName || info.longVersionCode != release.versionCode) return false
        @Suppress("DEPRECATION")
        val own = pm.getPackageInfo(context.packageName, PackageManager.GET_SIGNING_CERTIFICATES)
        val theirs = signers(info.signingInfo) ?: return false
        return theirs == signers(own.signingInfo)
    }

    private fun signers(info: SigningInfo?): Set<String>? =
        info?.apkContentsSigners?.map { it.toCharsString() }?.toSet()

    /**
     * Hands the file to Android's installer, which asks the user (UpdateReceiver), or with
     * [unattended] only puts it in where it need not ask. Blocks: call off the main thread.
     */
    fun install(context: Context, file: File, unattended: Boolean = false): Boolean {
        val installer = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(context.packageName)
            setSize(file.length())
            // Once this app installed itself, Android may update it without asking again.
            if (Build.VERSION.SDK_INT >= 31) setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
        }
        var id = -1
        return try {
            id = installer.createSession(params)
            installer.openSession(id).use { session ->
                file.inputStream().use { input ->
                    session.openWrite("uni-vpn.apk", 0, file.length()).use { out ->
                        input.copyTo(out)
                        session.fsync(out)
                    }
                }
                // Mutable: the installer adds its answer (EXTRA_STATUS) to it.
                val flags = PendingIntent.FLAG_UPDATE_CURRENT or (if (Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_MUTABLE else 0)
                val intent = Intent(context, UpdateReceiver::class.java).setPackage(context.packageName).putExtra(EXTRA_UNATTENDED, unattended)
                session.commit(PendingIntent.getBroadcast(context, id, intent, flags).intentSender)
            }
            true
        } catch (e: Exception) {
            Log.e(TAG, "update not handed to the installer", e)
            if (id >= 0) runCatching { installer.abandonSession(id) }
            false
        }
    }
}
