package io.github.chinmay28.hostman

import android.annotation.SuppressLint
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.webkit.WebView
import androidx.appcompat.app.AppCompatActivity
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebMessageCompat
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import io.github.chinmay28.hostman.core.AppMode
import io.github.chinmay28.hostman.core.BridgeReply
import io.github.chinmay28.hostman.core.BridgeRequest
import io.github.chinmay28.hostman.core.Origin
import io.github.chinmay28.hostman.core.TransferAction
import io.github.chinmay28.hostman.core.TransferException
import io.github.chinmay28.hostman.core.TransferMeta
import io.github.chinmay28.hostman.core.TransferSink
import io.github.chinmay28.hostman.core.Transfers
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream
import java.io.OutputStream
import java.util.concurrent.Executors

/**
 * What the app adds to HostMan's page: assets/hostman-bridge.js, run before
 * the page's own scripts, and the `HostManAndroid` object it talks through.
 *
 * Both are given to HostMan's [origin] alone — the web view itself checks
 * where each page and each message comes from, so a page from anywhere else
 * sees neither. The script is what makes a web page's downloads and
 * navigator.share() work in a web view, and how the page asks the app to
 * open its settings or follow a deployment; see [BridgeRequest].
 */
class PageBridge(
    private val activity: AppCompatActivity,
    private val origin: Origin,
    private val mode: AppMode,
    private val onOpenSettings: () -> Unit,
    private val onWatch: (id: Long, title: String) -> Boolean,
) : WebViewCompat.WebMessageListener {

    private val main = Handler(Looper.getMainLooper())
    private val io = Executors.newSingleThreadExecutor()
    private val staged = HashMap<String, StagedFile>()

    private sealed class Delivered {
        data class Saved(val name: String) : Delivered()
        data class Staged(val file: StagedFile) : Delivered()
    }

    private val transfers = Transfers(maxBytes = MAX_BYTES) { meta -> sinkFor(meta) }

    /** Whether this web view can carry the bridge at all (WebView 82+). */
    val supported: Boolean
        get() = WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)

    private val needsLateInjection: Boolean
        get() = !WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT)

    @SuppressLint("RequiresFeature")
    fun install(web: WebView) {
        if (!supported) return
        val rules = setOf(origin.toString())
        WebViewCompat.addWebMessageListener(web, OBJECT_NAME, rules, this)
        if (!needsLateInjection) WebViewCompat.addDocumentStartJavaScript(web, script(), rules)
    }

    /** For web views without document-start scripts: run it as the page starts. */
    fun onPageStarted(web: WebView, url: String) {
        if (supported && needsLateInjection && origin.contains(url)) web.evaluateJavascript(script(), null)
    }

    private fun script(): String {
        val js = activity.assets.open("hostman-bridge.js").bufferedReader().use { it.readText() }
        return js.replace("\"__HOSTMAN_MODE__\"", JSONObject.quote(ConfigStore.modeValue(mode)))
    }

    // Only ever called through the listener install() added, which it adds
    // only where the feature is present.
    @SuppressLint("RequiresFeature")
    override fun onPostMessage(
        view: WebView,
        message: WebMessageCompat,
        sourceOrigin: Uri,
        isMainFrame: Boolean,
        replyProxy: JavaScriptReplyProxy,
    ) {
        if (!isMainFrame) return
        val text = message.data ?: return
        val reply: (String) -> Unit = { r -> main.post { runCatching { replyProxy.postMessage(r) } } }
        val request = BridgeRequest.parse(text).getOrElse {
            reply(BridgeReply.error(BridgeRequest.reOf(text), it.message ?: "bad message"))
            return
        }
        when (request) {
            is BridgeRequest.Hello -> reply(BridgeReply.ok(request.re, "platform" to "android", "mode" to ConfigStore.modeValue(mode), "version" to BuildConfig.VERSION_NAME))
            is BridgeRequest.OpenSettings -> {
                onOpenSettings()
                reply(BridgeReply.ok(request.re))
            }
            is BridgeRequest.Fetch -> {
                Downloads.fetch(activity, origin, request.url, request.name)
                reply(BridgeReply.ok(request.re))
            }
            is BridgeRequest.Share -> share(request, reply)
            is BridgeRequest.Watch -> main.post {
                // "following" false: the person turned it off in the app's settings.
                val following = runCatching { onWatch(request.id, request.title) }.getOrDefault(false)
                reply(BridgeReply.ok(request.re, "following" to following))
            }
            else -> io.execute { transfer(request, reply) }
        }
    }

    private fun transfer(request: BridgeRequest, reply: (String) -> Unit) {
        try {
            when (request) {
                is BridgeRequest.Begin -> {
                    val id = transfers.begin(TransferMeta(request.name, request.mime, request.size, request.action))
                    reply(BridgeReply.ok(request.re, "id" to id))
                }
                is BridgeRequest.Chunk -> {
                    transfers.chunk(request.id, request.seq, request.data)
                    reply(BridgeReply.ok(request.re))
                }
                is BridgeRequest.End -> {
                    when (val done = transfers.end(request.id).second) {
                        is Delivered.Saved -> {
                            Downloads.toast(activity, activity.getString(R.string.saved_to_downloads, done.name))
                            reply(BridgeReply.ok(request.re, "name" to done.name))
                        }
                        is Delivered.Staged -> {
                            synchronized(staged) { staged[request.id] = done.file }
                            reply(BridgeReply.ok(request.re, "id" to request.id))
                        }
                    }
                }
                is BridgeRequest.Abort -> transfers.abort(request.id)
                else -> Unit
            }
        } catch (e: TransferException) {
            Log.w(TAG, "transfer failed: ${e.message}")
            if (request is BridgeRequest.End) {
                Downloads.toast(activity, activity.getString(R.string.save_failed, "the file", e.message ?: "failed"))
            }
            reply(BridgeReply.error(request.re, e.message ?: "transfer failed"))
        }
    }

    private fun share(request: BridgeRequest.Share, reply: (String) -> Unit) {
        val files = synchronized(staged) { request.ids.mapNotNull { staged.remove(it) } }
        if (files.size != request.ids.size) {
            reply(BridgeReply.error(request.re, "a file to share went missing"))
            return
        }
        main.post {
            try {
                Sharing.open(activity, files, request.title, request.text, request.url)
                reply(BridgeReply.ok(request.re))
            } catch (e: Exception) {
                reply(BridgeReply.error(request.re, e.message ?: "could not open the share sheet"))
            }
        }
    }

    private fun sinkFor(meta: TransferMeta): TransferSink<Delivered> {
        val dir = File(activity.cacheDir, "incoming").apply { mkdirs() }
        val file = File.createTempFile("part", ".bin", dir)
        val out = FileOutputStream(file).buffered(256 * 1024)
        return object : TransferSink<Delivered> {
            override val output: OutputStream = out

            override fun commit(): Delivered = when (meta.action) {
                TransferAction.SAVE -> try {
                    Delivered.Saved(file.inputStream().use { Downloads.save(activity, it, meta.name, meta.mime) })
                } finally {
                    file.delete()
                }
                TransferAction.STAGE -> Delivered.Staged(Sharing.stage(activity, file, meta.name, meta.mime))
            }

            override fun discard() {
                file.delete()
            }
        }
    }

    fun close() {
        io.execute { transfers.abortAll() }
        io.shutdown()
    }

    companion object {
        private const val TAG = "HostManBridge"
        const val OBJECT_NAME = "HostManAndroid"

        // The page already holds the whole file in memory to hand it over;
        // this only stops a runaway from filling the phone.
        private const val MAX_BYTES = 8L shl 30
    }
}
