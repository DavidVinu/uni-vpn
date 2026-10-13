package de.davidvinu.univpn.ui

import android.graphics.Bitmap
import androidx.core.graphics.createBitmap
import androidx.core.graphics.set
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.outlined.QrCodeScanner
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.FilterQuality
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import com.google.zxing.qrcode.decoder.ErrorCorrectionLevel
import de.davidvinu.univpn.AppModel
import de.davidvinu.univpn.MainActivity
import kotlinx.coroutines.GlobalScope
import kotlinx.coroutines.launch

/**
 * Add a device. Role models: Signal "Link a device" and WhatsApp linked devices: one device
 * shows a QR code with the code under it, the other scans it or types the code. The phone can
 * also scan the code a computer shows under "Use settings from another device".
 */
@Composable
fun PairScreen(model: AppModel, mode: String, activity: MainActivity) {
    val status = model.status ?: return
    val context = LocalContext.current
    // Saved, so turning the phone keeps the code the other device may be typing.
    var shown by rememberSaveable { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var typing by rememberSaveable { mutableStateOf(false) }
    var typed by rememberSaveable { mutableStateOf("") }
    var done by rememberSaveable { mutableStateOf(false) }
    var attempt by rememberSaveable { mutableIntStateOf(0) }
    // The attempt whose code shows.
    var offered by rememberSaveable { mutableIntStateOf(-1) }

    LaunchedEffect(attempt) {
        if (offered == attempt && shown != null) return@LaunchedEffect
        error = null
        shown = null
        val result = model.post("/api/sync/$mode", timeoutMs = 30_000)
        if (result.ok) {
            shown = result.json?.optJSONObject("pairing")?.optString("code")
            offered = attempt
        } else {
            error = result.error
        }
    }
    @OptIn(kotlinx.coroutines.DelicateCoroutinesApi::class)
    DisposableEffect(Unit) {
        onDispose {
            // Turning the phone builds the screen anew: the pairing goes on.
            if (!activity.isChangingConfigurations) {
                GlobalScope.launch { model.core.post("/api/sync/cancel") }
                model.pairResult = null // not for the next visit
            }
        }
    }
    val pairing = status.optJSONObject("sync")?.optJSONObject("pairing")
    val pairingState = if (pairing != null && pairing.optString("code") == shown) pairing.optString("state") else ""
    LaunchedEffect(pairingState) {
        when (pairingState) {
            "done" -> done = true
            "expired" -> error = model.t("sync.expired")
            "failed" -> error = model.t("sync.offline")
        }
    }
    // A typed or scanned code: its answer comes through the model, also after turning the phone.
    LaunchedEffect(model.pairResult) {
        val result = model.pairResult ?: return@LaunchedEffect
        model.pairResult = null
        if (result.ok) done = true else error = result.error
    }

    Column(Modifier.fillMaxSize().navigationBarsPadding().imePadding().verticalScroll(rememberScrollState())) {
        Bar(model, model.t(if (mode == "offer") "sync.add" else "sync.use_other"))
        Column(
            Modifier.fillMaxWidth().padding(24.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            when {
                done -> {
                    Icon(Icons.Filled.CheckCircle, contentDescription = null, tint = Ok, modifier = Modifier.size(72.dp))
                    Text(model.t("sync.done"), style = MaterialTheme.typography.headlineSmall)
                    Button(onClick = { model.back() }) { Text(model.t("app.done")) }
                }
                typing -> {
                    OutlinedTextField(
                        value = typed,
                        onValueChange = { typed = it.uppercase() },
                        label = { Text(model.t("sync.code")) },
                        placeholder = { Text("XXXX-XXXX-XXXX") },
                        singleLine = true,
                        keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Characters, autoCorrectEnabled = false),
                        modifier = Modifier.fillMaxWidth(),
                    )
                    if (model.pairBusy) CircularProgressIndicator()
                    Button(enabled = !model.pairBusy && typed.isNotBlank(), onClick = {
                        error = null
                        model.enterPairCode(typed, inSetup = false)
                    }) { Text(model.t("app.next")) }
                }
                else -> {
                    val code = shown
                    if (code == null) {
                        if (error == null) CircularProgressIndicator()
                    } else {
                        val qr = remember(code) { qrBitmap("UNIVPN:" + code.replace("-", "")) }
                        Image(
                            qr.asImageBitmap(),
                            contentDescription = code,
                            filterQuality = FilterQuality.None,
                            modifier = Modifier.size(220.dp).clip(RoundedCornerShape(12.dp)).background(Color.White),
                        )
                        Text(code, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.headlineSmall)
                        Text(
                            model.t(if (mode == "offer") "sync.show_offer" else "sync.show_request"),
                            textAlign = TextAlign.Center,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                    TextButton(onClick = {
                        scanPairing(context, model) { error = it }
                    }) {
                        Icon(Icons.Outlined.QrCodeScanner, contentDescription = null)
                        Text("  " + model.t("phone.scan_code"))
                    }
                    TextButton(onClick = { typing = true; error = null }) { Text(model.t("sync.enter")) }
                }
            }
            ErrorText(error)
            if (error != null && !typing && !done) {
                TextButton(onClick = { attempt++ }) { Text(model.t("sync.new_code")) }
            }
        }
    }
}

/** Black modules on white with the standard quiet zone, error correction M (docs/sync.md). */
private fun qrBitmap(text: String): Bitmap {
    val matrix = QRCodeWriter().encode(
        text, BarcodeFormat.QR_CODE, 0, 0,
        mapOf(EncodeHintType.ERROR_CORRECTION to ErrorCorrectionLevel.M, EncodeHintType.MARGIN to 4),
    )
    val bitmap = createBitmap(matrix.width, matrix.height)
    for (x in 0 until matrix.width) {
        for (y in 0 until matrix.height) {
            bitmap[x, y] = if (matrix.get(x, y)) android.graphics.Color.BLACK else android.graphics.Color.WHITE
        }
    }
    return bitmap
}
