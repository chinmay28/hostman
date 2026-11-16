package io.github.chinmay28.hostman

import android.app.Notification
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import io.github.chinmay28.hostman.core.ServerState
import io.github.chinmay28.hostman.core.ServerSupervisor
import java.net.Inet4Address
import java.net.NetworkInterface
import java.util.concurrent.Executors

/**
 * Runs HostMan on the phone, in the foreground so Android lets it keep
 * working while the app is in the background, and says so in a notification
 * with a Stop button.
 */
class ServerService : Service() {
    private val work = Executors.newSingleThreadExecutor()
    private var supervisor: ServerSupervisor? = null
    private var dns: DnsWatcher? = null
    private var unobserve: (() -> Unit)? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopSelf()
            return START_NOT_STICKY
        }
        goForeground(ServerState.Starting)

        if (!LocalServer.available(this)) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (dns == null) dns = DnsWatcher(this, LocalServer.dnsFile(this)).also { it.start() }
        if (unobserve == null) unobserve = LocalServer.observe { s -> if (s !is ServerState.Stopped) goForeground(s) }

        val restart = intent?.action == ACTION_RESTART
        work.execute {
            val current = supervisor
            if (current != null && restart) {
                current.stop()
                LocalServer.clearSupervisor(current)
                supervisor = null
            }
            if (supervisor == null) {
                LocalServer.killStale(this)
                supervisor = LocalServer.createSupervisor(this).also { it.start() }
            } else if (supervisor?.running != true) {
                supervisor?.start()
            }
        }
        // Started again by Android after it killed the process for memory —
        // the person asked for HostMan to run, and it should come back.
        return START_STICKY
    }

    /** Swiped away from recents: stop too, unless asked to keep running. */
    override fun onTaskRemoved(rootIntent: Intent?) {
        if (!ConfigStore(this).load().keepRunning) stopSelf()
    }

    override fun onDestroy() {
        unobserve?.invoke()
        unobserve = null
        dns?.stop()
        dns = null
        val s = supervisor
        supervisor = null
        // stop() waits for the server to record its deployments and close
        // its database.
        work.execute {
            s?.stop()
            s?.let { LocalServer.clearSupervisor(it) }
        }
        work.shutdown()
        super.onDestroy()
    }

    private fun goForeground(state: ServerState) {
        val type = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0
        ServiceCompat.startForeground(this, NOTIFICATION_ID, notification(state), type)
    }

    private fun notification(state: ServerState): Notification {
        val c = ConfigStore(this).load()
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE,
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, ServerService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE,
        )
        val title = when (state) {
            is ServerState.Running -> getString(R.string.notify_server_running)
            is ServerState.Failed -> getString(R.string.server_failed)
            is ServerState.Restarting -> getString(R.string.restarting_server)
            else -> getString(R.string.starting_server)
        }
        val text = if (c.shareOnNetwork) {
            lanAddress()?.let { getString(R.string.notify_server_shared, it, c.localPort) } ?: getString(R.string.pref_share_summary_on)
        } else {
            getString(R.string.notify_server_local)
        }
        return NotificationCompat.Builder(this, HostManApp.CHANNEL_SERVER)
            .setSmallIcon(R.drawable.ic_stat_hostman)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setContentIntent(open)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .addAction(0, getString(R.string.notify_stop), stop)
            .build()
    }

    companion object {
        const val ACTION_STOP = "io.github.chinmay28.hostman.STOP_SERVER"
        const val ACTION_RESTART = "io.github.chinmay28.hostman.RESTART_SERVER"
        private const val NOTIFICATION_ID = 1

        /** This phone's address on the local network, for other devices to type. */
        fun lanAddress(): String? = runCatching {
            NetworkInterface.getNetworkInterfaces().toList()
                .filter { it.isUp && !it.isLoopback }
                .flatMap { it.inetAddresses.toList() }
                .firstOrNull { it is Inet4Address && it.isSiteLocalAddress }
                ?.hostAddress
        }.getOrNull()
    }
}
