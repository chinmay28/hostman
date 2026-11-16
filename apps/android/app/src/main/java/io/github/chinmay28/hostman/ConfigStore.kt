package io.github.chinmay28.hostman

import android.content.Context
import android.content.SharedPreferences
import android.os.Build
import android.provider.Settings
import io.github.chinmay28.hostman.core.AppConfig
import io.github.chinmay28.hostman.core.AppMode
import io.github.chinmay28.hostman.core.MachineName
import io.github.chinmay28.hostman.core.Origin
import io.github.chinmay28.hostman.core.SessionLogin

/**
 * The settings, kept in the shared preferences the settings screen edits
 * directly — one store, so the screen and the rest of the app never disagree.
 */
class ConfigStore(context: Context) {
    private val app = context.applicationContext
    val prefs: SharedPreferences = app.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    fun load(): AppConfig = AppConfig(
        mode = when (prefs.getString(KEY_MODE, null)) {
            MODE_REMOTE -> AppMode.REMOTE
            MODE_ON_PHONE -> AppMode.ON_PHONE
            else -> null
        },
        serverAddress = prefs.getString(KEY_ADDRESS, "") ?: "",
        localPort = prefs.getString(KEY_PORT, null)?.toIntOrNull() ?: Origin.DEFAULT_PORT,
        shareOnNetwork = prefs.getBoolean(KEY_SHARE, false),
        keepRunning = prefs.getBoolean(KEY_KEEP, false),
        machineName = prefs.getString(KEY_MACHINE, "") ?: "",
        pin = prefs.getString(KEY_PIN, "") ?: "",
    )

    fun save(c: AppConfig) {
        prefs.edit()
            .putString(KEY_MODE, c.mode?.let(::modeValue))
            .putString(KEY_ADDRESS, c.serverAddress)
            .putString(KEY_PORT, c.localPort.toString())
            .putBoolean(KEY_SHARE, c.shareOnNetwork)
            .putBoolean(KEY_KEEP, c.keepRunning)
            .putString(KEY_MACHINE, c.machineName)
            .putString(KEY_PIN, c.pin)
            .apply()
    }

    /** The name a network search lists this phone's HostMan as: the person's, else the phone's own. */
    fun machineName(c: AppConfig = load()): String =
        c.machineName.trim().ifEmpty { defaultMachineName() }

    fun defaultMachineName(): String = MachineName.of(
        runCatching { Settings.Global.getString(app.contentResolver, Settings.Global.DEVICE_NAME) }.getOrNull(),
        Build.MANUFACTURER,
        Build.MODEL,
    )

    /**
     * The PIN HostMan on the phone runs behind: the person's when they chose
     * one, else the app's own secret, made once and kept in private storage.
     */
    fun serverPin(c: AppConfig = load()): String {
        c.pin.trim().takeIf { it.isNotEmpty() }?.let { return it }
        prefs.getString(KEY_SECRET, null)?.takeIf { it.isNotBlank() }?.let { return it }
        val secret = SessionLogin.newSecret()
        prefs.edit().putString(KEY_SECRET, secret).apply()
        return secret
    }

    /** Whether to follow deployments in a notification (on unless turned off). */
    val followDeployments: Boolean get() = prefs.getBoolean(KEY_FOLLOW, true)

    companion object {
        const val FILE = "hostman"
        const val KEY_MODE = "mode"
        const val KEY_ADDRESS = "server_address"
        const val KEY_PORT = "local_port"
        const val KEY_SHARE = "share_on_network"
        const val KEY_KEEP = "keep_running"
        const val KEY_MACHINE = "machine_name"
        const val KEY_PIN = "pin"
        const val KEY_FOLLOW = "follow_deployments"
        private const val KEY_SECRET = "server_secret"
        const val MODE_REMOTE = "remote"
        const val MODE_ON_PHONE = "on_phone"

        fun modeValue(m: AppMode) = if (m == AppMode.REMOTE) MODE_REMOTE else MODE_ON_PHONE
    }
}
