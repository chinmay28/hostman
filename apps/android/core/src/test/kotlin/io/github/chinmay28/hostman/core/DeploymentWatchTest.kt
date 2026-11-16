package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class DeploymentWatchTest {
    private fun snapshot(json: String) = DeploymentSnapshot.parse(json)!!

    // The shape server/internal/store.Deployment is written in.
    private val running = """{"id":41,"appId":2,"hostId":3,"command":"curl … | sudo bash","params":{},"kind":"install",
        "status":"running","exitCode":null,"error":"","log":"== fetching\n== building\n","startedAt":"2026-11-16T10:00:00Z",
        "finishedAt":null,"appName":"CountRoster","hostName":"nakedpi"}"""

    @Test
    fun readsARunningDeployment() {
        val s = snapshot(running)
        assertEquals(41L, s.id)
        assertTrue(s.running)
        assertNull(s.exitCode)
        assertEquals("Deploying CountRoster to nakedpi", s.title)
        assertEquals("== building", s.text)
    }

    @Test
    fun saysHowItEnded() {
        val ok = snapshot(running.replace("\"running\"", "\"succeeded\"").replace("\"exitCode\":null", "\"exitCode\":0"))
        assertEquals("Deployed CountRoster to nakedpi", ok.title)
        assertEquals(0, ok.exitCode)

        val failed = snapshot(running.replace("\"running\"", "\"failed\"").replace("\"exitCode\":null", "\"exitCode\":2"))
        assertEquals("Deploying CountRoster to nakedpi failed", failed.title)
        assertEquals("Exited with status 2", failed.text)
        assertEquals("ssh: no route", snapshot(running.replace("\"running\"", "\"failed\"").replace("\"error\":\"\"", "\"error\":\"ssh: no route\"")).text)

        val removed = snapshot(running.replace("\"install\"", "\"uninstall\"").replace("\"running\"", "\"succeeded\""))
        assertEquals("Removed CountRoster from nakedpi", removed.title)
        assertEquals("Removing CountRoster from nakedpi was canceled", snapshot(running.replace("\"install\"", "\"uninstall\"").replace("\"running\"", "\"canceled\"")).title)
        assertEquals("Deploying CountRoster to nakedpi was interrupted", snapshot(running.replace("\"running\"", "\"interrupted\"")).title)
    }

    @Test
    fun refusesWhatIsNotADeployment() {
        assertNull(DeploymentSnapshot.parse("""{"error":"not found"}"""))
        assertNull(DeploymentSnapshot.parse("<html>"))
        assertNull(DeploymentSnapshot.parse("""{"id":0,"status":"running"}"""))
    }

    @Test
    fun theLastLineIsWhatATerminalWouldShow() {
        assertEquals("", DeploymentSnapshot.lastLine(""))
        assertEquals("done", DeploymentSnapshot.lastLine("a\ndone\n\n  \n"))
        // A progress bar redraws its line with carriage returns.
        assertEquals("100%", DeploymentSnapshot.lastLine("downloading\r 10%\r 50%\r100%\n"))
        assertEquals("ok green", DeploymentSnapshot.lastLine("\u001B[32mok green\u001B[0m\n"))
        val long = DeploymentSnapshot.lastLine("x".repeat(500), max = 20)
        assertEquals(20, long.length)
        assertTrue(long.endsWith("…"))
    }

    @Test
    fun pollsWhileRunningAndStopsAtTheEnd() {
        val w = DeploymentWatch(intervalMs = 3_000, startedAt = 0)
        assertEquals(WatchStep.Again(3_000), w.answered(snapshot(running), 1_000))
        val done = snapshot(running.replace("\"running\"", "\"succeeded\""))
        assertEquals(WatchStep.Finished(done), w.answered(done, 2_000))
        assertTrue(w.gone() is WatchStep.GiveUp)
    }

    /** Updating HostMan restarts it mid-deployment: that is a wait, not an ending. */
    @Test
    fun anUnreachableServerIsWaitedForThenGivenUpOn() {
        val w = DeploymentWatch(unreachableIntervalMs = 10_000, patienceMs = 60_000, startedAt = 0)
        assertEquals(WatchStep.Again(10_000), w.unreachable(30_000))
        w.answered(snapshot(running), 50_000)
        assertEquals(WatchStep.Again(10_000), w.unreachable(100_000))
        assertTrue(w.unreachable(110_000) is WatchStep.GiveUp)
    }
}
