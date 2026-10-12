package de.davidvinu.univpn

import android.content.Context
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.codescanner.GmsBarcodeScannerOptions
import com.google.mlkit.vision.codescanner.GmsBarcodeScanning

/**
 * Google's code scanner: the system's scanner opens, no camera permission is asked. When it
 * cannot open (without Google Play services, or while it still downloads) [onFailed] lets the
 * screen say so; its typing field stays the way in. Cancelled, the result is null.
 */
object Scanner {
    fun scanQr(context: Context, onFailed: () -> Unit = {}, onResult: (String?) -> Unit) {
        val options = GmsBarcodeScannerOptions.Builder()
            .setBarcodeFormats(Barcode.FORMAT_QR_CODE)
            .enableAutoZoom()
            .build()
        try {
            GmsBarcodeScanning.getClient(context, options).startScan()
                .addOnSuccessListener { onResult(it.rawValue) }
                .addOnCanceledListener { onResult(null) }
                .addOnFailureListener { onFailed() }
        } catch (_: Exception) {
            onFailed()
        }
    }
}
