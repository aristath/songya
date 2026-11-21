package songya.app

import android.content.Context
import android.net.Uri
import android.util.Base64
import android.webkit.WebView
import java.io.IOException
import java.util.UUID
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import org.json.JSONTokener
import kotlin.coroutines.resume

/** Only writes to a destination explicitly chosen in Android's picker. No JS/native bridge. */
internal class Downloads(private val context: Context) {
    private val client = OkHttpClient.Builder().followRedirects(false).followSslRedirects(false).build()
    suspend fun save(url: String, target: Credentials, destination: Uri, cookie: String, browser: WebView?) {
        if (target.owns(url)) {
            withContext(Dispatchers.IO) {
                client.newCall(Request.Builder().url(url).header("Cookie", cookie).build()).execute().use { response ->
                    check(response.isSuccessful) { "Download failed" }
                    requireNotNull(context.contentResolver.openOutputStream(destination, "wt")).use { output ->
                        requireNotNull(response.body).byteStream().use { it.copyTo(output) }
                    }
                }
            }
        } else {
            require(url.startsWith("blob:") && target.owns(url.removePrefix("blob:")))
            require(browser != null && target.owns(browser.url.orEmpty()))
            saveBlob(browser, url, destination, target)
        }
    }
    private suspend fun saveBlob(browser: WebView, url: String, destination: Uri, target: Credentials) {
        val key = JSONObject.quote("songyaDownload" + UUID.randomUUID().toString().replace("-", ""))
        evaluate(browser, target, """(() => { const s = window[$key] = {}; fetch(${JSONObject.quote(url)}).then(r => r.blob()).then(b => { s.blob=b; s.size=b.size; s.ready=true; }).catch(() => s.error=true); })()""")
        val output = withContext(Dispatchers.IO) { requireNotNull(context.contentResolver.openOutputStream(destination, "wt")) }
        try {
            val size = waitFor(browser, target, key, "ready").getLong("size")
            var offset = 0L
            while (offset < size) {
                val length = minOf(65536L, size - offset)
                evaluate(browser, target, """(() => { const s=window[$key]; s.data=null; const r=new FileReader(); r.onload=()=>s.data=r.result.split(',')[1]; r.onerror=()=>s.error=true; r.readAsDataURL(s.blob.slice($offset,${offset+length})); })()""")
                val bytes = Base64.decode(waitFor(browser, target, key, "data").getString("data"), Base64.DEFAULT)
                require(bytes.size.toLong() == length)
                withContext(Dispatchers.IO) { output.write(bytes) }
                offset += length
            }
        } finally {
            withContext(Dispatchers.IO) { output.close() }
            if (browser.isAttachedToWindow) evaluate(browser, target, "delete window[$key]")
        }
    }
    private suspend fun waitFor(browser: WebView, target: Credentials, key: String, field: String): JSONObject {
        repeat(400) {
            if (!browser.isAttachedToWindow) throw IOException("Display closed")
            val raw = evaluate(browser, target, "JSON.stringify((s=>s?{ready:s.ready,size:s.size,data:s.data,error:s.error}:{error:true})(window[$key]))")
            val parsed = JSONObject(JSONTokener(raw).nextValue() as String)
            if (parsed.optBoolean("error")) throw IOException("Download interrupted")
            if (parsed.has(field) && !parsed.isNull(field) && parsed.opt(field) != false) return parsed
            delay(25)
        }
        throw IOException("Download timed out")
    }
    private suspend fun evaluate(browser: WebView, target: Credentials, script: String): String = suspendCancellableCoroutine { continuation ->
        require(target.owns(browser.url.orEmpty())) { "Download origin changed" }
        browser.evaluateJavascript(script) { value -> if (continuation.isActive) continuation.resume(value) }
    }
}
