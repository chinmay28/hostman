package io.github.chinmay28.hostman.core

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class SessionLoginTest {
    @Test
    fun theLoginBodyCarriesThePin() {
        assertEquals("1234\"x", JSONObject(SessionLogin.body("1234\"x")).getString("pin"))
    }

    @Test
    fun readsTheSessionStatus() {
        assertTrue(SessionLogin.authenticated("""{"required":true,"authenticated":true}"""))
        assertFalse(SessionLogin.authenticated("""{"required":true,"authenticated":false}"""))
        assertFalse(SessionLogin.authenticated("nope"))
    }

    @Test
    fun picksTheSessionCookie() {
        val cookie = "hostman_session=abc; Path=/; Expires=Wed, 16 Dec 2026 10:00:00 GMT; HttpOnly; SameSite=Lax"
        assertEquals(cookie, SessionLogin.sessionCookie(listOf("other=1; Path=/", cookie)))
        assertNull(SessionLogin.sessionCookie(listOf("hostman_session=; Path=/")))
        assertNull(SessionLogin.sessionCookie(null))
    }

    @Test
    fun secretsAreLongAndFresh() {
        val a = SessionLogin.newSecret()
        assertEquals(43, a.length)
        assertNotEquals(a, SessionLogin.newSecret())
        assertTrue(AppConfig.pinGoodEnough(a))
    }

    /** The cookie's name is the server's. */
    @Test
    fun matchesTheServer() {
        // Tests run in apps/android/core.
        val auth = File("../../../server/internal/api/auth.go").readText()
        assertTrue(auth.contains("const sessionCookie = \"${SessionLogin.COOKIE}\""))
    }
}
