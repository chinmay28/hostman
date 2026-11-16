package io.github.chinmay28.hostman

import android.app.Activity
import android.app.Dialog
import android.os.Build
import android.os.SystemClock
import android.provider.Settings
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import io.github.chinmay28.hostman.core.DevFlashCurve

/**
 * The developer badge thrown over the screen: the native twin of the web
 * UI's DevMark flash. A veil over everything (the screen behind blurred where
 * Android can), the full badge with `github.com/chinmay28` under it landing
 * with a small overshoot, three seconds, and a tap ends it early. The curve
 * is core/DevFlashCurve, which follows the web's keyframes.
 */
object DevFlash {
    fun show(activity: Activity) {
        val dialog = Dialog(activity, R.style.Theme_HostMan_DevFlash)
        dialog.setContentView(R.layout.dev_flash)
        dialog.window?.apply {
            setLayout(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                addFlags(WindowManager.LayoutParams.FLAG_BLUR_BEHIND)
                attributes = attributes.apply { blurBehindRadius = (26 * activity.resources.displayMetrics.density).toInt() }
            }
        }
        val veil = dialog.findViewById<View>(R.id.veil)
        val lockup = dialog.findViewById<View>(R.id.lockup)
        val still = reducedMotion(activity)

        // Driven frame by frame off the clock rather than by an Animator: the
        // phone's "remove animations" would cut an Animator to nothing, and
        // the web keeps the three seconds (and the fade) even then.
        val start = SystemClock.uptimeMillis()
        val frame = object : Runnable {
            override fun run() {
                if (!dialog.isShowing) return
                val t = (SystemClock.uptimeMillis() - start).toFloat() / DevFlashCurve.DURATION_MS
                veil.alpha = DevFlashCurve.veilAlpha(t)
                val scale = DevFlashCurve.badgeScale(t, still)
                lockup.scaleX = scale
                lockup.scaleY = scale
                if (t >= 1f) dialog.dismiss() else veil.postOnAnimation(this)
            }
        }
        veil.setOnClickListener { dialog.dismiss() }
        dialog.show()
        veil.postOnAnimation(frame)
    }

    /** The phone's "remove animations": the badge then holds still, as on the web. */
    private fun reducedMotion(activity: Activity): Boolean =
        runCatching {
            Settings.Global.getFloat(activity.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f
        }.getOrDefault(false)
}
