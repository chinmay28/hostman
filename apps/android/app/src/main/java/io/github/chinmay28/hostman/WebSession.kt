package io.github.chinmay28.hostman

import android.webkit.CookieManager
import io.github.chinmay28.hostman.core.Origin
import io.github.chinmay28.hostman.core.SessionLogin
import java.net.HttpURLConnection
import java.net.URL

/**
 * Signs the web view in to HostMan on the phone, which always runs behind a
 * PIN the app knows (see [SessionLogin]). Blocks; call it off the main thread.
 */
object WebSession {
    /** True once the web view's cookies carry a session [origin] accepts. */
    fun ensure(origin: Origin, pin: String): Boolean {
        val cookies = CookieManager.getInstance()
        if (signedIn(origin, cookies.getCookie(origin.url("/")))) return true
        val conn = URL(origin.url("/api/session")).openConnection() as HttpURLConnection
        return try {
            conn.requestMethod = "POST"
            conn.connectTimeout = 5_000
            conn.readTimeout = 10_000
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
            conn.outputStream.use { it.write(SessionLogin.body(pin).toByteArray()) }
            if (conn.responseCode != 200) return false
            val cookie = SessionLogin.sessionCookie(conn.headerFields["Set-Cookie"] ?: conn.headerFields["set-cookie"])
                ?: return false
            cookies.setCookie(origin.url("/"), cookie)
            cookies.flush()
            true
        } catch (_: Exception) {
            false
        } finally {
            conn.disconnect()
        }
    }

    private fun signedIn(origin: Origin, cookie: String?): Boolean {
        if (cookie.isNullOrBlank()) return false
        val conn = URL(origin.url("/api/session")).openConnection() as HttpURLConnection
        return try {
            conn.connectTimeout = 5_000
            conn.readTimeout = 10_000
            conn.setRequestProperty("Cookie", cookie)
            conn.responseCode == 200 && SessionLogin.authenticated(conn.inputStream.bufferedReader().readText())
        } catch (_: Exception) {
            false
        } finally {
            conn.disconnect()
        }
    }
}
