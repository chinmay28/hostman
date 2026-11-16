package io.github.chinmay28.hostman.core

import java.io.OutputStream
import java.util.Base64

/** A file the page is sending, as announced by [BridgeRequest.Begin]. */
data class TransferMeta(val name: String, val mime: String, val size: Long, val action: TransferAction)

/** Where one transfer's bytes go, and what becomes of them. */
interface TransferSink<T> {
    val output: OutputStream

    /** Every byte has arrived: make the file what it is for. */
    fun commit(): T

    /** The transfer failed or was abandoned: throw away what arrived. */
    fun discard()
}

class TransferException(message: String) : Exception(message)

/**
 * Puts files back together from the chunks the page sends.
 *
 * Holds the page to what it announced: chunks in order, never more bytes than
 * it said, and exactly that many by the end. A transfer that breaks any of
 * those is discarded, so a half-written file never lands in Downloads.
 *
 * Not thread-safe: the app feeds it from one thread.
 */
class Transfers<T>(
    private val maxBytes: Long,
    private val open: (TransferMeta) -> TransferSink<T>,
) {
    private class Active<T>(val meta: TransferMeta, val sink: TransferSink<T>) {
        var nextSeq = 0
        var received = 0L
    }

    private val active = LinkedHashMap<String, Active<T>>()
    private var counter = 0

    val inFlight: Int get() = active.size

    fun begin(meta: TransferMeta): String {
        if (meta.size < 0) throw TransferException("a file of unknown size")
        if (meta.size > maxBytes) throw TransferException("${meta.name} is too large to hand over (${meta.size} bytes)")
        val id = "t${++counter}"
        active[id] = Active(meta, open(meta))
        return id
    }

    fun chunk(id: String, seq: Int, base64: String) {
        val t = active[id] ?: throw TransferException("no transfer $id")
        try {
            if (seq != t.nextSeq) throw TransferException("chunk $seq arrived when ${t.nextSeq} was expected")
            val bytes = try {
                Base64.getDecoder().decode(base64)
            } catch (e: IllegalArgumentException) {
                throw TransferException("chunk $seq is not base64")
            }
            if (t.received + bytes.size > t.meta.size) {
                throw TransferException("more bytes than the ${t.meta.size} announced")
            }
            t.sink.output.write(bytes)
            t.received += bytes.size
            t.nextSeq++
        } catch (e: Exception) {
            fail(id, t)
            throw e as? TransferException ?: TransferException(e.message ?: "could not write the file")
        }
    }

    /** The last chunk is in: returns what the sink made of it. */
    fun end(id: String): Pair<TransferMeta, T> {
        val t = active[id] ?: throw TransferException("no transfer $id")
        if (t.received != t.meta.size) {
            fail(id, t)
            throw TransferException("${t.received} of ${t.meta.size} bytes arrived")
        }
        active.remove(id)
        return try {
            t.sink.output.close()
            t.meta to t.sink.commit()
        } catch (e: Exception) {
            runCatching { t.sink.discard() }
            throw TransferException(e.message ?: "could not save the file")
        }
    }

    fun abort(id: String) {
        active[id]?.let { fail(id, it) }
    }

    fun abortAll() {
        for ((id, t) in active.entries.toList()) fail(id, t)
    }

    private fun fail(id: String, t: Active<T>) {
        active.remove(id)
        runCatching { t.sink.output.close() }
        runCatching { t.sink.discard() }
    }
}
