package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class DiscoveryProtocolTest {
    @Test
    fun readsAnAnnouncement() {
        val f = DiscoveryProtocol.parse("""{"service":"hostman","name":"nakedpi","port":8899,"version":"v2026.11.240"}""", "/192.168.1.20")!!
        assertEquals("nakedpi", f.name)
        assertEquals("http://192.168.1.20:8899", f.origin.toString())
        assertEquals("v2026.11.240", f.version)
    }

    @Test
    fun secureMeansHttpsAndAMissingNameIsTheHost() {
        val f = DiscoveryProtocol.parse("""{"service":"hostman","port":443,"secure":true}""", "fd00::20%wlan0")!!
        assertEquals("https://[fd00::20]", f.origin.toString())
        assertEquals("fd00::20", f.name)
    }

    /** The constants are the Go responder's; a change on one side alone breaks the search. */
    @Test
    fun matchesTheServer() {
        // Tests run in apps/android/core.
        val go = java.io.File("../../../server/internal/discovery/discovery.go").readText()
        assertTrue("PORT differs from discovery.DefaultPort", go.contains("const DefaultPort = ${DiscoveryProtocol.PORT}"))
        assertTrue("QUERY differs from discovery.Query", go.contains("const Query = \"${DiscoveryProtocol.QUERY}\""))
        assertTrue("SERVICE differs from discovery.Service", go.contains("const Service = \"${DiscoveryProtocol.SERVICE}\""))
    }

    @Test
    fun ignoresAnythingElse() {
        assertNull(DiscoveryProtocol.parse("""{"service":"other","port":8899}""", "10.0.0.1"))
        assertNull(DiscoveryProtocol.parse("""{"service":"hostman","port":0}""", "10.0.0.1"))
        assertNull(DiscoveryProtocol.parse("HOSTMAN-DISCOVER/1", "10.0.0.1"))
        assertNull(DiscoveryProtocol.parse("""{"service":"hostman","port":8899}""", ""))
    }
}
