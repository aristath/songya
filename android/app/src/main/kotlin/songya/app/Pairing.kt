package songya.app

import java.net.URI
import java.net.URLDecoder

/** Only a Songya origin persists here; the non-exportable device key lives in Android Keystore. */
data class Credentials(val origin: String) {
    fun url(path: String): String {
        require(path.startsWith("/") && !path.startsWith("//"))
        return origin + path
    }
    fun owns(url: String): Boolean = runCatching { normalizeOrigin(URI(url)) == origin }.getOrDefault(false)
}
data class Pairing(val credentials: Credentials, val token: String, val expiresAt: Long) {
    companion object {
        fun parse(raw: String, now: Long = System.currentTimeMillis()): Pairing? = runCatching {
            require(raw.length <= 4096 && raw.startsWith("songya://pair?"))
            val fields = linkedMapOf<String, String>()
            for (entry in raw.substringAfter('?').split('&')) {
                require(entry.contains('='))
                val key = URLDecoder.decode(entry.substringBefore('='), "UTF-8")
                val value = URLDecoder.decode(entry.substringAfter('='), "UTF-8")
                require(fields.put(key, value) == null)
            }
            require(fields.keys == setOf("url", "token", "exp"))
            val uri = URI(fields.getValue("url"))
            require(uri.rawPath.isNullOrEmpty() || uri.rawPath == "/")
            require(uri.rawQuery == null && uri.rawFragment == null)
            val origin = normalizeOrigin(uri)
            val token = fields.getValue("token")
            require(token.matches(Regex("[A-Za-z0-9_-]{43}")))
            val expires = fields.getValue("exp").toLong()
            require(expires > now)
            Pairing(Credentials(origin), token, expires)
        }.getOrNull()
    }
}
internal fun normalizeOrigin(uri: URI): String {
    require(uri.scheme.equals("https", ignoreCase = true) && uri.rawUserInfo == null)
    val host = requireNotNull(uri.host).lowercase()
    require(host.isNotBlank() && (uri.port == -1 || uri.port in 1..65535))
    return URI("https", null, host, if (uri.port == 443) -1 else uri.port, null, null, null).toASCIIString()
}
