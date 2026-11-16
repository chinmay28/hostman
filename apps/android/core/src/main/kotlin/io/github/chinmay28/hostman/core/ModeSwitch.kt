package io.github.chinmay28.hostman.core

/** What happens when the person flips the app between its two modes. */
sealed interface ModeChange {
    /** Nothing stands in the way: save the new mode. */
    data object Apply : ModeChange

    /** HostMan cannot run here: this build or this processor has no server. */
    data object Unavailable : ModeChange

    /**
     * Thin client with no usable server address yet. The mode is still
     * saved — the other mode's settings are kept either way — but the
     * address has to be asked for before the app can open anything.
     */
    data object NeedsAddress : ModeChange
}

/**
 * The decisions behind Settings' switches, kept here so they are the same
 * wherever a switch is offered and can be tested without Android.
 */
object ModeSwitch {
    fun check(target: AppMode, config: AppConfig, canRunHere: Boolean): ModeChange = when (target) {
        AppMode.ON_PHONE -> if (canRunHere) ModeChange.Apply else ModeChange.Unavailable
        AppMode.REMOTE -> if (Origin.parse(config.serverAddress).isSuccess) ModeChange.Apply else ModeChange.NeedsAddress
    }

    /**
     * Whether "Serve other devices on this network" may be turned on: only
     * with a PIN for them to sign in with (see [AppConfig.pinGoodEnough]).
     */
    fun canShare(config: AppConfig): Boolean = AppConfig.pinGoodEnough(config.pin)
}
