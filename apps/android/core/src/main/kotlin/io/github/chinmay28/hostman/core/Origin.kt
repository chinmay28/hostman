package io.github.chinmay28.hostman.core

import java.net.URI
import java.util.Locale

/**
 * Where a HostMan server is: a scheme, a host and a port, and nothing else.
 *
 * Everything the app trusts is keyed on this. The web view loads only this
 * origin, the bridge to native code answers only it, and cookies are handed
 * to a download only when the download is on it.
 */
data class Origin(val scheme: String, val host: String, val port: Int) {

    init {
        require(scheme == "http" || scheme == "https") { "scheme must be http or https" }
        require(host.isNotEmpty()) { "host must not be empty" }
        require(port in 1..65535) { "port out of range" }
    }

    val secure: Boolean get() = scheme == "https"

    /** The host as it goes into a URL: an IPv6 literal in brackets. */
    private val urlHost: String get() = if (host.contains(':')) "[$host]" else host

    /** "https://hostman.example.com" or "http://192.168.1.20:8899", with no trailing slash. */
    override fun toString(): String {
        val defaultPort = if (secure) 443 else 80
        return if (port == defaultPort) "$scheme://$urlHost" else "$scheme://$urlHost:$port"
    }

    /** The address of [path] on this origin. */
    fun url(path: String = "/"): String = toString() + (if (path.startsWith("/")) path else "/$path")

    /** Whether [url] is on this origin — same scheme, host and port. */
    fun contains(url: String): Boolean = of(url) == this

    companion object {
        /** The port hostman listens on unless told otherwise. */
        const val DEFAULT_PORT = 8899

        /** The full-stack server: on this phone, reachable only from it. */
        fun loopback(port: Int): Origin = Origin("http", "127.0.0.1", port)

        /**
         * Reads what somebody typed into "server address".
         *
         * Forgiving about what people type: no scheme means http, and http
         * with no port means HostMan's own 8899 — "nakedpi.local" is the same
         * server as "http://nakedpi.local:8899". A path, query or fragment is dropped;
         * the app always opens the server's root. Credentials in the address
         * are refused rather than dropped, since keeping them would put a
         * password in settings and dropping them silently would surprise.
         */
        fun parse(input: String): Result<Origin> {
            val text = input.trim()
            if (text.isEmpty()) return Result.failure(IllegalArgumentException("Enter the server's address."))
            val withScheme = if (Regex("^[a-zA-Z][a-zA-Z0-9+.-]*://").containsMatchIn(text)) text else "http://$text"
            val uri = try {
                URI(withScheme)
            } catch (e: Exception) {
                return Result.failure(IllegalArgumentException("That is not an address: $text"))
            }
            val scheme = uri.scheme?.lowercase(Locale.ROOT)
            if (scheme != "http" && scheme != "https") {
                return Result.failure(IllegalArgumentException("The address must start with http:// or https://."))
            }
            if (uri.rawUserInfo != null) {
                return Result.failure(IllegalArgumentException("Leave the user name and password out of the address."))
            }
            val host = uri.host?.removePrefix("[")?.removeSuffix("]")?.lowercase(Locale.ROOT)
            if (host.isNullOrEmpty()) {
                return Result.failure(IllegalArgumentException("That address has no host: $text"))
            }
            val typedPort = uri.port
            val port = when {
                typedPort > 0 -> typedPort
                scheme == "https" -> 443
                // "http://pi.local" spelled out in full still means the default
                // HTTP port; only a bare host gets HostMan's own.
                withScheme !== text -> DEFAULT_PORT
                else -> 80
            }
            return runCatching { Origin(scheme, host, port) }
        }

        /** The origin of a full URL, or null when it is not http(s). */
        fun of(url: String): Origin? = try {
            val uri = URI(url)
            val scheme = uri.scheme?.lowercase(Locale.ROOT)
            val host = uri.host?.removePrefix("[")?.removeSuffix("]")?.lowercase(Locale.ROOT)
            if ((scheme != "http" && scheme != "https") || host.isNullOrEmpty()) {
                null
            } else {
                val port = if (uri.port > 0) uri.port else if (scheme == "https") 443 else 80
                Origin(scheme, host, port)
            }
        } catch (_: Exception) {
            null
        }
    }
}
