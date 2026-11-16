package io.github.chinmay28.hostman

import android.Manifest
import android.annotation.SuppressLint
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.net.http.SslError
import android.os.Build
import android.os.Bundle
import android.os.Message
import android.view.View
import android.view.ViewGroup
import android.webkit.MimeTypeMap
import android.webkit.SslErrorHandler
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import io.github.chinmay28.hostman.core.AcceptTypes
import io.github.chinmay28.hostman.core.AppConfig
import io.github.chinmay28.hostman.core.AppMode
import io.github.chinmay28.hostman.core.Navigation
import io.github.chinmay28.hostman.core.NavigationPolicy
import io.github.chinmay28.hostman.core.Origin
import io.github.chinmay28.hostman.core.ServerState
import kotlin.concurrent.thread

/**
 * The app: HostMan's own web interface, in a web view, from whichever
 * server the settings name — another machine's, or the one on this phone.
 *
 * Everything HostMan does in a browser it does here, because it is the same
 * page: hosts, deployments and their logs, shells, services, files, port
 * forwards, Claude Code. The app supplies what a browser would have and a
 * web view does not — a file picker, Downloads, the share sheet, a real
 * browser for every other site — and one thing a browser cannot: a
 * deployment that keeps reporting in a notification after the app is left
 * (DeploymentService).
 */
class MainActivity : AppCompatActivity() {
    private lateinit var store: ConfigStore
    private lateinit var config: AppConfig
    private var origin: Origin? = null

    private lateinit var web: WebView
    private lateinit var statusPanel: View
    private lateinit var statusText: TextView
    private lateinit var statusDetail: TextView
    private lateinit var statusProgress: ProgressBar
    private lateinit var statusButtons: LinearLayout
    private lateinit var fullscreen: FrameLayout

    private var bridge: PageBridge? = null
    private var unobserve: (() -> Unit)? = null
    private var loaded = false
    private var customView: View? = null
    private var customViewCallback: WebChromeClient.CustomViewCallback? = null
    private var pendingFiles: ValueCallback<Array<Uri>>? = null

    /** A deployment page to open instead of the front page: a notification was tapped. */
    private var pendingDeployment: Long = 0

    /** Whether the server on the phone has been down since the page was last signed in. */
    private var serverWasDown = true

    private val pickFiles = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        val cb = pendingFiles ?: return@registerForActivityResult
        pendingFiles = null
        val data = result.data
        val uris = if (result.resultCode != RESULT_OK || data == null) {
            null
        } else {
            val clip = data.clipData
            if (clip != null && clip.itemCount > 0) Array(clip.itemCount) { clip.getItemAt(it).uri } else data.data?.let { arrayOf(it) }
        }
        cb.onReceiveValue(uris)
    }

    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) { }

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        store = ConfigStore(this)
        config = store.load()
        pendingDeployment = intent?.getLongExtra(EXTRA_DEPLOYMENT, 0) ?: 0
        if (!config.configured) {
            startActivity(Intent(this, SetupActivity::class.java))
            finish()
            return
        }

        setContentView(R.layout.activity_main)
        web = findViewById(R.id.web)
        statusPanel = findViewById(R.id.status)
        statusText = findViewById(R.id.status_text)
        statusDetail = findViewById(R.id.status_detail)
        statusProgress = findViewById(R.id.status_progress)
        statusButtons = findViewById(R.id.status_buttons)
        fullscreen = findViewById(R.id.fullscreen)
        findViewById<View>(R.id.retry).setOnClickListener { retry() }
        findViewById<View>(R.id.settings).setOnClickListener { openSettings() }

        // The page lays itself out for the screen it is given; the bars and
        // the keyboard are kept out of it here.
        ViewCompat.setOnApplyWindowInsetsListener(findViewById(R.id.root)) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout() or WindowInsetsCompat.Type.ime())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            WindowInsetsCompat.CONSUMED
        }

        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                when {
                    customView != null -> exitFullscreen()
                    loaded && web.canGoBack() -> web.goBack()
                    else -> finish()
                }
            }
        })

        val o = config.origin().getOrElse {
            showProblem(it.message ?: "The settings name no server.", null, retry = false)
            return
        }
        origin = o
        setUpWebView(o)

        when (config.mode) {
            AppMode.ON_PHONE -> startOnPhone()
            else -> load()
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        val id = intent.getLongExtra(EXTRA_DEPLOYMENT, 0)
        val o = origin
        if (id > 0 && o != null && ::web.isInitialized) {
            if (loaded) web.loadUrl(o.url("/deployments/$id")) else pendingDeployment = id
        }
    }

    override fun onResume() {
        super.onResume()
        if (!::config.isInitialized || !config.configured) return
        val now = store.load()
        if (now == config) return
        // Back from settings with something changed.
        val before = config
        config = now
        when {
            now.mode == AppMode.REMOTE && before.mode == AppMode.ON_PHONE -> LocalServer.stop(this)
            now.mode == AppMode.ON_PHONE && before.serverChanged(now) -> LocalServer.restart(this)
        }
        if (now.origin().getOrNull() != origin || now.mode != before.mode) recreate()
    }

    override fun onDestroy() {
        unobserve?.invoke()
        bridge?.close()
        if (::web.isInitialized) {
            (web.parent as? ViewGroup)?.removeView(web)
            web.destroy()
        }
        // Leaving the app (not turning the phone): the server goes too,
        // unless it was asked to keep running.
        if (isFinishing && ::config.isInitialized && config.mode == AppMode.ON_PHONE && !store.load().keepRunning) {
            LocalServer.stop(this)
        }
        super.onDestroy()
    }

    // --- HostMan on this phone ---

    private fun startOnPhone() {
        if (!LocalServer.available(this)) {
            showProblem(getString(R.string.setup_phone_unavailable), null, retry = false)
            return
        }
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
        showWaiting(getString(R.string.starting_server))
        unobserve = LocalServer.observe { s -> runOnUiThread { onServerState(s) } }
        LocalServer.start(this)
    }

    private fun onServerState(s: ServerState) {
        if (isFinishing || isDestroyed) return
        when (s) {
            // A server that has (re)started has forgotten every session, the
            // web view's included: sign in again and reload the page.
            is ServerState.Running -> if (serverWasDown) {
                serverWasDown = false
                signInThenLoad()
            } else {
                hideStatus()
            }
            is ServerState.Starting -> {
                serverWasDown = true
                if (!loaded) showWaiting(getString(R.string.starting_server))
            }
            is ServerState.Restarting -> {
                serverWasDown = true
                showWaiting(getString(R.string.restarting_server))
            }
            is ServerState.Failed -> {
                serverWasDown = true
                showProblem(getString(R.string.server_failed), s.logTail.takeLast(3_000), retry = true)
            }
            is ServerState.Stopped -> serverWasDown = true
        }
    }

    private fun retry() {
        when {
            config.mode == AppMode.ON_PHONE && LocalServer.state !is ServerState.Running -> {
                showWaiting(getString(R.string.starting_server))
                LocalServer.restart(this)
            }
            config.mode == AppMode.ON_PHONE -> signInThenLoad()
            else -> load()
        }
    }

    /**
     * HostMan on the phone runs behind a PIN only the app may know: sign the
     * web view in before showing the page (see core/SessionLogin).
     */
    private fun signInThenLoad() {
        val o = origin ?: return
        showWaiting(getString(R.string.signing_in))
        val pin = store.serverPin()
        thread(name = "hostman-sign-in") {
            val ok = WebSession.ensure(o, pin)
            runOnUiThread {
                if (isFinishing || isDestroyed) return@runOnUiThread
                if (ok) load() else showProblem(getString(R.string.sign_in_failed), null, retry = true)
            }
        }
    }

    /**
     * The page asked to follow deployment [id] in a notification. Asks for
     * the notification permission the first time, as it is what the person
     * just reached for; follows either way (the service runs regardless).
     */
    private fun watchDeployment(id: Long, title: String): Boolean {
        if (!store.followDeployments) return false
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
        DeploymentService.watch(this, id, title)
        return true
    }

    // --- the web view ---

    @SuppressLint("SetJavaScriptEnabled")
    private fun setUpWebView(o: Origin) {
        web.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            mediaPlaybackRequiresUserGesture = false
            setSupportMultipleWindows(true)
            javaScriptCanOpenWindowsAutomatically = true
            allowFileAccess = false
            allowContentAccess = false
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            builtInZoomControls = false
            // The page is laid out for the phone (its viewport tag says
            // device-width). Overview mode would instead zoom out to whatever
            // is widest on it, so a single element a few pixels too wide
            // rescaled the whole app and set every width on it moving.
            loadWithOverviewMode = false
            useWideViewPort = true
        }
        // Says which app this is to anything that logs user agents.
        web.settings.userAgentString = web.settings.userAgentString + " HostManApp/" + BuildConfig.VERSION_NAME

        bridge = PageBridge(this, o, config.mode ?: AppMode.REMOTE, ::openSettings, ::watchDeployment).also { it.install(web) }

        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean =
                route(request.url.toString())

            override fun onPageStarted(view: WebView, url: String, favicon: android.graphics.Bitmap?) {
                bridge?.onPageStarted(view, url)
            }

            override fun onPageFinished(view: WebView, url: String) {
                if (o.contains(url) && statusButtons.visibility != View.VISIBLE) {
                    loaded = true
                    hideStatus()
                }
            }

            override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
                if (!request.isForMainFrame) return
                loaded = false
                showProblem(getString(R.string.cannot_reach, o.toString()), error.description?.toString(), retry = true)
            }

            @SuppressLint("WebViewClientOnReceivedSslError")
            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) {
                handler.cancel()
                loaded = false
                showProblem(getString(R.string.bad_certificate, o.toString()), null, retry = true)
            }
        }

        web.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(view: WebView, callback: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
                pendingFiles?.onReceiveValue(null)
                pendingFiles = callback
                val types = AcceptTypes.mimeTypes(params.acceptTypes.orEmpty().toList()) {
                    MimeTypeMap.getSingleton().getMimeTypeFromExtension(it)
                }
                val intent = Intent(Intent.ACTION_GET_CONTENT).apply {
                    addCategory(Intent.CATEGORY_OPENABLE)
                    type = types.singleOrNull() ?: "*/*"
                    if (types.size > 1) putExtra(Intent.EXTRA_MIME_TYPES, types.toTypedArray())
                    if (params.mode == FileChooserParams.MODE_OPEN_MULTIPLE) putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true)
                }
                return try {
                    pickFiles.launch(intent)
                    true
                } catch (_: ActivityNotFoundException) {
                    pendingFiles = null
                    false
                }
            }

            // window.open: an app's page, a sign-in, a link opened in a new
            // tab. None belongs in the app; the address goes to the phone.
            override fun onCreateWindow(view: WebView, isDialog: Boolean, isUserGesture: Boolean, resultMsg: Message): Boolean {
                val catcher = WebView(this@MainActivity)
                catcher.webViewClient = object : WebViewClient() {
                    private var done = false

                    private fun take(v: WebView, url: String) {
                        if (done || url == "about:blank") return
                        done = true
                        v.stopLoading()
                        // A window that opens HostMan itself is a tab of HostMan.
                        if (!route(url)) web.loadUrl(url)
                        v.post { v.destroy() }
                    }

                    override fun shouldOverrideUrlLoading(v: WebView, request: WebResourceRequest): Boolean {
                        take(v, request.url.toString())
                        return true
                    }

                    // Some web views load a new window's first address without asking.
                    override fun onPageStarted(v: WebView, url: String, favicon: android.graphics.Bitmap?) = take(v, url)
                }
                (resultMsg.obj as WebView.WebViewTransport).webView = catcher
                resultMsg.sendToTarget()
                return true
            }

            override fun onShowCustomView(view: View, callback: CustomViewCallback) {
                customView?.let { exitFullscreen() }
                customView = view
                customViewCallback = callback
                fullscreen.addView(view, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
                fullscreen.visibility = View.VISIBLE
                WindowInsetsControllerCompat(window, window.decorView).apply {
                    hide(WindowInsetsCompat.Type.systemBars())
                    systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
                }
            }

            override fun onHideCustomView() = exitFullscreen()
        }

        web.setDownloadListener { url, _, contentDisposition, mime, _ ->
            when {
                o.contains(url) -> Downloads.fetch(
                    this, o, url,
                    io.github.chinmay28.hostman.core.FileNames.fromContentDisposition(contentDisposition),
                    mime,
                )
                url.startsWith("http") -> openExternally(url)
            }
        }
    }

    private fun load() {
        val o = origin ?: return
        showWaiting(getString(R.string.connecting, o.toString()))
        web.visibility = View.VISIBLE
        val deployment = pendingDeployment
        pendingDeployment = 0
        val current = web.url
        when {
            deployment > 0 -> web.loadUrl(o.url("/deployments/$deployment"))
            current != null && o.contains(current) -> web.reload()
            else -> web.loadUrl(o.url("/"))
        }
    }

    /** Where a navigation goes; true when the web view must not follow it. */
    private fun route(url: String): Boolean {
        val o = origin ?: return true
        return when (NavigationPolicy.decide(o, url)) {
            Navigation.IN_APP -> false
            Navigation.EXTERNAL -> {
                openExternally(url)
                true
            }
            Navigation.INTENT -> {
                openIntent(url)
                true
            }
            Navigation.BLOCK -> true
        }
    }

    private fun openExternally(url: String) {
        try {
            startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)).addCategory(Intent.CATEGORY_BROWSABLE))
        } catch (_: ActivityNotFoundException) {
            Toast.makeText(this, R.string.no_app_for_link, Toast.LENGTH_SHORT).show()
        }
    }

    /**
     * An intent: link. The page names an app and a type, never a component:
     * only apps that take links from a browser can be reached this way,
     * exactly as from Chrome.
     */
    private fun openIntent(url: String) {
        val intent = try {
            Intent.parseUri(url, Intent.URI_INTENT_SCHEME)
        } catch (_: Exception) {
            return
        }
        intent.component = null
        intent.selector = null
        intent.addCategory(Intent.CATEGORY_BROWSABLE)
        intent.flags = 0
        try {
            startActivity(intent)
        } catch (_: ActivityNotFoundException) {
            val pkg = intent.`package`
            if (pkg != null) openExternally("market://details?id=$pkg") else Toast.makeText(this, R.string.no_app_for_link, Toast.LENGTH_SHORT).show()
        }
    }

    private fun exitFullscreen() {
        val v = customView ?: return
        fullscreen.removeView(v)
        fullscreen.visibility = View.GONE
        customView = null
        customViewCallback?.onCustomViewHidden()
        customViewCallback = null
        WindowInsetsControllerCompat(window, window.decorView).show(WindowInsetsCompat.Type.systemBars())
        WindowCompat.setDecorFitsSystemWindows(window, false)
    }

    private fun openSettings() {
        startActivity(Intent(this, SettingsActivity::class.java))
    }

    companion object {
        /** A deployment id: open its page (DeploymentService's notifications). */
        const val EXTRA_DEPLOYMENT = "io.github.chinmay28.hostman.DEPLOYMENT"
    }

    // --- the panel in front of the page ---

    private fun showWaiting(text: String) {
        statusPanel.visibility = View.VISIBLE
        statusProgress.visibility = View.VISIBLE
        statusText.text = text
        statusDetail.visibility = View.GONE
        statusButtons.visibility = View.GONE
    }

    private fun showProblem(text: String, detail: String?, retry: Boolean) {
        statusPanel.visibility = View.VISIBLE
        statusProgress.visibility = View.GONE
        statusText.text = text
        statusDetail.text = detail.orEmpty()
        statusDetail.visibility = if (detail.isNullOrBlank()) View.GONE else View.VISIBLE
        statusButtons.visibility = View.VISIBLE
        findViewById<View>(R.id.retry).visibility = if (retry) View.VISIBLE else View.GONE
        if (::web.isInitialized) web.visibility = View.INVISIBLE
    }

    private fun hideStatus() {
        statusPanel.visibility = View.GONE
        web.visibility = View.VISIBLE
    }
}
