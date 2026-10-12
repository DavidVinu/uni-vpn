package de.davidvinu.univpn

import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.VpnService
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

/**
 * The phase 0 questions of the mobile plan that an emulator can answer: the core and
 * openconnect run from the app's native library folder, reach a real gateway with the phone's
 * certificates, keep secrets encrypted, and the VPN hands the phone the proxy rule.
 * CI grants the VPN permission beforehand (appops ACTIVATE_VPN), see ci.yml.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class PhoneTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private val core = context.core

    private fun status(): JSONObject {
        core.ensureRunning()
        repeat(100) {
            core.get("/status.json").json?.let { return it }
            Thread.sleep(200)
        }
        throw AssertionError("the core did not answer")
    }

    private fun waitFor(seconds: Int, what: (JSONObject) -> Boolean): JSONObject {
        var last = status()
        repeat(seconds * 2) {
            last = status()
            if (what(last)) return last
            Thread.sleep(500)
        }
        throw AssertionError("timed out, last status: $last")
    }

    private fun plain(path: String): Int {
        val conn = URL("http://127.0.0.1:${core.port}$path").openConnection() as HttpURLConnection
        return conn.responseCode.also { conn.disconnect() }
    }

    @Test
    fun a_coreRunsAndOnlyThisAppMayUseItsApi() {
        val s = status()
        assertEquals("android", s.getString("platform"))
        assertEquals(403, plain("/status.json"))
        assertEquals(200, plain("/proxy.pac"))
        assertTrue(core.get("/locales.json").ok)
    }

    @Test
    fun b_openconnectReachesTheGatewayAndSecretsStayEncrypted() {
        val password = "uni-vpn-ci-not-a-password"
        val body = JSONObject().put("user", "univpnci").put("password", password)
            .put("secret", "JBSWY3DPEHPK3PXP").put("university", "heidelberg")
        val reply = core.post("/api/setup", body, 60_000)
        assertTrue(reply.text, reply.ok)
        // A made-up account: the university must say no. Getting that far means openconnect
        // ran, resolved the gateway, trusted its certificate and spoke AnyConnect.
        val s = waitFor(120) { it.getString("state") in setOf("auth_failed", "connected") }
        assertEquals(s.toString(), "auth_failed", s.getString("state"))
        val secrets = File(context.filesDir, "config/uni-vpn/secrets.json")
        assertTrue(secrets.exists())
        assertFalse(secrets.readText().contains(password))
        assertFalse(s.getBoolean("setup_needed"))
    }

    @Test
    fun c_theVpnCarriesTheProxyRule() {
        assertNull("CI grants the VPN permission", VpnService.prepare(context))
        UniVpnService.start(context)
        val manager = context.getSystemService(ConnectivityManager::class.java)
        var pac: String? = null
        repeat(40) {
            @Suppress("DEPRECATION")
            val vpn = manager.allNetworks.firstOrNull {
                manager.getNetworkCapabilities(it)?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true
            }
            pac = vpn?.let { manager.getLinkProperties(it)?.httpProxy?.pacFileUrl?.toString() }
            if (pac != null) return@repeat
            Thread.sleep(500)
        }
        assertNotNull("no VPN with a proxy rule", pac)
        assertTrue(pac!!, pac!!.startsWith("http://127.0.0.1:${core.port}/proxy.pac"))
        assertTrue(UniVpnService.isOn.value)
        UniVpnService.stop(context)
        repeat(20) {
            if (!UniVpnService.isOn.value) return
            Thread.sleep(250)
        }
        throw AssertionError("the VPN did not stop")
    }
}
