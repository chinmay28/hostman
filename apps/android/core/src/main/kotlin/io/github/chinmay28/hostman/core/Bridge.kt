package io.github.chinmay28.hostman.core

import org.json.JSONArray
import org.json.JSONObject

/**
 * The messages HostMan's page and the app send each other.
 *
 * The page cannot hand the phone a file by itself: a web view has no
 * Downloads, no share sheet, and cannot read a blob: address from outside the
 * page. So a script the app puts into the page (assets/hostman-bridge.js)
 * turns "save this blob" and navigator.share() into these messages, carrying
 * the bytes across in base64 chunks, each answered before the next is sent so
 * a large file never piles up in memory on either side. The same channel
 * carries the page's two requests of the app itself: open its settings, and
 * follow a deployment in a notification.
 *
 * Every request carries `re`, a number the page picked; the answer carries
 * the same one. Messages arrive only from HostMan's own origin — the web view
 * enforces that, not this code.
 */
sealed class BridgeRequest {
    abstract val re: Int

    /** The page asking what it is running in. */
    data class Hello(override val re: Int) : BridgeRequest()

    /** Open the app's own settings. */
    data class OpenSettings(override val re: Int) : BridgeRequest()

    /** A file is coming: [size] bytes, to be saved to Downloads or staged for sharing. */
    data class Begin(
        override val re: Int,
        val name: String,
        val mime: String,
        val size: Long,
        val action: TransferAction,
    ) : BridgeRequest()

    data class Chunk(override val re: Int, val id: String, val seq: Int, val data: String) : BridgeRequest()

    data class End(override val re: Int, val id: String) : BridgeRequest()

    data class Abort(override val re: Int, val id: String) : BridgeRequest()

    /** Open the share sheet with staged files and/or text. */
    data class Share(
        override val re: Int,
        val ids: List<String>,
        val title: String,
        val text: String,
        val url: String,
    ) : BridgeRequest()

    /** Download an address on HostMan's origin straight to Downloads. */
    data class Fetch(override val re: Int, val url: String, val name: String) : BridgeRequest()

    /**
     * Follow deployment [id] in a notification until it ends, so leaving the
     * app does not mean losing sight of it. [title] is what the page calls it
     * ("CountRoster on nakedpi"), shown until the app's own first look.
     */
    data class Watch(override val re: Int, val id: Long, val title: String) : BridgeRequest()

    companion object {
        fun parse(message: String): Result<BridgeRequest> = runCatching {
            val m = JSONObject(message)
            val re = m.optInt("re", 0)
            when (val t = m.optString("t")) {
                "hello" -> Hello(re)
                "settings" -> OpenSettings(re)
                "begin" -> Begin(
                    re,
                    name = m.optString("name"),
                    mime = m.optString("mime").ifBlank { "application/octet-stream" },
                    size = m.optLong("size", -1).also { require(it >= 0) { "begin without a size" } },
                    action = TransferAction.of(m.optString("action")),
                )
                "chunk" -> Chunk(re, m.getString("id"), m.getInt("seq"), m.getString("data"))
                "end" -> End(re, m.getString("id"))
                "abort" -> Abort(re, m.getString("id"))
                "share" -> Share(
                    re,
                    ids = m.optJSONArray("ids")?.let { a -> List(a.length()) { a.getString(it) } } ?: emptyList(),
                    title = m.optString("title"),
                    text = m.optString("text"),
                    url = m.optString("url"),
                )
                "fetch" -> Fetch(re, m.getString("url"), m.optString("name"))
                "watch" -> Watch(
                    re,
                    id = m.optLong("id", 0).also { require(it > 0) { "watch without a deployment id" } },
                    title = m.optString("title"),
                )
                else -> throw IllegalArgumentException("unknown message ${if (t.isEmpty()) "(no type)" else t}")
            }
        }

        /** The `re` of a message that may not parse, so even a refusal can be answered. */
        fun reOf(message: String): Int = runCatching { JSONObject(message).optInt("re", 0) }.getOrDefault(0)
    }
}

enum class TransferAction {
    /** Straight into Downloads: the page saved a file. */
    SAVE,

    /** Held for a [BridgeRequest.Share] that follows: the page shared a file. */
    STAGE;

    companion object {
        fun of(s: String): TransferAction = when (s) {
            "save" -> SAVE
            "stage" -> STAGE
            else -> throw IllegalArgumentException("unknown action \"$s\"")
        }
    }
}

/** An answer to a [BridgeRequest]. */
object BridgeReply {
    fun ok(re: Int, vararg extra: Pair<String, Any?>): String {
        val o = JSONObject().put("re", re).put("ok", true)
        for ((k, v) in extra) o.put(k, if (v is List<*>) JSONArray(v) else v)
        return o.toString()
    }

    /** [cancelled] says the person backed out, which the page does not report as a failure. */
    fun error(re: Int, message: String, cancelled: Boolean = false): String =
        JSONObject().put("re", re).put("ok", false).put("error", message).put("cancelled", cancelled).toString()
}
