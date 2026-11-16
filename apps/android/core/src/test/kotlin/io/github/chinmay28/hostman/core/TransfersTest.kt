package io.github.chinmay28.hostman.core

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.ByteArrayOutputStream
import java.util.Base64

class TransfersTest {
    private class MemorySink : TransferSink<ByteArray> {
        override val output = ByteArrayOutputStream()
        var discarded = false
        override fun commit(): ByteArray = output.toByteArray()
        override fun discard() { discarded = true }
    }

    private val sinks = mutableListOf<MemorySink>()
    private val transfers = Transfers(maxBytes = 1_000) { MemorySink().also { sinks += it } }
    private fun b64(s: String) = Base64.getEncoder().encodeToString(s.toByteArray())
    private fun meta(size: Long) = TransferMeta("a.txt", "text/plain", size, TransferAction.SAVE)

    private fun expectFailure(block: () -> Unit) {
        try {
            block()
            fail("no TransferException")
        } catch (_: TransferException) {
        }
    }

    @Test
    fun reassemblesChunksInOrder() {
        val id = transfers.begin(meta(11))
        transfers.chunk(id, 0, b64("hello "))
        transfers.chunk(id, 1, b64("world"))
        val (m, bytes) = transfers.end(id)
        assertEquals("a.txt", m.name)
        assertArrayEquals("hello world".toByteArray(), bytes)
        assertEquals(0, transfers.inFlight)
    }

    @Test
    fun anEmptyFileIsFine() {
        val id = transfers.begin(meta(0))
        assertEquals(0, transfers.end(id).second.size)
    }

    @Test
    fun outOfOrderChunksDiscardTheFile() {
        val id = transfers.begin(meta(11))
        expectFailure { transfers.chunk(id, 1, b64("world")) }
        assertTrue(sinks.single().discarded)
        expectFailure { transfers.chunk(id, 0, b64("hello")) }
    }

    @Test
    fun moreThanAnnouncedIsRefused() {
        val id = transfers.begin(meta(3))
        expectFailure { transfers.chunk(id, 0, b64("four")) }
        assertTrue(sinks.single().discarded)
    }

    @Test
    fun lessThanAnnouncedIsRefusedAtTheEnd() {
        val id = transfers.begin(meta(5))
        transfers.chunk(id, 0, b64("abc"))
        expectFailure { transfers.end(id) }
        assertTrue(sinks.single().discarded)
    }

    @Test
    fun badBase64AndUnknownIds() {
        val id = transfers.begin(meta(5))
        expectFailure { transfers.chunk(id, 0, "***") }
        expectFailure { transfers.chunk("t99", 0, b64("x")) }
        expectFailure { transfers.end("t99") }
    }

    @Test
    fun tooLargeIsRefusedUpFront() {
        expectFailure { transfers.begin(meta(1_001)) }
        assertEquals(0, sinks.size)
    }

    @Test
    fun abortAllDiscardsEverything() {
        transfers.begin(meta(5))
        transfers.begin(meta(5))
        transfers.abortAll()
        assertEquals(0, transfers.inFlight)
        assertTrue(sinks.all { it.discarded })
    }

    @Test
    fun aSinkThatCannotCommitIsDiscarded() {
        val broken = object : TransferSink<Unit> {
            override val output = ByteArrayOutputStream()
            var discarded = false
            override fun commit() = throw IllegalStateException("disk full")
            override fun discard() { discarded = true }
        }
        val t = Transfers(100) { broken }
        val id = t.begin(meta(0))
        try {
            t.end(id)
            fail()
        } catch (e: TransferException) {
            assertEquals("disk full", e.message)
        }
        assertTrue(broken.discarded)
    }
}
