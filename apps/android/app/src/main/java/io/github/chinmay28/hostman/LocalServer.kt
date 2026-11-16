package io.github.chinmay28.hostman

import android.content.Context
import android.content.Intent
import androidx.core.content.ContextCompat
import io.github.chinmay28.hostman.core.Health
import io.github.chinmay28.hostman.core.Origin
import io.github.chinmay28.hostman.core.ServerLaunch
import io.github.chinmay28.hostman.core.ServerState
import io.github.chinmay28.hostman.core.ServerSupervisor
import io.github.chinmay28.hostman.core.StaleServers
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.util.TimeZone
import java.util.concurrent.CopyOnWriteArraySet

/**
 * HostMan on the phone: where its binary and files are, and the one
 * supervisor that runs it. [ServerService] owns the supervisor's lifetime;
 * screens watch its state through [observe].
 */
object LocalServer {
    private const val BINARY = "libhostman.so"

    fun binary(context: Context): File? =
        File(context.applicationInfo.nativeLibraryDir, BINARY).takeIf { BuildConfig.HAS_SERVER && it.canExecute() }

    fun available(context: Context): Boolean = binary(context) != null

    fun dataDir(context: Context) = File(context.filesDir, "hostman")
    fun logFile(context: Context) = File(context.filesDir, "logs/server.log")
    fun dnsFile(context: Context) = File(context.filesDir, "dns-servers")

    private val listeners = CopyOnWriteArraySet<(ServerState) -> Unit>()

    @Volatile
    private var supervisor: ServerSupervisor? = null

    val state: ServerState get() = supervisor?.state ?: ServerState.Stopped

    /** Calls [listener] now and on every change, on the thread that changed it. */
    fun observe(listener: (ServerState) -> Unit): () -> Unit {
        listeners += listener
        listener(state)
        return { listeners -= listener }
    }

    fun start(context: Context) {
        ContextCompat.startForegroundService(context, Intent(context, ServerService::class.java))
    }

    fun restart(context: Context) {
        ContextCompat.startForegroundService(context, Intent(context, ServerService::class.java).setAction(ServerService.ACTION_RESTART))
    }

    fun stop(context: Context) {
        context.stopService(Intent(context, ServerService::class.java))
    }

    fun logTail(context: Context, maxChars: Int = 20_000): String = runCatching {
        val text = logFile(context).readText()
        if (text.length > maxChars) text.substring(text.length - maxChars) else text
    }.getOrDefault("")

    // --- for ServerService ---

    internal fun createSupervisor(context: Context): ServerSupervisor {
        val app = context.applicationContext
        val store = ConfigStore(app)
        return ServerSupervisor(
            launch = { launchFor(app, store) },
            logFile = logFile(app),
            onState = { s -> listeners.forEach { runCatching { it(s) } } },
            healthy = { alive ->
                val port = store.load().localPort
                Health.await(timeoutMs = 90_000, stillWanted = alive) { probe(Origin.loopback(port)) }
            },
        ).also { supervisor = it }
    }

    internal fun clearSupervisor(s: ServerSupervisor) {
        if (supervisor === s) supervisor = null
        listeners.forEach { runCatching { it(ServerState.Stopped) } }
    }

    /** Ends servers an earlier run of the app left behind, holding the port. */
    internal fun killStale(context: Context) {
        val bin = binary(context) ?: return
        for (pid in StaleServers.find(File("/proc"), bin, self = android.os.Process.myPid())) {
            android.os.Process.sendSignal(pid, 15)
            Thread.sleep(500)
            android.os.Process.killProcess(pid)
        }
    }

    private fun launchFor(context: Context, store: ConfigStore): ServerLaunch {
        val c = store.load()
        return ServerLaunch(
            binary = binary(context) ?: error("no server binary in this build"),
            dataDir = dataDir(context),
            cacheDir = File(context.cacheDir, "hostman"),
            port = c.localPort,
            shareOnNetwork = c.shareOnNetwork,
            machineName = store.machineName(c),
            dnsFile = dnsFile(context),
            timeZone = TimeZone.getDefault().id,
            pin = store.serverPin(c),
        )
    }

    /** The server's version if it answers health checks at [origin], else null. */
    fun probe(origin: Origin): String? = try {
        val conn = URL(origin.url("/api/health")).openConnection() as HttpURLConnection
        conn.connectTimeout = 1_000
        conn.readTimeout = 2_000
        try {
            if (conn.responseCode == 200) Health.version(conn.inputStream.bufferedReader().readText()) else null
        } finally {
            conn.disconnect()
        }
    } catch (_: Exception) {
        null
    }
}
