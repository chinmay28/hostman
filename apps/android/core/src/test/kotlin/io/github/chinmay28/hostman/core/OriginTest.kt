package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class OriginTest {

    private fun parsed(input: String) = Origin.parse(input).getOrThrow().toString()

    @Test
    fun aBareHostIsHostMansDefaultPortOverHttp() {
        assertEquals("http://nakedpi.local:8899", parsed("nakedpi.local"))
        assertEquals("http://192.168.1.20:8899", parsed(" 192.168.1.20 "))
        assertEquals("http://192.168.1.20:9000", parsed("192.168.1.20:9000"))
    }

    @Test
    fun aSchemeSpelledOutKeepsItsOwnDefaultPort() {
        assertEquals("http://pi.local", parsed("http://pi.local"))
        assertEquals("https://hostman.example.com", parsed("https://hostman.example.com"))
        assertEquals("https://hostman.example.com:8443", parsed("HTTPS://HostMan.Example.com:8443/"))
    }

    @Test
    fun pathQueryAndFragmentAreDropped() {
        assertEquals("https://hostman.example.com", parsed("https://hostman.example.com/some/folder?x=1#y"))
    }

    @Test
    fun ipv6IsBracketed() {
        val o = Origin.parse("[fd00::20]").getOrThrow()
        assertEquals("fd00::20", o.host)
        assertEquals("http://[fd00::20]:8899", o.toString())
        assertEquals("http://[fd00::20]:8899/api/health", o.url("api/health"))
    }

    @Test
    fun badAddressesAreRefusedWithAReason() {
        for (bad in listOf("", "   ", "ftp://pi.local", "https://user:pw@hostman.example.com", "http://", "http://pi.local:99999")) {
            assertTrue("accepted $bad", Origin.parse(bad).isFailure)
        }
        assertTrue(Origin.parse("").exceptionOrNull()!!.message!!.isNotBlank())
    }

    @Test
    fun containsIsSameOrigin() {
        val o = Origin.parse("http://pi.local:8899").getOrThrow()
        assertTrue(o.contains("http://pi.local:8899/api/files?path=/"))
        assertTrue(o.contains("http://PI.local:8899"))
        assertFalse(o.contains("http://pi.local:8900/"))
        assertFalse(o.contains("https://pi.local:8899/"))
        assertFalse(o.contains("http://pi.local.evil.test:8899/"))
        assertFalse(o.contains("http://evil.test/?http://pi.local:8899"))
        assertFalse(o.contains("not a url"))
    }

    @Test
    fun ofReadsDefaultPorts() {
        assertEquals(Origin("https", "a.test", 443), Origin.of("https://a.test/x"))
        assertEquals(Origin("http", "a.test", 80), Origin.of("http://a.test"))
        assertNull(Origin.of("blob:http://a.test/123"))
        assertNull(Origin.of("mailto:me@a.test"))
    }

    @Test
    fun loopbackIsTheFullStackServer() {
        assertEquals("http://127.0.0.1:8899", Origin.loopback(Origin.DEFAULT_PORT).toString())
    }
}
