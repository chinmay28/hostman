package io.github.chinmay28.hostman.core

/**
 * The developer badge's flash, as keyframes: the same three seconds as the
 * web UI's `dev-flash-veil` and SAND Harbor's `sand-dev-*` animations, so the
 * badge lands the same way in the app as on the page.
 *
 * The veil fades in over the first 7%, holds, and fades out after 82%. The
 * badge lands from 0.82 with a small overshoot (1.02 at 14%, settled by 22%)
 * and drifts out to 1.06 as the veil clears.
 */
object DevFlashCurve {
    const val DURATION_MS = 3_000L

    private val veil = listOf(0f to 0f, 0.07f to 1f, 0.82f to 1f, 1f to 0f)
    private val badge = listOf(0f to 0.82f, 0.14f to 1.02f, 0.22f to 1f, 0.82f to 1f, 1f to 1.06f)

    /** The veil's opacity at [t], 0–1 through the animation. */
    fun veilAlpha(t: Float): Float = at(veil, t)

    /** The badge's scale at [t]; with [reducedMotion] it holds still, as the web's does. */
    fun badgeScale(t: Float, reducedMotion: Boolean = false): Float = if (reducedMotion) 1f else at(badge, t)

    private fun at(frames: List<Pair<Float, Float>>, t: Float): Float {
        val x = t.coerceIn(0f, 1f)
        for (i in 1 until frames.size) {
            val (x1, y1) = frames[i]
            if (x <= x1) {
                val (x0, y0) = frames[i - 1]
                return if (x1 == x0) y1 else y0 + (y1 - y0) * (x - x0) / (x1 - x0)
            }
        }
        return frames.last().second
    }
}
