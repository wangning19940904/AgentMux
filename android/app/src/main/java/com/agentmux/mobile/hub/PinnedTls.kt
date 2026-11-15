package com.agentmux.mobile.hub

import java.security.MessageDigest
import java.security.SecureRandom
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager
import okhttp3.OkHttpClient

/**
 * A hub without a domain presents its own self-signed certificate. Machines
 * that pair with it trust exactly that certificate by its SHA-256
 * fingerprint (link.Fingerprint in the Go code); the phone does the same.
 */
object PinnedTls {

    /** SHA-256 of the certificate's DER encoding, in lowercase hex. */
    fun fingerprint(certificate: X509Certificate): String =
        MessageDigest.getInstance("SHA-256").digest(certificate.encoded)
            .joinToString("") { "%02x".format(it) }

    /**
     * Makes [builder] trust only the certificate with [fingerprint]. A null
     * fingerprint leaves the system's CA checks in place.
     */
    fun pin(builder: OkHttpClient.Builder, fingerprint: String?): OkHttpClient.Builder {
        if (fingerprint == null) return builder
        val trustManager = FingerprintTrustManager(fingerprint)
        val context = SSLContext.getInstance("TLS")
        context.init(null, arrayOf(trustManager), SecureRandom())
        return builder
            .sslSocketFactory(context.socketFactory, trustManager)
            // The certificate is pinned outright, and the hub is often
            // reached by an IP address its certificate does not name.
            .hostnameVerifier { _, _ -> true }
    }
}

/** Trusts one leaf certificate, named by its SHA-256 fingerprint. */
class FingerprintTrustManager(expected: String) : X509TrustManager {
    private val expected = HubLink.normalizeFingerprint(expected).toByteArray()

    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        val leaf = chain?.firstOrNull() ?: throw CertificateException("The hub sent no certificate.")
        val actual = PinnedTls.fingerprint(leaf).toByteArray()
        if (!MessageDigest.isEqual(expected, actual)) {
            throw CertificateMismatchException()
        }
    }

    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        throw CertificateException("Client certificates are not used.")
    }

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}

/** The server is not the hub this phone was paired with. */
class CertificateMismatchException :
    CertificateException("The hub's certificate does not match the one in its link.")
