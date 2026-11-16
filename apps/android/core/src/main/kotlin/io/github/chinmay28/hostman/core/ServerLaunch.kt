package io.github.chinmay28.hostman.core

import java.io.File

/**
 * How HostMan on the phone is started: the command line and environment for
 * the hostman binary, worked out from the settings and the app's directories.
 *
 * Kept apart from starting it so that what the server is told can be read,
 * and tested, without a phone.
 */
data class ServerLaunch(
    /** The hostman binary, packaged as libhostman.so so Android installs it executable. */
    val binary: File,

    /** Private to the app: the database, which holds HostMan's SSH key. */
    val dataDir: File,

    /** Private scratch space, which Android may clear. */
    val cacheDir: File,

    val port: Int,
    val shareOnNetwork: Boolean,

    /** What a network search lists this phone's HostMan as. */
    val machineName: String,

    /** The file [DnsList] writes and the server re-reads; see server/internal/hostenv. */
    val dnsFile: File,

    /** An IANA zone name, e.g. "Europe/Berlin". */
    val timeZone: String,

    /**
     * The PIN the server asks for: the person's when they chose one, else a
     * secret the app generated and keeps to itself. Either way the server is
     * never open: a port on 127.0.0.1 is reachable by every app on the phone,
     * and this one holds a key that runs commands as root on every host.
     */
    val pin: String,
) {
    init {
        require(pin.isNotBlank()) { "HostMan on the phone always has a PIN" }
    }

    val database: File get() = File(dataDir, "hostman.db")

    fun command(): List<String> = buildList {
        add(binary.absolutePath)
        // Loopback unless asked: then only this phone's apps can reach it,
        // and the PIN keeps them out.
        add("-addr"); add((if (shareOnNetwork) "0.0.0.0" else "127.0.0.1") + ":" + port)
        add("-db"); add(database.absolutePath)
        // No sshd, no sudo, and updated by installing a newer app: there is
        // no home host here and nothing for HostMan to update itself with.
        add("-managed-by"); add(MANAGED_BY)
        // An app's port forward is for the person on this phone unless the
        // phone serves the network too.
        if (!shareOnNetwork) {
            add("-forward-bind"); add("127.0.0.1")
        }
        // Answering a network search only makes sense on a server it can reach.
        add("-discovery-port"); add(if (shareOnNetwork) DiscoveryProtocol.PORT.toString() else "0")
        if (machineName.isNotBlank()) {
            add("-name"); add(machineName.trim())
        }
    }

    fun environment(): Map<String, String> = buildMap {
        // Go reads these for the home directory and temporary files; on
        // Android it would otherwise pick /sdcard and /data/local/tmp, which
        // the app cannot write. USER is what os/user falls back to without
        // an /etc/passwd.
        put("HOME", dataDir.absolutePath)
        put("TMPDIR", cacheDir.absolutePath)
        put("XDG_CONFIG_HOME", File(dataDir, ".config").absolutePath)
        put("XDG_CACHE_HOME", cacheDir.absolutePath)
        put("USER", "hostman")
        put("HOSTMAN_DNS_FILE", dnsFile.absolutePath)
        // Used only while the phone reports no servers of its own: some VPNs
        // and carriers do not say.
        put("HOSTMAN_DNS", DnsList.FALLBACK.joinToString(","))
        if (timeZone.isNotBlank()) put("HOSTMAN_TZ", timeZone)
        // In the environment rather than on the command line, where it would
        // be one /proc read away.
        put("HOSTMAN_PIN", pin)
    }

    companion object {
        /** What the server says updates it (`-managed-by`): the web UI shows it in Settings. */
        const val MANAGED_BY = "the HostMan Android app"
    }
}

/** The DNS server file HostMan on the phone reads (server/internal/hostenv). */
object DnsList {
    /** Asked only when the phone names no DNS servers at all. */
    val FALLBACK = listOf("1.1.1.1", "8.8.8.8")

    /**
     * The file's contents for these servers: one address per line, IPv6
     * scopes dropped (the server cannot use them), duplicates once.
     */
    fun render(servers: List<String>): String {
        val lines = servers
            .map { it.trim().removePrefix("/").substringBefore('%') }
            .filter { it.isNotEmpty() }
            .distinct()
        return buildString {
            append("# DNS servers of the phone's current network, written by the HostMan app.\n")
            for (line in lines) append(line).append('\n')
        }
    }
}

/** What a phone gets called when the person has not named it. */
object MachineName {
    /**
     * A name from the phone's own: the name the person gave the device, else
     * its maker and model ("Pixel 9"), else "phone". Reduced to what reads
     * well in a list and travels on a command line.
     */
    fun of(deviceName: String?, manufacturer: String?, model: String?): String {
        val chosen = deviceName?.takeIf { it.isNotBlank() }
            ?: listOfNotNull(manufacturer?.takeIf { it.isNotBlank() }, model?.takeIf { it.isNotBlank() })
                .let { parts ->
                    // "Google Pixel 9" reads oddly when the model already says it.
                    if (parts.size == 2 && parts[1].startsWith(parts[0], ignoreCase = true)) parts[1] else parts.joinToString(" ")
                }
                .takeIf { it.isNotBlank() }
            ?: "phone"
        val cleaned = chosen.filter { !it.isISOControl() }.trim().replace(Regex("\\s+"), " ")
        return cleaned.take(48).ifBlank { "phone" }
    }
}
