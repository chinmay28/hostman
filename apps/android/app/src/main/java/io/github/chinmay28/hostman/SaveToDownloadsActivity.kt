package io.github.chinmay28.hostman

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.provider.OpenableColumns
import androidx.core.content.IntentCompat
import kotlin.concurrent.thread

/**
 * "Save to Downloads" in the share sheet. Copies only what the app's own
 * FileProvider serves — the files HostMan's page just shared — so another
 * app cannot use it to put things in Downloads.
 */
class SaveToDownloadsActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val own = "$packageName.files"
        val uris = when (intent.action) {
            Intent.ACTION_SEND -> listOfNotNull(IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri::class.java))
            Intent.ACTION_SEND_MULTIPLE -> IntentCompat.getParcelableArrayListExtra(intent, Intent.EXTRA_STREAM, Uri::class.java).orEmpty()
            else -> emptyList()
        }.filter { it.scheme == "content" && it.authority == own }

        val app = applicationContext
        thread(name = "save-to-downloads") {
            for (uri in uris) {
                val name = displayName(uri) ?: "download"
                try {
                    val saved = app.contentResolver.openInputStream(uri)!!.use { Downloads.save(app, it, name, app.contentResolver.getType(uri)) }
                    Downloads.toast(app, app.getString(R.string.saved_to_downloads, saved))
                } catch (e: Exception) {
                    Downloads.toast(app, app.getString(R.string.save_failed, name, e.message ?: e.javaClass.simpleName))
                }
            }
        }
        finish()
    }

    private fun displayName(uri: Uri): String? = runCatching {
        contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
            if (c.moveToFirst()) c.getString(0) else null
        }
    }.getOrNull()
}
