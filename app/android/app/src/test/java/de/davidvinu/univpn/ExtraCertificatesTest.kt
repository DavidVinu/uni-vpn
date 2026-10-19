package de.davidvinu.univpn

import java.io.File
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import org.junit.Assert.assertEquals
import org.junit.Test

/** The certificates Core appends to the phone's own: newer roots cross-signed by older ones (internal/certs). */
class ExtraCertificatesTest {
    @Test
    fun theAssetHoldsCrossCertificates() {
        // Each PEM block on its own, as OpenSSL and Go read the file: the comments around them stay out.
        val blocks = Regex("-----BEGIN CERTIFICATE-----[^-]+-----END CERTIFICATE-----")
            .findAll(File("src/main/assets/${Core.EXTRA_CERTIFICATES}").readText()).map { it.value }.toList()
        val factory = CertificateFactory.getInstance("X.509")
        val certs = blocks.map { factory.generateCertificate(it.byteInputStream()) as X509Certificate }
        assertEquals(
            listOf(
                "HARICA TLS RSA Root CA 2021",
                "HARICA TLS ECC Root CA 2021",
                "emSign Root TLS CA - G1",
                "DigiCert TLS RSA4096 Root G5",
                "DigiCert TLS ECC P384 Root G5",
                "GlobalSign Root R46",
                "GlobalSign Root E46",
            ),
            certs.map { cn(it.subjectX500Principal.name) },
        )
        assertEquals(
            listOf(
                "Hellenic Academic and Research Institutions RootCA 2015",
                "Hellenic Academic and Research Institutions ECC RootCA 2015",
                "emSign Root CA - G1",
                "DigiCert Global Root CA",
                "DigiCert Global Root G3",
                "GlobalSign",
                "GlobalSign",
            ),
            certs.map { cn(it.issuerX500Principal.name) },
        )
        for (cert in certs) assert(cert.basicConstraints >= 0) // CA certificates
    }

    private fun cn(name: String) = name.split(",").first { it.startsWith("CN=") }.removePrefix("CN=")
}
