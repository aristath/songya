package songya.app

import com.google.zxing.BarcodeFormat
import com.google.zxing.qrcode.QRCodeWriter
import java.net.URLEncoder
import org.junit.Assert.*
import org.junit.Test

class PairingTest {
    private val now = 1234000L
    private fun qr(origin: String = "https://songya.example", expires: Long = now + 300000) =
        "songya://pair?url=${URLEncoder.encode(origin,"UTF-8")}&token=${"a".repeat(43)}&exp=$expires"
    @Test fun acceptsOnlyThePublicOriginAndExpiringOneUseToken() {
        val pairing = requireNotNull(Pairing.parse(qr(), now))
        assertEquals(Credentials("https://songya.example"), pairing.credentials)
        assertEquals(now + 300000, pairing.expiresAt)
        assertNull(Pairing.parse(qr(expires = now), now))
        assertNull(Pairing.parse(qr(expires = now - 1), now))
        assertNull(Pairing.parse(qr() + "&bearer=forbidden", now))
        assertNull(Pairing.parse(qr() + "&token=${"b".repeat(43)}", now))
    }
    @Test fun rejectsInsecureOrAmbiguousOrigins() {
        for (origin in listOf("http://songya.example","https://user:password@songya.example","https://songya.example/path","https://songya.example?token=x","https://songya.example#fragment","https://songya.example:0")) {
            assertNull(origin, Pairing.parse(qr(origin), now))
        }
    }
    @Test fun canonicalizesTlsPortsAndHostnameCase() {
        assertEquals("https://songya.example", Pairing.parse(qr("https://SONGYA.EXAMPLE:443/"), now)?.credentials?.origin)
        assertEquals("https://localhost:44444", Pairing.parse(qr("https://localhost:44444"), now)?.credentials?.origin)
    }
    @Test fun privilegesAreRestrictedToTheExactOrigin() {
        val target = Credentials("https://songya.example")
        assertTrue(target.owns("https://songya.example/?chat=123"))
        for (url in listOf("https://songya.example.evil.test/","https://evil.test/?https://songya.example","https://songya.example:444/","http://songya.example/","file:///etc/passwd","data:text/html,hello","https://user@songya.example/")) assertFalse(url,target.owns(url))
    }
    @Test fun rejectsUnscopedNativePaths() {
        for (path in listOf("https://other.example","//other.example/","relative")) {
            assertThrows(IllegalArgumentException::class.java) { Credentials("https://songya.example").url(path) }
        }
    }
    @Test fun cameraDecoderReadsTheActualGeneratedQrPixels() {
        val raw = qr()
        val matrix = QRCodeWriter().encode(raw, BarcodeFormat.QR_CODE, 600, 600)
        val luminance = ByteArray(matrix.width * matrix.height) { index ->
            if (matrix[index % matrix.width, index / matrix.width]) 0 else 255.toByte()
        }
        assertEquals(raw, QrDecoder.decode(luminance,matrix.width,matrix.height))
        assertNull(QrDecoder.decode(ByteArray(640*480) {255.toByte()},640,480))
    }
}
