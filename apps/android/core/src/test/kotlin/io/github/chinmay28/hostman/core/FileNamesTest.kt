package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class FileNamesTest {
    @Test
    fun sanitize() {
        assertEquals("passport.pdf", FileNames.sanitize("passport.pdf"))
        assertEquals("passwd", FileNames.sanitize("../../etc/passwd"))
        assertEquals("evil.txt", FileNames.sanitize("C:\\Users\\evil.txt"))
        assertEquals("bashrc", FileNames.sanitize(".bashrc"))
        assertEquals("ab.txt", FileNames.sanitize("a\u0000b\n.txt"))
        assertEquals("what.txt", FileNames.sanitize("what?.txt"))
        assertEquals("download", FileNames.sanitize("  "))
        assertEquals("download", FileNames.sanitize(null))
        assertEquals("kit.zip", FileNames.sanitize("", "kit.zip"))
    }

    @Test
    fun longNamesKeepTheirExtension() {
        val s = FileNames.sanitize("é".repeat(200) + ".mkv")
        assertTrue(s.endsWith(".mkv"))
        assertTrue(s.toByteArray().size <= 180)
    }

    @Test
    fun contentDisposition() {
        assertEquals("a b.zip", FileNames.fromContentDisposition("attachment; filename=\"a b.zip\""))
        assertEquals("plain.zip", FileNames.fromContentDisposition("attachment; filename=plain.zip"))
        assertEquals("Photos – 2026.zip", FileNames.fromContentDisposition(
            "attachment; filename=\"Photos - 2026.zip\"; filename*=UTF-8''Photos%20%E2%80%93%202026.zip"))
        assertEquals("q\"uote.txt", FileNames.fromContentDisposition("attachment; filename=\"q\\\"uote.txt\""))
        assertNull(FileNames.fromContentDisposition("inline"))
        assertNull(FileNames.fromContentDisposition(null))
    }

    @Test
    fun forDownloadPrefersThePageThenTheServerThenTheAddress() {
        assertEquals("mine.zip", FileNames.forDownload("mine.zip", "attachment; filename=theirs.zip", "http://h/x.zip"))
        assertEquals("theirs.zip", FileNames.forDownload("", "attachment; filename=theirs.zip", "http://h/x.zip"))
        assertEquals("Holiday photos.zip", FileNames.forDownload(null, null, "http://h/api/zip/Holiday%20photos.zip?t=1"))
        assertEquals("download", FileNames.forDownload(null, null, "http://h/"))
    }
}
