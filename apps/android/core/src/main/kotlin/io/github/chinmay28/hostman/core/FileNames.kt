package io.github.chinmay28.hostman.core

import java.net.URI
import java.net.URLDecoder
import java.nio.charset.StandardCharsets

/** Names for files the app saves into Downloads. */
object FileNames {
    private const val MAX_BYTES = 180

    /**
     * A name safe to give the phone's storage: no folders, no control
     * characters, nothing hidden, not too long — the extension kept when the
     * name has to be cut. Never empty.
     */
    fun sanitize(name: String?, fallback: String = "download"): String {
        var s = (name ?: "")
            .substringAfterLast('/')
            .substringAfterLast('\\')
            .filter { !it.isISOControl() && it !in "<>:\"|?*" }
            .trim()
            .trimStart('.')
            .trim()
        if (s.isEmpty()) s = fallback
        if (s.toByteArray(StandardCharsets.UTF_8).size <= MAX_BYTES) return s

        val dot = s.lastIndexOf('.')
        val ext = if (dot > 0 && s.length - dot <= 10) s.substring(dot) else ""
        var stem = s.substring(0, s.length - ext.length)
        while (stem.isNotEmpty() && (stem + ext).toByteArray(StandardCharsets.UTF_8).size > MAX_BYTES) {
            stem = stem.dropLast(1)
        }
        return (stem.trimEnd() + ext).ifEmpty { fallback }
    }

    /**
     * The name a download should get: the page's own when it gave one, else
     * the server's Content-Disposition, else the last part of the address.
     */
    fun forDownload(suggested: String?, contentDisposition: String?, url: String): String {
        suggested?.takeIf { it.isNotBlank() }?.let { return sanitize(it) }
        fromContentDisposition(contentDisposition)?.let { return sanitize(it) }
        val last = try {
            URI(url).path?.substringAfterLast('/')
        } catch (_: Exception) {
            null
        }
        return sanitize(last?.let { decode(it) })
    }

    /**
     * The filename in a Content-Disposition header: filename* (RFC 5987,
     * UTF-8) when there is one, as the server sends for any name that is not
     * plain ASCII, else filename.
     */
    fun fromContentDisposition(header: String?): String? {
        if (header.isNullOrBlank()) return null
        val params = splitParams(header)
        params["filename*"]?.let { ext ->
            val parts = ext.split('\'', limit = 3)
            if (parts.size == 3) {
                val charset = runCatching { charset(parts[0].ifBlank { "UTF-8" }) }.getOrDefault(StandardCharsets.UTF_8)
                return runCatching { URLDecoder.decode(parts[2].replace("+", "%2B"), charset) }.getOrNull()
            }
        }
        return params["filename"]?.takeIf { it.isNotBlank() }
    }

    private fun splitParams(header: String): Map<String, String> {
        val out = mutableMapOf<String, String>()
        var i = header.indexOf(';')
        while (i in header.indices) {
            val eq = header.indexOf('=', i)
            if (eq < 0) break
            val key = header.substring(i + 1, eq).trim().lowercase()
            var j = eq + 1
            while (j < header.length && header[j] == ' ') j++
            val value: String
            if (j < header.length && header[j] == '"') {
                val sb = StringBuilder()
                j++
                while (j < header.length && header[j] != '"') {
                    if (header[j] == '\\' && j + 1 < header.length) j++
                    sb.append(header[j])
                    j++
                }
                value = sb.toString()
                i = header.indexOf(';', j)
            } else {
                val end = header.indexOf(';', j).let { if (it < 0) header.length else it }
                value = header.substring(j, end).trim()
                i = if (end < header.length) end else -1
            }
            out.putIfAbsent(key, value)
        }
        return out
    }

    private fun decode(s: String): String = runCatching { URLDecoder.decode(s.replace("+", "%2B"), StandardCharsets.UTF_8) }.getOrDefault(s)
}
