package de.davidvinu.univpn

import android.content.Context
import android.os.Build
import android.os.SystemClock
import android.provider.Settings
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
import android.util.Base64
import android.util.Log
import androidx.core.content.edit
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.Proxy
import java.net.URL
import java.security.KeyStore
import java.security.SecureRandom
import java.security.cert.X509Certificate
import javax.crypto.AEADBadTagException
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * The Go core (cmd/uni-vpn, shipped as libunivpn.so) running as a child process, and the way to
 * its local API. The app is a thin shell like the desktop apps: everything else lives in the core.
 */
class Core(private val context: Context) {
    val port = 1081
    private val prefs = context.getSharedPreferences("core", Context.MODE_PRIVATE)
    private val token: String = prefs.getString("token", null) ?: randomHex(32).also {
        prefs.edit { putString("token", it) }
    }
    private val lock = Object()

    /** Setup finished, as last seen in status.json (the tile reads it without asking the core). */
    var setupDone: Boolean
        get() = prefs.getBoolean("setup_done", false)
        set(value) {
            if (value != setupDone) prefs.edit { putBoolean("setup_done", value) }
        }
    private var process: Process? = null
    private var started = 0L

    /** Exists while the core this app started holds its ports (daemon.Options.ReadyFile). */
    private val readyFile = File(context.filesDir, "state/uni-vpn/api-ready")

    /**
     * Another app held the core's port, so the core ended (EXIT_PORTS_TAKEN): the app says so
     * instead of waiting for an answer that cannot come.
     */
    @Volatile var portTaken = false
        private set

    /** Starts the core unless it runs. Cheap to call often. */
    fun ensureRunning() {
        synchronized(lock) {
            val last = process
            if (last?.isAlive == true) return
            if (last != null) portTaken = last.exitValue() == EXIT_PORTS_TAKEN
            // A core that just exited (for example because another one holds the lock) is not
            // restarted in a tight loop. Another app may hold the port all day: then only every
            // half minute, which still finds the port once that app lets go.
            val pause = if (portTaken) 30_000L else 1_000L
            if (SystemClock.elapsedRealtime() - started < pause) return  // not the clock: it can go back
            started = SystemClock.elapsedRealtime()
            // Only the core started now may say it holds the ports.
            readyFile.delete()
            process = try {
                start()
            } catch (e: Exception) {
                Log.e(TAG, "core did not start", e)
                null
            }
        }
    }

    /** "Repair" on a phone: start the core afresh. */
    fun restart() {
        synchronized(lock) {
            process?.destroy()
            process?.waitForQuietly()
            process = null
            started = 0
        }
        ensureRunning()
    }

    private fun Process.waitForQuietly() = try {
        waitFor(5, java.util.concurrent.TimeUnit.SECONDS)
    } catch (_: InterruptedException) {
        false
    }

    private fun start(): Process {
        val native = context.applicationInfo.nativeLibraryDir
        val files = context.filesDir
        val state = File(files, "state").apply { mkdirs() }
        val output = File(state, "core-output.log")
        if (output.length() > 1_000_000) output.renameTo(File(state, "core-output.log.1"))
        val ca = writeCertificates()
        val builder = ProcessBuilder("$native/libunivpn.so", "daemon")
            .directory(files)
            .redirectErrorStream(true)
            .redirectOutput(ProcessBuilder.Redirect.appendTo(output))
        builder.environment().apply {
            put("HOME", files.path)
            put("XDG_CONFIG_HOME", File(files, "config").path)
            put("XDG_STATE_HOME", state.path)
            put("XDG_DATA_HOME", File(files, "data").path)
            put("TMPDIR", context.cacheDir.path)
            put("UNI_VPN_NATIVE_DIR", native)
            if (ca != null) {
                put("UNI_VPN_CAFILE", ca.path)
                // The core's own checks (Go) read it too, whatever Android version keeps its
                // certificates where.
                put("SSL_CERT_FILE", ca.path)
            }
            put("UNI_VPN_API_TOKEN", token)
            put("UNI_VPN_SECRETS_KEY", secretsKey())
            put("UNI_VPN_DEVICE_NAME", deviceName())
        }
        return builder.start()
    }

    private fun deviceName(): String =
        Settings.Global.getString(context.contentResolver, Settings.Global.DEVICE_NAME)?.takeIf { it.isNotBlank() }
            ?: Build.MODEL

    /**
     * The phone's trusted certificates as one PEM file: openconnect's OpenSSL cannot read
     * Android's certificate folder (its file names use an older hash).
     */
    private fun writeCertificates(): File? = try {
        val store = KeyStore.getInstance("AndroidCAStore").apply { load(null) }
        val pem = StringBuilder()
        for (alias in store.aliases()) {
            if (!alias.startsWith("system:")) continue // like apps: user-added ones are not trusted
            val cert = store.getCertificate(alias) as? X509Certificate ?: continue
            pem.append("-----BEGIN CERTIFICATE-----\n")
            pem.append(Base64.encodeToString(cert.encoded, Base64.DEFAULT))
            pem.append("-----END CERTIFICATE-----\n")
        }
        // Android 13 and older lack HARICA's 2021 roots, where the GEANT certificates of many
        // university gateways end: HARICA's cross-certificates from its 2015 roots, which they have.
        pem.append(context.assets.open(EXTRA_CERTIFICATES).bufferedReader().use { it.readText() })
        File(context.filesDir, "ca.pem").apply { writeText(pem.toString()) }
    } catch (e: Exception) {
        Log.e(TAG, "certificates not written", e)
        null
    }

    /**
     * The key the core encrypts the password and setup key with (secrets.json), wrapped by a key
     * in the Android Keystore that never leaves the phone's hardware. Not tied to unlocking, so
     * the VPN can reconnect in a pocket.
     */
    private fun secretsKey(): String {
        val (keystoreKey, fresh) = keystoreKey()
        val stored = prefs.getString("wrapped", null)
        if (stored != null && !fresh) {
            try {
                val raw = Base64.decode(stored, Base64.NO_WRAP)
                val cipher = Cipher.getInstance("AES/GCM/NoPadding")
                cipher.init(Cipher.DECRYPT_MODE, keystoreKey, GCMParameterSpec(128, raw, 0, 12))
                return cipher.doFinal(raw, 12, raw.size - 12).toHex()
            } catch (e: KeyPermanentlyInvalidatedException) {
                Log.e(TAG, "secrets key gone, making a new one", e)
            } catch (e: AEADBadTagException) {
                Log.e(TAG, "secrets key unreadable, making a new one", e)
            }
            // Anything else (the Keystore not ready yet, for example) throws: the core is not
            // started and the next try reads the same key.
        }
        // A new key cannot read the old secrets: drop them, so the core asks for them again
        // instead of reporting a broken store.
        File(File(context.filesDir, "config"), "uni-vpn/secrets.json").delete()
        val key = ByteArray(32).also { SecureRandom().nextBytes(it) }
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, keystoreKey)
        val wrapped = cipher.iv + cipher.doFinal(key)
        prefs.edit(commit = true) { putString("wrapped", Base64.encodeToString(wrapped, Base64.NO_WRAP)) }
        return key.toHex()
    }

    /** The Keystore key, and whether it was just made (so nothing wrapped before can be read). */
    private fun keystoreKey(): Pair<SecretKey, Boolean> {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it to false }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        generator.init(
            KeyGenParameterSpec.Builder(KEY_ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return generator.generateKey() to true
    }

    // --- Local API -------------------------------------------------------------------------

    class Reply(val status: Int, val json: JSONObject?, val text: String) {
        val ok get() = status in 200..299
    }

    /**
     * The core this app started runs and holds its ports. Any app can listen on 127.0.0.1 first,
     * and requests carry the token and the password: they go only to our own core.
     */
    fun ready(): Boolean {
        val ours = synchronized(lock) { process?.isAlive == true } && readyFile.exists()
        if (ours) portTaken = false
        return ours
    }

    /** One request to the core; status 0 when it is not reachable. Call off the main thread. */
    fun call(method: String, path: String, body: JSONObject? = null, timeoutMs: Int = 10_000): Reply {
        if (!ready()) {
            ensureRunning()
            return Reply(0, null, "the core is not ready")
        }
        return try {
            // Straight to the core: a Wi-Fi proxy with an empty bypass list would get 127.0.0.1 too.
            val conn = URL("http://127.0.0.1:$port$path").openConnection(Proxy.NO_PROXY) as HttpURLConnection
            conn.requestMethod = method
            conn.connectTimeout = 2_000
            conn.readTimeout = timeoutMs
            conn.useCaches = false
            conn.setRequestProperty("X-Uni-VPN", "1")
            conn.setRequestProperty("X-Uni-VPN-Token", token)
            if (method == "POST") {
                conn.doOutput = true
                conn.setRequestProperty("Content-Type", "application/json")
                conn.outputStream.use { it.write((body ?: JSONObject()).toString().toByteArray()) }
            }
            val status = conn.responseCode
            val stream = if (status >= 400) conn.errorStream else conn.inputStream
            val text = stream?.use { it.readBytes().toString(Charsets.UTF_8) } ?: ""
            conn.disconnect()
            Reply(status, runCatching { JSONObject(text) }.getOrNull(), text)
        } catch (e: Exception) {
            ensureRunning()
            Reply(0, null, e.toString())
        }
    }

    fun get(path: String) = call("GET", path)
    fun post(path: String, body: JSONObject? = null, timeoutMs: Int = 10_000) = call("POST", path, body, timeoutMs)

    fun pacUrl(version: String) = "http://127.0.0.1:$port/proxy.pac?v=$version"

    companion object {
        private const val TAG = "uni-vpn"
        private const val KEY_ALIAS = "uni-vpn-secrets"
        /** The core's exit code when another app holds its port (cmd/uni-vpn exitPortsTaken). */
        private const val EXIT_PORTS_TAKEN = 3
        /** Certificates the phone may lack, appended to its own (assets/extra-ca.pem). */
        internal const val EXTRA_CERTIFICATES = "extra-ca.pem"

        private fun randomHex(bytes: Int) = ByteArray(bytes).also { SecureRandom().nextBytes(it) }.toHex()
        private fun ByteArray.toHex() = joinToString("") { "%02x".format(it) }
    }
}
