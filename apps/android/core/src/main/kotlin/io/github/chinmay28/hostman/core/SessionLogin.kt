package io.github.chinmay28.hostman.core

import org.json.JSONObject
import java.security.SecureRandom
import java.util.Base64

/**
 * Signing the app's own web view in to HostMan on the phone.
 *
 * HostMan on the phone always runs behind a PIN (see [ServerLaunch.pin]):
 * every app on a phone can reach 127.0.0.1, and this server holds a key that
 * runs commands as root on every host. Unless the person chose a PIN for
 * other devices, nobody knows it but the app, so the app signs in itself —
 * `POST /api/session`, as the page's own PIN screen does — and hands the
 * session cookie to the web view before it opens the page.
 */
object SessionLogin {
    /** The cookie server/internal/api sets on a good PIN. */
    const val COOKIE = "hostman_session"

    /** The body of `POST /api/session`. */
    fun body(pin: String): String = JSONObject().put("pin", pin).toString()

    /** Whether `GET /api/session`'s answer says this client is already signed in. */
    fun authenticated(body: String): Boolean = try {
        JSONObject(body).optBoolean("authenticated", false)
    } catch (_: Exception) {
        false
    }

    /** The one of a response's Set-Cookie headers that carries the session, as sent. */
    fun sessionCookie(setCookies: List<String>?): String? =
        setCookies.orEmpty().firstOrNull { it.trimStart().startsWith("$COOKIE=") && it.substringAfter('=').substringBefore(';').isNotBlank() }

    /** A secret for the app to guard the server with: 32 random bytes, URL-safe. */
    fun newSecret(random: SecureRandom = SecureRandom()): String {
        val bytes = ByteArray(32).also(random::nextBytes)
        return Base64.getUrlEncoder().withoutPadding().encodeToString(bytes)
    }
}
