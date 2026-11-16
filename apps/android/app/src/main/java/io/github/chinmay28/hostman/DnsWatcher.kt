package io.github.chinmay28.hostman

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import io.github.chinmay28.hostman.core.DnsList
import java.io.File

/**
 * Keeps HostMan on the phone's DNS file (server/internal/hostenv) naming the
 * servers of whichever network the phone is on, as it moves between Wi-Fi
 * and mobile data.
 */
class DnsWatcher(context: Context, private val file: File) {
    private val cm = context.getSystemService(ConnectivityManager::class.java)

    private val callback = object : ConnectivityManager.NetworkCallback() {
        override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) = write(lp)
        override fun onLost(network: Network) = write(null)
    }

    fun start() {
        write(cm.activeNetwork?.let { cm.getLinkProperties(it) })
        cm.registerDefaultNetworkCallback(callback)
    }

    fun stop() {
        runCatching { cm.unregisterNetworkCallback(callback) }
    }

    private fun write(lp: LinkProperties?) {
        val servers = lp?.dnsServers.orEmpty().mapNotNull { it.hostAddress }
        val text = DnsList.render(servers)
        runCatching {
            file.parentFile?.mkdirs()
            val tmp = File(file.path + ".tmp")
            tmp.writeText(text)
            if (!tmp.renameTo(file)) {
                file.writeText(text)
                tmp.delete()
            }
        }
    }
}
