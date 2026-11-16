package io.github.chinmay28.hostman.core

/** How the app reaches HostMan. */
enum class AppMode {
    /** A thin client: HostMan runs on another machine (a Pi), and the app shows it. */
    REMOTE,

    /** Full stack: HostMan runs on this phone, and the app shows that. */
    ON_PHONE,
}

/**
 * Everything the person chose in Settings.
 *
 * Both modes' settings are kept side by side, so switching back and forth
 * keeps the other mode's address and port rather than starting over.
 */
data class AppConfig(
    /** Null until the first-run screen has been answered. */
    val mode: AppMode? = null,

    /** REMOTE: the server, as typed; [Origin.parse] reads it. */
    val serverAddress: String = "",

    /** ON_PHONE: the port the server on the phone listens on. */
    val localPort: Int = Origin.DEFAULT_PORT,

    /**
     * ON_PHONE: also serve other devices on this network — a laptop's
     * browser, another phone — rather than this phone alone. Needs [pin].
     */
    val shareOnNetwork: Boolean = false,

    /**
     * ON_PHONE: keep the server running when the app is closed, so health
     * checks, port forwards and other devices carry on.
     */
    val keepRunning: Boolean = false,

    /**
     * ON_PHONE: the name a network search lists this phone's HostMan under.
     * Blank means the phone's own name.
     */
    val machineName: String = "",

    /**
     * ON_PHONE: the PIN other devices sign in to this phone's HostMan with.
     * Blank means none chosen: the app then guards the server with a secret
     * of its own (see [ServerLaunch.pin]) and signs its own web view in.
     */
    val pin: String = "",
) {
    val configured: Boolean get() = mode != null

    /** The origin the web view opens, or a reason it has none. */
    fun origin(): Result<Origin> = when (mode) {
        null -> Result.failure(IllegalStateException("Choose how to reach HostMan first."))
        AppMode.REMOTE -> Origin.parse(serverAddress)
        AppMode.ON_PHONE -> if (localPort in 1024..65535) {
            Result.success(Origin.loopback(localPort))
        } else {
            Result.failure(IllegalArgumentException("The port must be between 1024 and 65535."))
        }
    }

    /**
     * Whether going from this configuration to [next] needs the server on
     * the phone restarted: anything it was started with has changed.
     */
    fun serverChanged(next: AppConfig): Boolean =
        mode != next.mode ||
            (
                next.mode == AppMode.ON_PHONE &&
                    (
                        localPort != next.localPort || shareOnNetwork != next.shareOnNetwork ||
                            machineName != next.machineName || pin != next.pin
                        )
                )

    companion object {
        /**
         * Whether [pin] will do for other devices: HostMan on the phone holds
         * a key that runs commands as root on every host, so the network gets
         * it only behind a PIN, and a PIN of at least four characters.
         */
        fun pinGoodEnough(pin: String): Boolean = pin.trim().length >= 4 && pin.none { it.isISOControl() }
    }
}
