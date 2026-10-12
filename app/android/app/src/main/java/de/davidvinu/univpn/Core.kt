package de.davidvinu.univpn

import android.content.Context
import android.os.Build
import android.provider.Settings
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import android.util.Log
import androidx.core.content.edit
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.security.KeyStore
import java.security.SecureRandom
import java.security.cert.X509Certificate
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

    /** Starts the core unless it runs. Cheap to call often. */
    fun ensureRunning() {
        synchronized(lock) {
            if (process?.isAlive == true) return
            // A core that just exited (for example because another one holds the lock) is not
            // restarted in a tight loop.
            if (System.currentTimeMillis() - started < 1000) return
            started = System.currentTimeMillis()
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
        val ca = writeCertificates()
        val builder = ProcessBuilder("$native/libunivpn.so", "daemon")
            .directory(files)
            .redirectErrorStream(true)
            .redirectOutput(ProcessBuilder.Redirect.appendTo(File(state, "core-output.log")))
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
        val keystoreKey = keystoreKey()
        prefs.getString("wrapped", null)?.let { stored ->
            try {
                val raw = Base64.decode(stored, Base64.NO_WRAP)
                val cipher = Cipher.getInstance("AES/GCM/NoPadding")
                cipher.init(Cipher.DECRYPT_MODE, keystoreKey, GCMParameterSpec(128, raw, 0, 12))
                return cipher.doFinal(raw, 12, raw.size - 12).toHex()
            } catch (e: Exception) {
                // The Keystore lost its key (restored phone): the core asks for the secrets again.
                Log.e(TAG, "secrets key unreadable, making a new one", e)
            }
        }
        val key = ByteArray(32).also { SecureRandom().nextBytes(it) }
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, keystoreKey)
        val wrapped = cipher.iv + cipher.doFinal(key)
        prefs.edit { putString("wrapped", Base64.encodeToString(wrapped, Base64.NO_WRAP)) }
        return key.toHex()
    }

    private fun keystoreKey(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        generator.init(
            KeyGenParameterSpec.Builder(KEY_ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return generator.generateKey()
    }

    // --- Local API -------------------------------------------------------------------------

    class Reply(val status: Int, val json: JSONObject?, val text: String) {
        val ok get() = status in 200..299
    }

    /** One request to the core; status 0 when it is not reachable. Call off the main thread. */
    fun call(method: String, path: String, body: JSONObject? = null, timeoutMs: Int = 10_000): Reply {
        return try {
            val conn = URL("http://127.0.0.1:$port$path").openConnection() as HttpURLConnection
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

        private fun randomHex(bytes: Int) = ByteArray(bytes).also { SecureRandom().nextBytes(it) }.toHex()
        private fun ByteArray.toHex() = joinToString("") { "%02x".format(it) }
    }
}
