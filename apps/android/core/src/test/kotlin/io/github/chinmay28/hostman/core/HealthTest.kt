package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class HealthTest {
    @Test
    fun readsTheVersion() {
        assertEquals("v2026.11.240", Health.version("""{"status":"ok","version":"v2026.11.240"}"""))
        assertEquals("unknown", Health.version("""{"status":"ok"}"""))
        assertNull(Health.version("""{"status":"locked"}"""))
        assertNull(Health.version("<html>nginx</html>"))
    }

    @Test
    fun awaitPollsUntilAnswered() {
        var now = 0L
        var tries = 0
        val got = Health.await(timeoutMs = 10_000, intervalMs = 100, clock = { now }, sleep = { now += it }) {
            tries++
            if (tries == 4) "v1" else null
        }
        assertEquals("v1", got)
        assertEquals(4, tries)
        assertEquals(300L, now)
    }

    @Test
    fun awaitGivesUpAtTheDeadline() {
        var now = 0L
        val got = Health.await(timeoutMs = 1_000, intervalMs = 300, clock = { now }, sleep = { now += it }) { null }
        assertNull(got)
        assertEquals(1_200L, now)
    }

    @Test
    fun awaitStopsWhenNoLongerWanted() {
        var tries = 0
        val got = Health.await(timeoutMs = 10_000, sleep = {}, stillWanted = { tries < 2 }) { tries++; null }
        assertNull(got)
        assertEquals(2, tries)
    }
}
