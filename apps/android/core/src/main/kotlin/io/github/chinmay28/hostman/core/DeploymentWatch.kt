package io.github.chinmay28.hostman.core

import org.json.JSONObject

/**
 * One look at a deployment: what `GET /api/deployments/{id}` says about it,
 * reduced to what a notification shows.
 */
data class DeploymentSnapshot(
    val id: Long,
    /** running, succeeded, failed, canceled or interrupted. */
    val status: String,
    /** install or uninstall. */
    val kind: String,
    val appName: String,
    val hostName: String,
    val exitCode: Int?,
    val error: String,
    /** The last line the command printed, for the notification's second line. */
    val lastLine: String,
) {
    val running: Boolean get() = status == RUNNING
    val succeeded: Boolean get() = status == SUCCEEDED

    private val removing: Boolean get() = kind == "uninstall"
    private val what: String
        get() = if (removing) "$appName from $hostName" else "$appName to $hostName"

    /** The notification's title: what is happening, or how it ended. */
    val title: String
        get() = when (status) {
            RUNNING -> (if (removing) "Removing " else "Deploying ") + what
            SUCCEEDED -> (if (removing) "Removed " else "Deployed ") + what
            "canceled" -> (if (removing) "Removing " else "Deploying ") + what + " was canceled"
            "interrupted" -> (if (removing) "Removing " else "Deploying ") + what + " was interrupted"
            else -> (if (removing) "Removing " else "Deploying ") + what + " failed"
        }

    /** The notification's text: the latest output while running, the reason after a failure. */
    val text: String
        get() = when {
            running || succeeded -> lastLine
            error.isNotBlank() -> error
            exitCode != null -> "Exited with status $exitCode"
            else -> lastLine
        }

    companion object {
        const val RUNNING = "running"
        const val SUCCEEDED = "succeeded"

        /** Reads the API's answer, or null for anything that is not a deployment. */
        fun parse(body: String): DeploymentSnapshot? = try {
            val j = JSONObject(body)
            val id = j.optLong("id", 0)
            val status = j.optString("status")
            if (id <= 0 || status.isBlank()) {
                null
            } else {
                DeploymentSnapshot(
                    id = id,
                    status = status,
                    kind = j.optString("kind").ifBlank { "install" },
                    appName = j.optString("appName").ifBlank { "the app" },
                    hostName = j.optString("hostName").ifBlank { "its host" },
                    exitCode = if (j.isNull("exitCode") || !j.has("exitCode")) null else j.optInt("exitCode"),
                    error = j.optString("error"),
                    lastLine = lastLine(j.optString("log")),
                )
            }
        } catch (_: Exception) {
            null
        }

        private val ansi = Regex("\u001B\\[[0-9;?]*[ -/]*[@-~]|\u001B[@-Z\\\\-_]")

        /**
         * The last line of [log] worth showing: colour codes stripped, a
         * progress bar's carriage returns taken to their latest state, blank
         * lines skipped, and cut to fit a notification.
         */
        fun lastLine(log: String, max: Int = 160): String {
            val line = log.split('\n')
                .asReversed()
                .map { it.substringAfterLast('\r').replace(ansi, "").filter { c -> !c.isISOControl() }.trim() }
                .firstOrNull { it.isNotEmpty() }
                ?: return ""
            return if (line.length > max) line.take(max - 1).trimEnd() + "…" else line
        }
    }
}

/** What the watcher does next. */
sealed class WatchStep {
    /** Look again after [delayMs]. */
    data class Again(val delayMs: Long) : WatchStep()

    /** It ended: say how, and stop. */
    data class Finished(val last: DeploymentSnapshot) : WatchStep()

    /** Stop looking: the deployment is gone, or HostMan has been out of reach too long. */
    data class GiveUp(val reason: String) : WatchStep()
}

/**
 * Following one deployment by polling, as the notification does.
 *
 * HostMan going quiet is not the end of a deployment: updating HostMan
 * itself restarts it half way through, and a phone walks in and out of Wi-Fi.
 * So an unreachable server is retried, more slowly, for [patienceMs] since it
 * last answered, and only then given up on.
 */
class DeploymentWatch(
    private val intervalMs: Long = 3_000,
    private val unreachableIntervalMs: Long = 10_000,
    private val patienceMs: Long = 10 * 60_000,
    startedAt: Long,
) {
    private var lastContact = startedAt

    /** The server answered with [s] at [now]. */
    fun answered(s: DeploymentSnapshot, now: Long): WatchStep {
        lastContact = now
        return if (s.running) WatchStep.Again(intervalMs) else WatchStep.Finished(s)
    }

    /** The server answered that there is no such deployment (or no longer one). */
    fun gone(): WatchStep = WatchStep.GiveUp("HostMan no longer has that deployment.")

    /** No answer at [now]: HostMan restarting, or the phone off the network. */
    fun unreachable(now: Long): WatchStep =
        if (now - lastContact >= patienceMs) {
            WatchStep.GiveUp("Lost touch with HostMan. Open the app to see how it ended.")
        } else {
            WatchStep.Again(unreachableIntervalMs)
        }
}
