package io.github.chinmay28.hostman.core

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class BridgeTest {
    @Test
    fun parsesEachMessage() {
        assertEquals(BridgeRequest.Hello(1), BridgeRequest.parse("""{"t":"hello","re":1}""").getOrThrow())
        assertEquals(BridgeRequest.OpenSettings(0), BridgeRequest.parse("""{"t":"settings"}""").getOrThrow())
        assertEquals(
            BridgeRequest.Begin(2, "a.pdf", "application/pdf", 10, TransferAction.SAVE),
            BridgeRequest.parse("""{"t":"begin","re":2,"name":"a.pdf","mime":"application/pdf","size":10,"action":"save"}""").getOrThrow(),
        )
        assertEquals(
            BridgeRequest.Begin(2, "", "application/octet-stream", 0, TransferAction.STAGE),
            BridgeRequest.parse("""{"t":"begin","re":2,"size":0,"action":"stage"}""").getOrThrow(),
        )
        assertEquals(BridgeRequest.Chunk(3, "t1", 0, "AAAA"), BridgeRequest.parse("""{"t":"chunk","re":3,"id":"t1","seq":0,"data":"AAAA"}""").getOrThrow())
        assertEquals(BridgeRequest.End(4, "t1"), BridgeRequest.parse("""{"t":"end","re":4,"id":"t1"}""").getOrThrow())
        assertEquals(BridgeRequest.Abort(5, "t1"), BridgeRequest.parse("""{"t":"abort","re":5,"id":"t1"}""").getOrThrow())
        assertEquals(
            BridgeRequest.Share(6, listOf("t1", "t2"), "a", "", ""),
            BridgeRequest.parse("""{"t":"share","re":6,"ids":["t1","t2"],"title":"a"}""").getOrThrow(),
        )
        assertEquals(BridgeRequest.Fetch(7, "http://h/z", "z.zip"), BridgeRequest.parse("""{"t":"fetch","re":7,"url":"http://h/z","name":"z.zip"}""").getOrThrow())
        assertEquals(
            BridgeRequest.Watch(8, 41, "CountRoster on nakedpi"),
            BridgeRequest.parse("""{"t":"watch","re":8,"id":41,"title":"CountRoster on nakedpi"}""").getOrThrow(),
        )
        assertEquals(BridgeRequest.Watch(9, 41, ""), BridgeRequest.parse("""{"t":"watch","re":9,"id":41}""").getOrThrow())
    }

    @Test
    fun refusesWhatItCannotRead() {
        for (bad in listOf("", "nope", """{"t":"explode"}""", """{"re":1}""", """{"t":"begin","action":"save"}""",
            """{"t":"begin","size":1,"action":"burn"}""", """{"t":"chunk","id":"t1"}""",
            """{"t":"watch"}""", """{"t":"watch","id":0}""", """{"t":"watch","id":-3}""")) {
            assertTrue("accepted $bad", BridgeRequest.parse(bad).isFailure)
        }
        assertEquals(9, BridgeRequest.reOf("""{"t":"explode","re":9}"""))
        assertEquals(0, BridgeRequest.reOf("garbage"))
    }

    @Test
    fun replies() {
        val ok = JSONObject(BridgeReply.ok(3, "id" to "t1", "ids" to listOf("a")))
        assertEquals(3, ok.getInt("re"))
        assertTrue(ok.getBoolean("ok"))
        assertEquals("t1", ok.getString("id"))
        assertEquals("a", ok.getJSONArray("ids").getString(0))
        val err = JSONObject(BridgeReply.error(4, "no", cancelled = true))
        assertEquals(false, err.getBoolean("ok"))
        assertEquals("no", err.getString("error"))
        assertEquals(true, err.getBoolean("cancelled"))
    }
}
