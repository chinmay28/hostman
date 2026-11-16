package io.github.chinmay28.hostman

import android.content.Context
import android.net.wifi.WifiManager
import io.github.chinmay28.hostman.core.DiscoveryProtocol
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.NetworkInterface
import java.net.SocketTimeoutException

/**
 * Finds HostMan servers on the Wi-Fi: a broadcast to the
 * discovery port, and whatever answers in the next couple of seconds.
 */
object LanDiscovery {
    /** Blocks for about [timeoutMs]; call it off the main thread. */
    fun find(context: Context, timeoutMs: Int = 2_000): List<DiscoveryProtocol.Found> {
        val wifi = context.applicationContext.getSystemService(WifiManager::class.java)
        val lock = wifi?.createMulticastLock("hostman-discovery")?.apply {
            setReferenceCounted(false)
            acquire()
        }
        try {
            DatagramSocket().use { socket ->
                socket.broadcast = true
                socket.soTimeout = 250
                val query = DiscoveryProtocol.QUERY.toByteArray(Charsets.UTF_8)
                for (target in broadcastAddresses()) {
                    runCatching { socket.send(DatagramPacket(query, query.size, target, DiscoveryProtocol.PORT)) }
                }
                val found = LinkedHashMap<String, DiscoveryProtocol.Found>()
                val buf = ByteArray(2048)
                val deadline = System.currentTimeMillis() + timeoutMs
                while (System.currentTimeMillis() < deadline) {
                    val packet = DatagramPacket(buf, buf.size)
                    try {
                        socket.receive(packet)
                    } catch (_: SocketTimeoutException) {
                        continue
                    }
                    val host = packet.address?.hostAddress ?: continue
                    val f = DiscoveryProtocol.parse(String(packet.data, 0, packet.length, Charsets.UTF_8), host) ?: continue
                    found[f.origin.toString()] = f
                }
                return found.values.toList()
            }
        } catch (_: Exception) {
            return emptyList()
        } finally {
            lock?.release()
        }
    }

    private fun broadcastAddresses(): List<InetAddress> {
        val out = LinkedHashSet<InetAddress>()
        runCatching {
            for (iface in NetworkInterface.getNetworkInterfaces()) {
                if (!iface.isUp || iface.isLoopback) continue
                for (addr in iface.interfaceAddresses) addr.broadcast?.let { out.add(it) }
            }
        }
        out.add(InetAddress.getByName("255.255.255.255"))
        return out.toList()
    }
}
