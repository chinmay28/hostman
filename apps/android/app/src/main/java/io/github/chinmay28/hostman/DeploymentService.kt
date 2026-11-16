package io.github.chinmay28.hostman

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.webkit.CookieManager
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import io.github.chinmay28.hostman.core.DeploymentSnapshot
import io.github.chinmay28.hostman.core.DeploymentWatch
import io.github.chinmay28.hostman.core.Origin
import io.github.chinmay28.hostman.core.WatchStep
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

/**
 * Follows the deployments the page asked about (BridgeRequest.Watch) in the
 * notification shade: the latest line of output while each runs, and how it
 * ended — so a deploy started on the phone can be pocketed and still be
 * heard from.
 *
 * The deployment itself runs on HostMan, wherever that is; nothing here
 * keeps it alive or could stop it. This only asks
 * `GET /api/deployments/{id}` every few seconds, with the web view's own
 * cookies, of the configured origin alone, and gives up when it ends, is
 * gone, or HostMan has been out of reach for longer than a self-update
 * takes (core/DeploymentWatch).
 */
class DeploymentService : Service() {
    private val pool = Executors.newScheduledThreadPool(1)
    private val watching = ConcurrentHashMap<Long, Watched>()

    /** The deployment whose notification is the foreground one, or null. */
    @Volatile
    private var foregroundId: Long? = null

    private class Watched(val id: Long, var title: String, val watch: DeploymentWatch) {
        var next: ScheduledFuture<*>? = null
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val id = intent?.getLongExtra(EXTRA_ID, 0) ?: 0
        when (intent?.action) {
            ACTION_WATCH -> if (id > 0) watch(id, intent.getStringExtra(EXTRA_TITLE).orEmpty())
            ACTION_UNWATCH -> if (id > 0) finish(id, null)
        }
        // Only watch() starts this as a foreground service, with an id the
        // bridge has checked, and it always promotes; anything else that
        // leaves nothing to follow just ends.
        if (watching.isEmpty()) stop()
        // A deployment followed by a process Android killed is not worth
        // resurrecting blind: the page asks again when it is opened.
        return START_NOT_STICKY
    }

    private fun watch(id: Long, title: String) {
        val existing = watching[id]
        if (existing != null) {
            if (title.isNotBlank()) existing.title = title
            return
        }
        val w = Watched(id, title.ifBlank { getString(R.string.deployment_watching, id) }, DeploymentWatch(startedAt = System.currentTimeMillis()))
        watching[id] = w
        val n = progress(id, w.title, "")
        if (foregroundId == null) promote(id, n) else notify(progressId(id), n)
        w.next = pool.schedule({ poll(w) }, 0, TimeUnit.MILLISECONDS)
    }

    private fun poll(w: Watched) {
        if (watching[w.id] !== w) return
        val origin = ConfigStore(this).load().origin().getOrNull()
        val step = if (origin == null) {
            w.watch.gone()
        } else {
            when (val got = fetch(origin, w.id)) {
                is Fetched.Ok -> w.watch.answered(got.snapshot, System.currentTimeMillis()).also {
                    if (it is WatchStep.Again) {
                        w.title = got.snapshot.title
                        notify(progressId(w.id), progress(w.id, got.snapshot.title, got.snapshot.text))
                    }
                }
                Fetched.Gone -> w.watch.gone()
                Fetched.Unreachable -> w.watch.unreachable(System.currentTimeMillis())
            }
        }
        when (step) {
            is WatchStep.Again -> w.next = pool.schedule({ poll(w) }, step.delayMs, TimeUnit.MILLISECONDS)
            is WatchStep.Finished -> finish(w.id, result(w.id, step.last.title, step.last.text, step.last.succeeded))
            is WatchStep.GiveUp -> finish(w.id, result(w.id, w.title, step.reason, ok = false))
        }
    }

    private sealed class Fetched {
        data class Ok(val snapshot: DeploymentSnapshot) : Fetched()
        object Gone : Fetched()
        object Unreachable : Fetched()
    }

    private fun fetch(origin: Origin, id: Long): Fetched {
        val url = origin.url("/api/deployments/$id")
        return try {
            val conn = URL(url).openConnection() as HttpURLConnection
            try {
                conn.instanceFollowRedirects = false
                conn.connectTimeout = 5_000
                conn.readTimeout = 15_000
                CookieManager.getInstance().getCookie(url)?.let { conn.setRequestProperty("Cookie", it) }
                when (conn.responseCode) {
                    200 -> DeploymentSnapshot.parse(conn.inputStream.bufferedReader().readText())
                        ?.let { Fetched.Ok(it) } ?: Fetched.Unreachable
                    // Not found, or signed out with nobody here to sign back in.
                    400, 401, 404 -> Fetched.Gone
                    else -> Fetched.Unreachable
                }
            } finally {
                conn.disconnect()
            }
        } catch (_: Exception) {
            Fetched.Unreachable
        }
    }

    /** Stops following [id], leaving [ending] (how it ended) in the shade if given. */
    @Synchronized
    private fun finish(id: Long, ending: Notification?) {
        val w = watching.remove(id) ?: return
        w.next?.cancel(false)
        if (foregroundId == id) {
            val other = watching.values.firstOrNull()
            if (other != null) {
                promote(other.id, progress(other.id, other.title, ""))
            } else {
                stop()
            }
        }
        nm().cancel(progressId(id))
        if (ending != null && ConfigStore(this).followDeployments) notify(resultId(id), ending)
    }

    private fun stop() {
        foregroundId = null
        ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    @Synchronized
    private fun promote(id: Long, n: Notification) {
        val type = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0
        ServiceCompat.startForeground(this, progressId(id), n, type)
        foregroundId = id
    }

    override fun onDestroy() {
        for (w in watching.values) {
            w.next?.cancel(false)
            nm().cancel(progressId(w.id))
        }
        watching.clear()
        pool.shutdownNow()
        super.onDestroy()
    }

    private fun progress(id: Long, title: String, text: String): Notification =
        NotificationCompat.Builder(this, HostManApp.CHANNEL_DEPLOYING)
            .setSmallIcon(R.drawable.ic_stat_hostman)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setProgress(0, 0, true)
            .setContentIntent(openPage(id))
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setCategory(NotificationCompat.CATEGORY_PROGRESS)
            .addAction(
                0, getString(R.string.notify_stop_watching),
                PendingIntent.getService(
                    this, progressId(id),
                    Intent(this, DeploymentService::class.java).setAction(ACTION_UNWATCH).putExtra(EXTRA_ID, id),
                    PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
                ),
            )
            .build()

    private fun result(id: Long, title: String, text: String, ok: Boolean): Notification =
        NotificationCompat.Builder(this, HostManApp.CHANNEL_DEPLOYED)
            .setSmallIcon(R.drawable.ic_stat_hostman)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setContentIntent(openPage(id))
            .setAutoCancel(true)
            .setCategory(if (ok) NotificationCompat.CATEGORY_STATUS else NotificationCompat.CATEGORY_ERROR)
            .build()

    /** Opens the deployment's own page in the app. */
    private fun openPage(id: Long): PendingIntent = PendingIntent.getActivity(
        this, resultId(id),
        Intent(this, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP)
            .putExtra(MainActivity.EXTRA_DEPLOYMENT, id),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    private fun nm() = getSystemService(NotificationManager::class.java)

    private fun notify(nid: Int, n: Notification) {
        // Without the notification permission this is silently dropped,
        // which is the person's choice.
        runCatching { nm().notify(nid, n) }
    }

    companion object {
        const val ACTION_WATCH = "io.github.chinmay28.hostman.WATCH_DEPLOYMENT"
        const val ACTION_UNWATCH = "io.github.chinmay28.hostman.UNWATCH_DEPLOYMENT"
        private const val EXTRA_ID = "id"
        private const val EXTRA_TITLE = "title"

        // Clear of ServerService's 1 and the downloads' 100+.
        private fun progressId(id: Long) = 10_000 + (id % 1_000_000).toInt() * 2
        private fun resultId(id: Long) = progressId(id) + 1

        /** Follows deployment [id] until it ends. Call while the app is in the foreground. */
        fun watch(context: Context, id: Long, title: String) {
            ContextCompat.startForegroundService(
                context,
                Intent(context, DeploymentService::class.java).setAction(ACTION_WATCH).putExtra(EXTRA_ID, id).putExtra(EXTRA_TITLE, title),
            )
        }
    }
}
