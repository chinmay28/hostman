package io.github.chinmay28.hostman.core

import java.util.Locale

/**
 * Turns an <input accept="…"> into the MIME types Android's file picker
 * understands.
 *
 * A page may write extensions as well as types — ".zip,.conf,application/zip"
 * — and a picker handed ".zip" as a type offers nothing at all. Extensions
 * are looked up; one with no known type (".conf") cannot be said to the
 * picker, so the answer is then "anything", and the page checks what it is
 * given, as it does in a browser. HostMan's own upload takes any file.
 */
object AcceptTypes {
    /** The types to ask for; empty means any file. [lookup] maps "zip" to "application/zip". */
    fun mimeTypes(accept: List<String>, lookup: (String) -> String?): List<String> {
        val out = LinkedHashSet<String>()
        for (entry in accept.flatMap { it.split(',') }) {
            val a = entry.trim().lowercase(Locale.ROOT)
            when {
                a.isEmpty() -> continue
                a == "*/*" || a == "*" -> return emptyList()
                a.startsWith(".") -> out += lookup(a.drop(1))?.lowercase(Locale.ROOT) ?: return emptyList()
                Regex("[a-z0-9.+-]+/([a-z0-9.+-]+|\\*)").matches(a) -> out += a
                else -> return emptyList()
            }
        }
        return out.toList()
    }
}
