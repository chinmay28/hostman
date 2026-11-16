package io.github.chinmay28.hostman.core

import java.io.File
import java.util.concurrent.TimeUnit

/** How a server that keeps dying is restarted, and when it is left down. */
class RestartPolicy(
    /** This many crashes within [windowMs] and it is left down. */
    private val maxCrashes: Int = 5,
    private val windowMs: Long = 120_000,
    private val firstDelayMs: Long = 1_000,
    private val maxDelayMs: Long = 30_000,
) {
    /**
     * The wait before the next start, given when the server crashed before
     * (oldest first, the latest included), or null to give up.
     */
    fun nextDelay(crashes: List<Long>, now: Long): Long? {
        val recent = crashes.count { now - it < windowMs }
        if (recent >= maxCrashes) return null
        val doublings = (recent - 1).coerceIn(0, 20)
        return (firstDelayMs shl doublings).coerceAtMost(maxDelayMs)
    }
}

/** What the server on the phone is doing. */
sealed class ServerState {
    object Stopped : ServerState()
    object Starting : ServerState()
    data class Running(val version: String) : ServerState()

    /** It exited by itself; another start follows after [retryInMs]. */
    data class Restarting(val exitCode: Int, val retryInMs: Long) : ServerState()

    /** It kept exiting, and has been left down. [logTail] is what it last said. */
    data class Failed(val exitCode: Int, val logTail: String) : ServerState()
}

/**
 * Runs hostman as a child process and keeps it running.
 *
 * Output goes to [logFile], which is rolled over to `.1` beside it when it
 * passes [maxLogBytes] at a start. A server that exits by itself is started
 * again under [policy]; [stop] asks it to stop (SIGTERM, which hostman answers by
 * recording an outcome for every deployment in flight and closing its
 * database) and kills it only if it has not gone in [stopGraceMs].
 *
 * [healthy] is asked, on the supervisor's own thread, after each start: it
 * should block until the server answers and return its version, or null if it
 * never did.
 */
class ServerSupervisor(
    private val launch: () -> ServerLaunch,
    private val logFile: File,
    private val onState: (ServerState) -> Unit,
    private val healthy: (stillWanted: () -> Boolean) -> String?,
    private val policy: RestartPolicy = RestartPolicy(),
    private val maxLogBytes: Long = 2L shl 20,
    private val stopGraceMs: Long = 8_000,
    private val clock: () -> Long = System::currentTimeMillis,
) {
    private val lock = Object()
    private var process: Process? = null
    private var thread: Thread? = null
    private var wanted = false

    @Volatile
    var state: ServerState = ServerState.Stopped
        private set

    val running: Boolean get() = synchronized(lock) { wanted }

    /** Starts the server unless it is already wanted. Returns at once. */
    fun start() {
        synchronized(lock) {
            if (wanted) return
            wanted = true
            thread = Thread(::superviseSafely, "hostman-supervisor").apply { isDaemon = true; start() }
        }
    }

    /** Stops the server and waits for it to go. */
    fun stop() {
        val t: Thread?
        val p: Process?
        synchronized(lock) {
            wanted = false
            t = thread
            p = process
            thread = null
            lock.notifyAll()
        }
        p?.let { terminate(it) }
        t?.interrupt()
        t?.join(stopGraceMs + 2_000)
        publish(ServerState.Stopped)
    }

    private fun stillWanted(): Boolean = synchronized(lock) { wanted }

    // An exception escaping a thread ends an Android app, so none may: being
    // interrupted is how stop() ends the loop, and anything else is logged.
    private fun superviseSafely() {
        try {
            supervise()
        } catch (_: InterruptedException) {
        } catch (e: Exception) {
            appendLog("supervisor stopped: $e")
            synchronized(lock) { wanted = false }
            publish(ServerState.Failed(-1, tail()))
        }
    }

    private fun supervise() {
        val crashes = mutableListOf<Long>()
        while (stillWanted()) {
            publish(ServerState.Starting)
            val p = try {
                spawn()
            } catch (e: Exception) {
                appendLog("could not start the server: ${e.message}")
                publish(ServerState.Failed(-1, tail()))
                synchronized(lock) { wanted = false }
                return
            }
            synchronized(lock) {
                if (!wanted) {
                    terminate(p)
                    return
                }
                process = p
            }

            val version = healthy { stillWanted() && p.isAlive }
            if (version != null && p.isAlive && stillWanted()) {
                publish(ServerState.Running(version))
            } else if (p.isAlive && stillWanted()) {
                // Up, but not answering: as good as down, and counted as a crash.
                appendLog("the server did not answer in time; restarting it")
                terminate(p)
            }

            val code = try {
                p.waitFor()
            } catch (_: InterruptedException) {
                if (!stillWanted()) return
                continue
            }
            synchronized(lock) { process = null }
            if (!stillWanted()) return

            appendLog("server exited with status $code")
            crashes += clock()
            val delay = policy.nextDelay(crashes, clock())
            if (delay == null) {
                publish(ServerState.Failed(code, tail()))
                synchronized(lock) { wanted = false }
                return
            }
            publish(ServerState.Restarting(code, delay))
            synchronized(lock) {
                val until = clock() + delay
                while (wanted && clock() < until) {
                    try {
                        lock.wait((until - clock()).coerceAtLeast(1))
                    } catch (_: InterruptedException) {
                        if (!wanted) return
                    }
                }
            }
        }
    }

    private fun spawn(): Process {
        val l = launch()
        l.dataDir.mkdirs()
        l.cacheDir.mkdirs()
        rollLog()
        val pb = ProcessBuilder(l.command())
            .directory(l.dataDir)
            .redirectErrorStream(true)
            .redirectOutput(ProcessBuilder.Redirect.appendTo(logFile))
        pb.environment().putAll(l.environment())
        return pb.start()
    }

    private fun terminate(p: Process) {
        p.destroy()
        try {
            if (!p.waitFor(stopGraceMs, TimeUnit.MILLISECONDS)) {
                p.destroyForcibly()
                p.waitFor(2, TimeUnit.SECONDS)
            }
        } catch (_: InterruptedException) {
            p.destroyForcibly()
        }
    }

    private fun publish(s: ServerState) {
        state = s
        runCatching { onState(s) }
    }

    private fun rollLog() {
        logFile.parentFile?.mkdirs()
        if (logFile.length() > maxLogBytes) {
            val old = File(logFile.path + ".1")
            old.delete()
            logFile.renameTo(old)
        }
    }

    private fun appendLog(line: String) {
        runCatching { logFile.appendText("[app] $line\n") }
    }

    /** The end of the log, for saying why the server is down. */
    fun tail(maxChars: Int = 4_000): String = runCatching {
        val text = logFile.readText()
        if (text.length > maxChars) text.substring(text.length - maxChars) else text
    }.getOrDefault("")
}

/**
 * Servers left behind by an earlier run of the app — still holding the port
 * and the database after the app was killed — found by their command line.
 */
object StaleServers {
    /** The pids in [procDir] (normally /proc) whose program is [binary]. */
    fun find(procDir: File, binary: File, self: Int = -1): List<Int> {
        val want = binary.absolutePath
        return procDir.listFiles().orEmpty().mapNotNull { dir ->
            val pid = dir.name.toIntOrNull() ?: return@mapNotNull null
            if (pid == self) return@mapNotNull null
            val argv0 = runCatching { File(dir, "cmdline").readBytes() }.getOrNull()
                ?.let { bytes -> String(bytes, Charsets.UTF_8).substringBefore('\u0000') }
            if (argv0 == want) pid else null
        }.sorted()
    }
}
