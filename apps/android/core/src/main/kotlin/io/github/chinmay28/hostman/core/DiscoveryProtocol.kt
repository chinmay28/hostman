package io.github.chinmay28.hostman.core

import org.json.JSONObject

/**
 * Finding HostMan servers on the local network: the client half of
 * server/internal/discovery. The app broadcasts [QUERY] to [PORT]; every
 * server answers with its port, name and version, and the host is wherever
 * the answer came from.
 */
object DiscoveryProtocol {
    // These three must match server/internal/discovery.
    const val PORT = 8900
    const val QUERY = "HOSTMAN-DISCOVER/1"
    const val SERVICE = "hostman"

    data class Found(val name: String, val origin: Origin, val version: String)

    /** Reads one answer, or null for anything that is not HostMan's. */
    fun parse(body: String, fromHost: String): Found? = try {
        val json = JSONObject(body)
        val port = json.optInt("port", 0)
        if (json.optString("service") != SERVICE || port !in 1..65535 || fromHost.isBlank()) {
            null
        } else {
            val host = fromHost.removePrefix("/").substringBefore('%')
            Found(
                name = json.optString("name").ifBlank { host },
                origin = Origin(if (json.optBoolean("secure", false)) "https" else "http", host, port),
                version = json.optString("version"),
            )
        }
    } catch (_: Exception) {
        null
    }
}
