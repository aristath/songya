package songya.app

import android.Manifest
import android.app.AlertDialog
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.DownloadListener
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebStorage
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.view.PreviewView
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import java.io.ByteArrayInputStream
import java.net.URLEncoder
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.Dispatchers

/** Native code owns pairing and Android handoffs; all application screens come from the web app. */
class MainActivity : ComponentActivity() {
    private val scope = MainScope()
    private lateinit var root: FrameLayout
    private lateinit var vault: DeviceVault
    private lateinit var authentication: Authentication
    @Volatile private var credentials: Credentials? = null
    @Volatile private var web: WebView? = null
    private var scanner: CameraScanner? = null
    private var credentialsUnavailable = false
    private var connecting = false
    @Volatile private var loggingOut = false
    private var lastConnectedAt = 0L
    private var returnPath: String? = null
    private var fullscreen: View? = null
    private var fullscreenCallback: WebChromeClient.CustomViewCallback? = null
    private var chooser: android.webkit.ValueCallback<Array<Uri>>? = null
    private var download: Pair<String, String>? = null
    private lateinit var downloads: Downloads
    private val cameraPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) scan() else showConnection("Camera access is needed to scan Songya’s pairing code.")
    }
    private val files = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        val selected = WebChromeClient.FileChooserParams.parseResult(result.resultCode, result.data)
            ?.filter { it.scheme == "content" }?.toTypedArray()
        chooser?.onReceiveValue(selected)
        chooser = null
    }
    private val saveFile = registerForActivityResult(ActivityResultContracts.CreateDocument("*/*")) { uri ->
        val pending = download
        download = null
        if (uri != null && pending != null) saveDownload(pending.first, uri)
    }
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        vault = DeviceVault(this)
        authentication = Authentication(vault)
        downloads = Downloads(this)
        returnPath = savedInstanceState?.getString("returnPath")
        root = FrameLayout(this)
        setContentView(root)
        ViewCompat.setOnApplyWindowInsetsListener(root) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            val ime = insets.getInsets(WindowInsetsCompat.Type.ime())
            view.setPadding(bars.left, bars.top, bars.right, maxOf(bars.bottom, ime.bottom))
            insets
        }
        onBackPressedDispatcher.addCallback(this, object : androidx.activity.OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (fullscreen != null) { hideFullscreen(); return }
                if (scanner != null) { scanner?.close(); scanner = null; showConnection(); return }
                val current = web
                if (current != null && current.canGoBack()) current.goBack() else finish()
            }
        })
        val incoming = intent?.dataString
        intent?.data = null
        val restored = restoreCredentials()
        if (incoming != null) acceptPairing(incoming)
        else if (restored) { if (credentials != null) connect() else showConnection() }
    }
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        val incoming = intent.dataString
        intent.data = null
        incoming?.let(::acceptPairing)
    }
    private fun restoreCredentials(): Boolean = try {
        credentials = vault.load()
        credentialsUnavailable = false
        true
    } catch (_: Exception) {
        credentialsUnavailable = true
        showConnection("Stored credentials could not be opened. Unlock this device and retry; the pairing is retained.")
        false
    }
    private fun showConnection(message: String = "Scan the QR code from Songya’s Connect page.") {
        scanner?.close(); scanner = null
        web?.visibility = View.GONE
        root.removeAllViews()
        val layout = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = android.view.Gravity.CENTER
            setPadding(48, 48, 48, 48)
        }
        layout.addView(TextView(this).apply { text = "Songya"; textSize = 28f; gravity = android.view.Gravity.CENTER })
        layout.addView(TextView(this).apply { text = message; textSize = 16f; setPadding(0, 24, 0, 24) })
        layout.addView(Button(this).apply {
            setAllCaps(false)
            text = if (credentials == null && !credentialsUnavailable) "Scan pairing code" else "Retry connection"
            setOnClickListener {
                if (credentialsUnavailable) {
                    if (restoreCredentials()) { if (credentials == null) showConnection() else connect() }
                } else if (credentials == null) scan() else connect()
            }
        })
        if (credentialsUnavailable) layout.addView(Button(this).apply {
            text = "Scan a new pairing code"
            setOnClickListener { scan() }
        })
        if (credentials != null) layout.addView(Button(this).apply {
            setAllCaps(false)
            text = "Unpair this device"
            setOnClickListener { unpair() }
        })
        root.addView(layout, FrameLayout.LayoutParams(-1, -1))
    }
    private fun scan() {
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) {
            cameraPermission.launch(Manifest.permission.CAMERA); return
        }
        scanner?.close()
        root.removeAllViews()
        val preview = PreviewView(this)
        root.addView(preview, FrameLayout.LayoutParams(-1, -1))
        scanner = CameraScanner(this, this, preview, { raw ->
            scanner?.close(); scanner = null
            acceptPairing(raw)
        }, { showConnection("Could not open the camera. Check permission and try again.") })
    }
    private fun acceptPairing(raw: String) {
        val pairing = Pairing.parse(raw)
        if (pairing == null) { showConnection("That code is invalid or expired. Refresh it in Songya and scan again."); return }
        AlertDialog.Builder(this).setTitle("Connect to Songya")
            .setMessage("Pair this device with ${Uri.parse(pairing.credentials.origin).authority}?")
            .setPositiveButton("Connect") { _, _ -> connect(pairing) }
            .setNegativeButton("Cancel") { _, _ -> if (credentials != null) connect() else showConnection() }
            .setOnCancelListener { showConnection() }.show()
    }
    private fun connect(pairing: Pairing? = null) {
        if (connecting || loggingOut) return
        val target = pairing?.credentials ?: credentials ?: return showConnection()
        if (pairing == null && returnPath == null) returnPath = web?.url?.takeIf { target.owns(it) }?.let { Uri.parse(it).encodedPath + (Uri.parse(it).encodedQuery?.let { q -> "?$q" } ?: "") }
        connecting = true
        root.removeAllViews()
        root.addView(ProgressBar(this), FrameLayout.LayoutParams(-2, -2, android.view.Gravity.CENTER))
        scope.launch {
            try {
                val handoff = withContext(Dispatchers.IO) { authentication.connect(target, pairing) }
                if (pairing != null) { clearWebSession(); if (credentials != null && credentials?.origin != target.origin) vault.clear(); returnPath = null }
                vault.save(target)
                credentials = target
                credentialsUnavailable = false
                val browser = browser()
                root.removeAllViews()
                (browser.parent as? ViewGroup)?.removeView(browser)
                root.addView(browser, FrameLayout.LayoutParams(-1, -1))
                browser.visibility = View.VISIBLE
                lastConnectedAt = android.os.SystemClock.elapsedRealtime()
                browser.postUrl(target.url("/api/mobile/session"), "handoff=${URLEncoder.encode(handoff, "UTF-8")}".toByteArray(Charsets.UTF_8))
            } catch (_: DeviceRevoked) {
                clearPairing()
                showConnection("This device is no longer authorized. Scan a fresh pairing code.")
            } catch (_: Exception) {
                showConnection("Could not connect securely to Songya. Check the connection and try again.")
            } finally { connecting = false }
        }
    }
    @android.annotation.SuppressLint("SetJavaScriptEnabled")
    private fun browser(): WebView {
        web?.let { return it }
        return WebView(this).also { browser ->
            web = browser
            browser.settings.javaScriptEnabled = true
            browser.settings.domStorageEnabled = true
            browser.settings.allowFileAccess = false
            browser.settings.allowContentAccess = true
            browser.settings.mixedContentMode = android.webkit.WebSettings.MIXED_CONTENT_NEVER_ALLOW
            CookieManager.getInstance().setAcceptThirdPartyCookies(browser, false)
            browser.webViewClient = object : WebViewClient() {
                override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                    if (credentials?.owns(request.url.toString()) == true) return false
                    if (request.isForMainFrame && request.hasGesture() && request.url.scheme in listOf("https", "http", "mailto", "tel"))
                        runCatching { startActivity(Intent(Intent.ACTION_VIEW, request.url)) }
                    return true
                }
                override fun onPageFinished(view: WebView, url: String) {
                    if (web !== view) return
                    CookieManager.getInstance().flush()
                    if (credentials?.owns(url) == true && Uri.parse(url).path == "/") {
                        val path = returnPath; returnPath = null
                        if (path != null && path.startsWith("/") && !path.startsWith("//") && !path.startsWith("/api/") && requireNotNull(credentials).url(path) != url)
                            view.loadUrl(requireNotNull(credentials).url(path))
                    }
                }
                override fun onReceivedHttpError(view: WebView, request: WebResourceRequest, response: WebResourceResponse) {
                    if (web !== view) return
                    if (request.isForMainFrame && response.statusCode >= 500) {
                        showConnection("Connection interrupted. Retry when Songya is reachable.")
                        return
                    }
                    if (credentials?.owns(request.url.toString()) == true && response.statusCode == 401 && !loggingOut)
                        runOnUiThread {
                            if (request.url.path == "/api/mobile/session") showConnection("The web session was not accepted. Please retry.")
                            else if (android.os.SystemClock.elapsedRealtime() - lastConnectedAt > 1500) connect()
                        }
                }
                override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
                    if (web !== view) return
                    if (request.isForMainFrame) showConnection("Connection interrupted. Retry when Songya is reachable.")
                }
                override fun onRenderProcessGone(view: WebView, detail: android.webkit.RenderProcessGoneDetail): Boolean {
                    (view.parent as? ViewGroup)?.removeView(view)
                    chooser?.onReceiveValue(null); chooser = null
                    view.destroy(); if (web === view) web = null
                    showConnection("The display closed. Retry to reconnect to Songya.")
                    return true
                }
                override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                    if (web !== view) return null
                    val target = credentials ?: return null
                    if (!target.owns(request.url.toString()) || request.url.path != "/api/logout" || request.method != "POST") return null
                    val origin = request.requestHeaders.entries.find { it.key.equals("Origin", true) }?.value
                    if (origin != target.origin || loggingOut) return null
                    loggingOut = true
                    return performLogout(target)
                }
            }
            browser.webChromeClient = object : WebChromeClient() {
                override fun onShowFileChooser(view: WebView, callback: android.webkit.ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
                    if (credentials?.owns(view.url.orEmpty()) != true) { callback.onReceiveValue(null); return true }
                    chooser?.onReceiveValue(null)
                    chooser = callback
                    runCatching { files.launch(params.createIntent()) }.onFailure { chooser?.onReceiveValue(null); chooser = null }
                    return true
                }
                override fun onShowCustomView(view: View, callback: CustomViewCallback) {
                    if (credentials?.owns(browser.url.orEmpty()) != true || fullscreen != null) { callback.onCustomViewHidden(); return }
                    fullscreen = view; fullscreenCallback = callback
                    browser.visibility = View.GONE
                    root.addView(view, FrameLayout.LayoutParams(-1, -1))
                    androidx.core.view.WindowInsetsControllerCompat(window, root).hide(WindowInsetsCompat.Type.systemBars())
                }
                override fun onHideCustomView() { hideFullscreen() }
                override fun onPermissionRequest(request: android.webkit.PermissionRequest) { request.deny() }
            }
            browser.setDownloadListener(DownloadListener { url, _, disposition, mime, _ ->
                if (credentials?.let { it.owns(url) || (url.startsWith("blob:") && it.owns(url.removePrefix("blob:")) && it.owns(browser.url.orEmpty())) } == true) {
                    download = url to mime.orEmpty()
                    saveFile.launch(if (url.startsWith("blob:")) "songya-download.${if (mime == "application/json") "json" else "bin"}" else android.webkit.URLUtil.guessFileName(url, disposition, mime))
                } else Toast.makeText(this, "This download could not be opened.", Toast.LENGTH_LONG).show()
            })
        }
    }
    private fun performLogout(target: Credentials): WebResourceResponse = try {
        authentication.revoke(target)
        finishLogout()
        WebResourceResponse("application/json", "UTF-8", ByteArrayInputStream("{\"ok\":true}".toByteArray()))
    } catch (_: DeviceRevoked) {
        finishLogout()
        WebResourceResponse("application/json", "UTF-8", ByteArrayInputStream("{\"ok\":true}".toByteArray()))
    } catch (_: Exception) {
        loggingOut = false
        WebResourceResponse("application/json", "UTF-8", 503, "Unavailable", mapOf("Cache-Control" to "no-store"), ByteArrayInputStream("{\"message\":\"Could not revoke the device. Retry when Songya is reachable.\"}".toByteArray()))
    }
    private fun finishLogout() {
        runOnUiThread { scope.launch { clearPairing(); loggingOut = false; showConnection() } }
    }
    private fun unpair() {
        val target = credentials ?: return
        if (loggingOut || connecting) return
        loggingOut = true
        scope.launch {
            try {
                withContext(Dispatchers.IO) { authentication.revoke(target) }
                clearPairing(); showConnection()
            } catch (_: DeviceRevoked) {
                clearPairing(); showConnection()
            } catch (_: Exception) {
                showConnection("Could not revoke this device. Retry when Songya is reachable.")
            } finally { loggingOut = false }
        }
    }
    private fun saveDownload(url: String, destination: Uri) {
        val target = credentials ?: return
        val cookie = CookieManager.getInstance().getCookie(target.origin).orEmpty()
        val browser = web
        scope.launch {
            try {
                downloads.save(url, target, destination, cookie, browser)
                Toast.makeText(this@MainActivity, "Download saved", Toast.LENGTH_SHORT).show()
            } catch (_: Exception) { Toast.makeText(this@MainActivity, "Download failed. Please try again.", Toast.LENGTH_LONG).show() }
        }
    }
    private suspend fun clearWebSession() {
        chooser?.onReceiveValue(null); chooser = null
        val previous = web
        web = null
        previous?.let { it.stopLoading(); it.clearCache(true); it.clearHistory(); (it.parent as? ViewGroup)?.removeView(it); it.destroy() }
        kotlinx.coroutines.suspendCancellableCoroutine<Unit> { continuation ->
            CookieManager.getInstance().removeAllCookies { if (continuation.isActive) continuation.resumeWith(Result.success(Unit)) }
        }
        CookieManager.getInstance().flush()
        WebStorage.getInstance().deleteAllData()
        web?.clearCache(true)
        web?.clearHistory()
    }
    private suspend fun clearPairing() {
        credentials = null
        credentialsUnavailable = false
        vault.clear()
        web?.stopLoading()
        clearWebSession()
        returnPath = null
    }
    private fun hideFullscreen() {
        fullscreen?.let { root.removeView(it) }; fullscreen = null
        fullscreenCallback?.onCustomViewHidden(); fullscreenCallback = null
        web?.visibility = View.VISIBLE
        androidx.core.view.WindowInsetsControllerCompat(window, root).show(WindowInsetsCompat.Type.systemBars())
    }
    override fun onSaveInstanceState(outState: Bundle) {
        web?.url?.takeIf { credentials?.owns(it) == true }?.let { url ->
            val uri = Uri.parse(url)
            outState.putString("returnPath", uri.encodedPath + (uri.encodedQuery?.let { "?$it" } ?: ""))
        }
        super.onSaveInstanceState(outState)
    }
    override fun onDestroy() {
        scanner?.close()
        hideFullscreen()
        chooser?.onReceiveValue(null)
        chooser = null
        scope.cancel()
        web?.let { (it.parent as? ViewGroup)?.removeView(it); it.destroy() }
        web = null
        super.onDestroy()
    }
}
