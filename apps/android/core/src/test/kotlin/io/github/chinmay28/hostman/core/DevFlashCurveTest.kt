package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Test

class DevFlashCurveTest {
    private fun near(want: Float, got: Float) = assertEquals(want, got, 0.001f)

    @Test
    fun theVeilFadesInHoldsAndClears() {
        near(0f, DevFlashCurve.veilAlpha(0f))
        near(0.5f, DevFlashCurve.veilAlpha(0.035f))
        near(1f, DevFlashCurve.veilAlpha(0.5f))
        near(0f, DevFlashCurve.veilAlpha(1f))
        near(0f, DevFlashCurve.veilAlpha(2f))
    }

    @Test
    fun theBadgeOvershootsThenSettles() {
        near(0.82f, DevFlashCurve.badgeScale(0f))
        near(1.02f, DevFlashCurve.badgeScale(0.14f))
        near(1f, DevFlashCurve.badgeScale(0.5f))
        near(1.06f, DevFlashCurve.badgeScale(1f))
        near(1f, DevFlashCurve.badgeScale(0f, reducedMotion = true))
    }

    /** The web UI's keyframes are the ones being copied. */
    @Test
    fun matchesTheWebUi() {
        val css = java.io.File("../../web/src/styles.css").readText()
        val veil = css.substringAfter("@keyframes dev-flash-veil").substringBefore("@keyframes")
        for (stop in listOf("7%", "82%")) org.junit.Assert.assertTrue("web veil lacks $stop", veil.contains(stop))
        val badge = css.substringAfter("@keyframes dev-flash-badge").substringBefore("@media")
        for (stop in listOf("scale(0.82)", "14%", "scale(1.02)", "22%", "scale(1.06)")) {
            org.junit.Assert.assertTrue("web keyframes lack $stop", badge.contains(stop))
        }
        org.junit.Assert.assertTrue(css.contains("animation: dev-flash-veil 3s"))
    }
}
