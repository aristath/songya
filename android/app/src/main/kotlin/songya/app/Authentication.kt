package songya.app

import android.os.Build
import java.io.IOException
import java.util.concurrent.TimeUnit
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject

class DeviceRevoked : IOException("This device must pair again")
class Authentication(private val vault: DeviceVault) {
    private val client = OkHttpClient.Builder()
        .followRedirects(false).followSslRedirects(false)
        .connectTimeout(15, TimeUnit.SECONDS).readTimeout(30, TimeUnit.SECONDS).build()
    fun connect(credentials: Credentials, pairing: Pairing? = null): String {
        require(pairing == null || pairing.expiresAt > System.currentTimeMillis()) { "Pairing code expired" }
        val body = JSONObject().put("publicKey", vault.publicKey(credentials.origin, create = pairing != null)).put("label", Build.MODEL)
        if (pairing != null) body.put("pairingToken", pairing.token)
        val challenge = post(credentials, "/api/mobile/challenge", body)
        val signed = JSONObject().put("id", challenge.getString("id")).put("signature", vault.sign(credentials.origin, challenge.getString("message")))
        val authenticated = post(credentials, "/api/mobile/authenticate", signed)
        val handoff = authenticated.getString("handoff")
        require(handoff.matches(Regex("[A-Za-z0-9_-]{43}")))
        require(authenticated.getLong("expiresAt") > System.currentTimeMillis())
        return handoff
    }
    /** Redeem a private handoff to revoke even when WebView's cookie has expired. */
    fun revoke(credentials: Credentials) {
        val cookie = sessionCookie(credentials)
        client.newCall(Request.Builder().url(credentials.url("/api/logout"))
            .header("Origin", credentials.origin).header("X-Songya", "1").header("Cookie", cookie)
            .post(ByteArray(0).toRequestBody()).build()).execute().use { response ->
            if (response.code == 401) throw DeviceRevoked()
            check(response.isSuccessful) { "Revocation failed" }
        }
    }
    private fun sessionCookie(credentials: Credentials): String {
        val handoff = connect(credentials)
        val session = Request.Builder().url(credentials.url("/api/mobile/session"))
            .post(("handoff=" + java.net.URLEncoder.encode(handoff, "UTF-8"))
                .toRequestBody("application/x-www-form-urlencoded".toMediaType())).build()
        return client.newCall(session).execute().use { response ->
            if (response.code == 401) throw DeviceRevoked()
            check(response.code == 303) { "Browser handoff failed" }
            response.headers.values("Set-Cookie").map { it.substringBefore(';') }
                .single { it.startsWith("songya_session=") }
        }
    }
    private fun post(credentials: Credentials, path: String, data: JSONObject): JSONObject {
        val request = Request.Builder().url(credentials.url(path)).header("X-Songya", "1")
            .post(data.toString().toRequestBody("application/json; charset=utf-8".toMediaType())).build()
        client.newCall(request).execute().use { response ->
            if (response.code == 401) throw DeviceRevoked()
            if (!response.isSuccessful) throw IOException("Songya connection failed (${response.code})")
            val body = requireNotNull(response.body)
            require(body.contentLength() <= 8192)
            val source = body.source()
            source.request(8193)
            require(source.buffer.size <= 8192)
            return JSONObject(source.readUtf8())
        }
    }
}
