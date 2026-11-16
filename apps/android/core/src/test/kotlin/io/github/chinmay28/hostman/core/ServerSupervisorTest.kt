package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.TimeUnit

class ServerSupervisorTest {
    @get:Rule
    val tmp = TemporaryFolder()

    /** A stand-in for hostman: a shell script that ignores its arguments. */
    private fun script(body: String): File = tmp.newFile("fake-hostman.sh").apply {
        writeText("#!/bin/sh\n$body\n")
        setExecutable(true)
    }

    private fun launch(binary: File) = ServerLaunch(
        binary = binary,
        dataDir = File(tmp.root, "data"),
        cacheDir = File(tmp.root, "cache"),
        port = 8899,
        shareOnNetwork = false,
        machineName = "test",
        dnsFile = File(tmp.root, "dns"),
        timeZone = "UTC",
        pin = "test-pin",
    )

    private fun waitFor(timeoutMs: Long = 10_000, cond: () -> Boolean) {
        val deadline = System.currentTimeMillis() + timeoutMs
        while (!cond()) {
            if (System.currentTimeMillis() > deadline) throw AssertionError("timed out")
            Thread.sleep(20)
        }
    }

    @Test
    fun restartPolicyBacksOffThenGivesUp() {
        val p = RestartPolicy(maxCrashes = 3, windowMs = 1_000, firstDelayMs = 100, maxDelayMs = 250)
        assertEquals(100L, p.nextDelay(listOf(0), 0))
        assertEquals(200L, p.nextDelay(listOf(0, 10), 10))
        assertNull(p.nextDelay(listOf(0, 10, 20), 20))
        // Old crashes fall out of the window.
        assertEquals(100L, p.nextDelay(listOf(0, 10, 2_000), 2_000))
        assertEquals(250L, RestartPolicy(maxCrashes = 10, firstDelayMs = 100, maxDelayMs = 250).nextDelay(List(5) { 0L }, 0))
    }

    @Test
    fun runsTheServerWithItsEnvironmentAndStopsIt() {
        val bin = script("echo \"home=\$HOME pin=\$HOSTMAN_PIN args=\$*\"\ntrap 'echo bye; exit 0' TERM\nwhile true; do sleep 0.05; done")
        val log = File(tmp.root, "logs/server.log")
        val states = CopyOnWriteArrayList<ServerState>()
        val sup = ServerSupervisor({ launch(bin) }, log, { states += it }, healthy = { "v-test" })

        sup.start()
        waitFor { sup.state is ServerState.Running }
        assertEquals(ServerState.Running("v-test"), sup.state)
        waitFor { log.readText().contains("home=") }
        val out = log.readText()
        assertTrue(out, out.contains("home=${File(tmp.root, "data").absolutePath} pin=test-pin"))
        assertTrue(out, out.contains("args=-addr 127.0.0.1:8899 -db"))

        sup.stop()
        assertEquals(ServerState.Stopped, sup.state)
        waitFor { log.readText().contains("bye") }
        assertEquals(ServerState.Starting, states.first())
    }

    @Test
    fun aServerThatKeepsDyingIsLeftDown() {
        val bin = script("echo dying; exit 3")
        val log = File(tmp.root, "server.log")
        val states = CopyOnWriteArrayList<ServerState>()
        val sup = ServerSupervisor(
            // Like Health.await: waits while the process lives, and gives up when it dies.
            { launch(bin) }, log, { states += it }, healthy = { alive -> while (alive()) Thread.sleep(5); null },
            policy = RestartPolicy(maxCrashes = 3, windowMs = 60_000, firstDelayMs = 10, maxDelayMs = 20),
        )
        sup.start()
        waitFor { sup.state is ServerState.Failed }
        val failed = sup.state as ServerState.Failed
        assertEquals(3, failed.exitCode)
        assertTrue(failed.logTail, failed.logTail.contains("dying"))
        assertEquals(2, states.count { it is ServerState.Restarting })
        assertTrue(!sup.running)
    }

    @Test
    fun aServerThatNeverAnswersIsRestarted() {
        val bin = script("echo up; while true; do sleep 0.05; done")
        val log = File(tmp.root, "server.log")
        var answered = 0
        val sup = ServerSupervisor(
            { launch(bin) }, log, {},
            healthy = { if (++answered >= 2) "v2" else null },
            policy = RestartPolicy(firstDelayMs = 10),
        )
        sup.start()
        waitFor { sup.state is ServerState.Running }
        assertTrue(log.readText().contains("did not answer in time"))
        sup.stop()
    }

    @Test
    fun aMissingBinaryFailsAtOnce() {
        val sup = ServerSupervisor({ launch(File(tmp.root, "absent")) }, File(tmp.root, "server.log"), {}, healthy = { "x" })
        sup.start()
        waitFor { sup.state is ServerState.Failed }
        assertTrue((sup.state as ServerState.Failed).logTail.contains("could not start"))
    }

    @Test
    fun theLogIsRolledOverWhenLarge() {
        val bin = script("echo fresh; while true; do sleep 0.05; done")
        val log = File(tmp.root, "server.log").apply { writeText("x".repeat(200)) }
        val sup = ServerSupervisor({ launch(bin) }, log, {}, healthy = { "v" }, maxLogBytes = 100)
        sup.start()
        waitFor { sup.state is ServerState.Running }
        sup.stop()
        assertEquals("x".repeat(200), File(log.path + ".1").readText())
        assertTrue(!log.readText().contains("xxx"))
    }

    @Test
    fun staleServersAreFoundByTheirProgram() {
        val proc = tmp.newFolder("proc")
        val bin = File("/data/app/lib/arm64/libhostman.so")
        fun proc(pid: String, argv: String?) {
            val d = File(proc, pid).apply { mkdirs() }
            if (argv != null) File(d, "cmdline").writeBytes(argv.replace(' ', '\u0000').toByteArray())
        }
        proc("101", "/data/app/lib/arm64/libhostman.so -addr 127.0.0.1:8899")
        proc("102", "/system/bin/app_process io.github.chinmay28.hostman")
        proc("103", null)
        proc("self", "/data/app/lib/arm64/libhostman.so")
        proc("104", "/data/app/lib/arm64/libhostman.so -v")
        assertEquals(listOf(101), StaleServers.find(proc, bin, self = 104))
        assertEquals(listOf(101, 104), StaleServers.find(proc, bin))
        assertEquals(emptyList<Int>(), StaleServers.find(File(tmp.root, "nope"), bin))
    }

    @Test(timeout = 20_000)
    fun stopKillsAServerThatIgnoresSigterm() {
        val bin = script("trap '' TERM\nwhile true; do sleep 0.05; done")
        val sup = ServerSupervisor({ launch(bin) }, File(tmp.root, "server.log"), {}, healthy = { "v" }, stopGraceMs = 300)
        sup.start()
        waitFor { sup.state is ServerState.Running }
        val t0 = System.nanoTime()
        sup.stop()
        assertTrue(TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - t0) < 10_000)
        assertEquals(ServerState.Stopped, sup.state)
    }
}
