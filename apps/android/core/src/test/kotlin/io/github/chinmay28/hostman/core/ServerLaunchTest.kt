package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class ServerLaunchTest {
    private val launch = ServerLaunch(
        binary = File("/data/app/lib/arm64/libhostman.so"),
        dataDir = File("/data/user/0/io.github.chinmay28.hostman/files/hostman"),
        cacheDir = File("/data/user/0/io.github.chinmay28.hostman/cache/hostman"),
        port = 8899,
        shareOnNetwork = false,
        machineName = "Pixel 9",
        dnsFile = File("/data/user/0/io.github.chinmay28.hostman/files/dns"),
        timeZone = "Europe/Berlin",
        pin = "s3cret-from-the-app",
    )

    @Test
    fun loopbackByDefaultWithNoDiscoveryAndLocalForwards() {
        assertEquals(
            listOf(
                "/data/app/lib/arm64/libhostman.so",
                "-addr", "127.0.0.1:8899",
                "-db", "/data/user/0/io.github.chinmay28.hostman/files/hostman/hostman.db",
                "-managed-by", "the HostMan Android app",
                "-forward-bind", "127.0.0.1",
                "-discovery-port", "0",
                "-name", "Pixel 9",
            ),
            launch.command(),
        )
    }

    @Test
    fun sharingBindsEveryInterfaceAndAnswersDiscovery() {
        val cmd = launch.copy(shareOnNetwork = true, port = 9000).command()
        assertEquals("0.0.0.0:9000", cmd[cmd.indexOf("-addr") + 1])
        assertEquals("8900", cmd[cmd.indexOf("-discovery-port") + 1])
        assertFalse("forwards stay on every interface", cmd.contains("-forward-bind"))
    }

    @Test
    fun aBlankNameIsLeftToTheServer() {
        assertFalse(launch.copy(machineName = " ").command().contains("-name"))
    }

    @Test
    fun environmentPointsGoAtTheAppsOwnDirectories() {
        val env = launch.environment()
        assertEquals("/data/user/0/io.github.chinmay28.hostman/files/hostman", env["HOME"])
        assertEquals("/data/user/0/io.github.chinmay28.hostman/cache/hostman", env["TMPDIR"])
        assertEquals("/data/user/0/io.github.chinmay28.hostman/files/dns", env["HOSTMAN_DNS_FILE"])
        assertEquals("1.1.1.1,8.8.8.8", env["HOSTMAN_DNS"])
        assertEquals("Europe/Berlin", env["HOSTMAN_TZ"])
        assertEquals("hostman", env["USER"])
    }

    /** The PIN travels in the environment, never on the command line. */
    @Test
    fun thePinIsInTheEnvironmentOnly() {
        assertEquals("s3cret-from-the-app", launch.environment()["HOSTMAN_PIN"])
        assertTrue(launch.command().none { it.contains("s3cret") })
    }

    @Test(expected = IllegalArgumentException::class)
    fun anOpenServerIsRefused() {
        launch.copy(pin = "")
    }

    @Test
    fun aBlankZoneIsLeftUnset() {
        assertFalse(launch.copy(timeZone = "").environment().containsKey("HOSTMAN_TZ"))
    }

    /** Every flag passed is one the server declares. */
    @Test
    fun theFlagsAreTheServers() {
        // Tests run in apps/android/core.
        val main = File("../../../server/cmd/hostman/main.go").readText()
        val flags = launch.copy(shareOnNetwork = false).command().filter { it.startsWith("-") }
        for (flag in flags) {
            assertTrue("$flag is not a hostman flag", main.contains("\"${flag.removePrefix("-")}\""))
        }
        for (env in listOf("HOSTMAN_PIN")) assertTrue("$env is not read by hostman", main.contains("\"$env\""))
        val hostenv = File("../../../server/internal/hostenv/hostenv.go").readText()
        for (env in listOf("HOSTMAN_DNS_FILE", "HOSTMAN_DNS", "HOSTMAN_TZ")) {
            assertTrue("$env is not read by hostenv", hostenv.contains("\"$env\""))
        }
    }

    @Test
    fun dnsListDropsScopesAndRepeats() {
        assertEquals(
            "# DNS servers of the phone's current network, written by the HostMan app.\n" +
                "192.168.1.1\nfe80::1\n2001:db8::53\n",
            DnsList.render(listOf("/192.168.1.1", "fe80::1%wlan0", "192.168.1.1", " 2001:db8::53 ", "")),
        )
    }

    @Test
    fun machineNames() {
        assertEquals("Chinmay's phone", MachineName.of("Chinmay's phone", "Google", "Pixel 9"))
        assertEquals("Google Pixel 9", MachineName.of(null, "Google", "Pixel 9"))
        assertEquals("Samsung SM-S921B", MachineName.of("", "Samsung", "SM-S921B"))
        assertEquals("Google Pixel", MachineName.of(null, "Google", "Google Pixel"))
        assertEquals("phone", MachineName.of(null, null, null))
        assertEquals("a b", MachineName.of("a\n  b\u0007", null, null))
        assertEquals(48, MachineName.of("x".repeat(100), null, null).length)
    }
}
