package de.davidvinu.univpn

import java.io.File
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import org.junit.Assert.assertEquals
import org.junit.Test

/** The certificates Core appends to the phone's own: HARICA's 2021 roots, cross-signed in 2015. */
class ExtraCertificatesTest {
    @Test
    fun theAssetHoldsHaricasCrossCertificates() {
        // Each PEM block on its own, as OpenSSL and Go read the file: the comments around them stay out.
        val blocks = Regex("-----BEGIN CERTIFICATE-----[^-]+-----END CERTIFICATE-----")
            .findAll(File("src/main/assets/${Core.EXTRA_CERTIFICATES}").readText()).map { it.value }.toList()
        val factory = CertificateFactory.getInstance("X.509")
        val certs = blocks.map { factory.generateCertificate(it.byteInputStream()) as X509Certificate }
        assertEquals(
            listOf("HARICA TLS RSA Root CA 2021", "HARICA TLS ECC Root CA 2021"),
            certs.map { cn(it.subjectX500Principal.name) },
        )
        assertEquals(
            listOf(
                "Hellenic Academic and Research Institutions RootCA 2015",
                "Hellenic Academic and Research Institutions ECC RootCA 2015",
            ),
            certs.map { cn(it.issuerX500Principal.name) },
        )
        for (cert in certs) assert(cert.basicConstraints >= 0) // CA certificates
    }

    private fun cn(name: String) = name.split(",").first { it.startsWith("CN=") }.removePrefix("CN=")
}
