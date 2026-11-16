package io.github.chinmay28.hostman

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager

class HostManApp : Application() {
    override fun onCreate() {
        super.onCreate()
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_SERVER, getString(R.string.channel_server), NotificationManager.IMPORTANCE_LOW)
                .apply { setShowBadge(false) },
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_DOWNLOADS, getString(R.string.channel_downloads), NotificationManager.IMPORTANCE_LOW),
        )
        // The running deployment updates every few seconds and must not buzz
        // each time; how it ended is worth one alert.
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_DEPLOYING, getString(R.string.channel_deployments), NotificationManager.IMPORTANCE_LOW)
                .apply { setShowBadge(false) },
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_DEPLOYED, getString(R.string.channel_deployment_results), NotificationManager.IMPORTANCE_DEFAULT),
        )
    }

    companion object {
        const val CHANNEL_SERVER = "server"
        const val CHANNEL_DOWNLOADS = "downloads"
        const val CHANNEL_DEPLOYING = "deploying"
        const val CHANNEL_DEPLOYED = "deployed"
    }
}
