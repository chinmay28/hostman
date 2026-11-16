package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Test

class AcceptTypesTest {
    private val known = mapOf("zip" to "application/zip", "pdf" to "application/pdf", "jpg" to "image/jpeg")
    private fun types(vararg accept: String) = AcceptTypes.mimeTypes(accept.toList()) { known[it] }

    @Test
    fun nothingMeansAnything() {
        assertEquals(emptyList<String>(), types())
        assertEquals(emptyList<String>(), types(""))
        assertEquals(emptyList<String>(), types("*/*"))
    }

    @Test
    fun typesPassThroughAndExtensionsAreLookedUp() {
        assertEquals(listOf("image/*"), types("image/*"))
        assertEquals(listOf("application/pdf", "image/jpeg"), types(".PDF, .jpg"))
        assertEquals(listOf("application/zip"), types(".zip,application/zip"))
    }

    @Test
    fun anExtensionWithNoTypeWidensToAnything() {
        // ".conf" has no MIME type to ask the picker for.
        assertEquals(emptyList<String>(), types(".zip,.conf,application/zip"))
        assertEquals(emptyList<String>(), types("not a type"))
    }
}
