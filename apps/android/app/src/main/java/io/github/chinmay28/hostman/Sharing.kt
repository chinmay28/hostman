package io.github.chinmay28.hostman

import android.app.Activity
import android.content.ClipData
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.pm.LabeledIntent
import android.net.Uri
import androidx.core.content.FileProvider
import io.github.chinmay28.hostman.core.FileNames
import java.io.File

/** A file the page staged for the share sheet. */
data class StagedFile(val file: File, val name: String, val mime: String)

/**
 * The share sheet, for navigator.share() on HostMan's page.
 *
 * On a phone a file is usually on its way to another app, so a page shares
 * rather than downloads; the sheet always leads with "Save to Downloads" too,
 * for when it is not.
 */
object Sharing {
    private const val MAX_AGE_MS = 60 * 60 * 1000L

    fun dir(context: Context) = File(context.cacheDir, "shared")

    /**
     * Moves a received file to where the FileProvider serves it from, under
     * its own name — the name the receiving app sees.
     */
    fun stage(context: Context, received: File, name: String, mime: String): StagedFile {
        sweep(context)
        val folder = File(dir(context), System.nanoTime().toString()).apply { mkdirs() }
        val target = File(folder, FileNames.sanitize(name))
        if (!received.renameTo(target)) {
            received.copyTo(target, overwrite = true)
            received.delete()
        }
        return StagedFile(target, target.name, mime)
    }

    fun open(activity: Activity, files: List<StagedFile>, title: String, text: String, url: String) {
        val authority = "${activity.packageName}.files"
        val uris = files.map { FileProvider.getUriForFile(activity, authority, it.file) }
        val body = listOf(text, url).filter { it.isNotBlank() }.joinToString("\n")

        val send = Intent().apply {
            when {
                uris.size > 1 -> {
                    action = Intent.ACTION_SEND_MULTIPLE
                    putParcelableArrayListExtra(Intent.EXTRA_STREAM, ArrayList<Uri>(uris))
                    type = commonType(files.map { it.mime })
                }
                uris.size == 1 -> {
                    action = Intent.ACTION_SEND
                    putExtra(Intent.EXTRA_STREAM, uris[0])
                    type = files[0].mime.ifBlank { "application/octet-stream" }
                }
                else -> {
                    action = Intent.ACTION_SEND
                    type = "text/plain"
                }
            }
            if (body.isNotEmpty()) putExtra(Intent.EXTRA_TEXT, body)
            if (title.isNotBlank()) putExtra(Intent.EXTRA_SUBJECT, title)
            if (uris.isNotEmpty()) {
                clipData = ClipData.newRawUri(files[0].name, uris[0]).apply {
                    for (u in uris.drop(1)) addItem(ClipData.Item(u))
                }
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
        }

        val chooser = Intent.createChooser(send, title.ifBlank { activity.getString(R.string.share_with) })
        if (uris.isNotEmpty()) {
            val save = Intent(send).setComponent(ComponentName(activity, SaveToDownloadsActivity::class.java))
            chooser.putExtra(
                Intent.EXTRA_INITIAL_INTENTS,
                arrayOf(LabeledIntent(save, activity.packageName, activity.getString(R.string.save_to_downloads), R.mipmap.ic_launcher)),
            )
        }
        activity.startActivity(chooser)
    }

    private fun commonType(types: List<String>): String {
        val distinct = types.map { it.ifBlank { "application/octet-stream" } }.distinct()
        if (distinct.size == 1) return distinct[0]
        val majors = distinct.map { it.substringBefore('/') }.distinct()
        return if (majors.size == 1) "${majors[0]}/*" else "*/*"
    }

    /** Files shared more than an hour ago: the receiving app has had them. */
    private fun sweep(context: Context) {
        val cutoff = System.currentTimeMillis() - MAX_AGE_MS
        dir(context).listFiles().orEmpty().filter { it.lastModified() < cutoff }.forEach { it.deleteRecursively() }
    }
}
