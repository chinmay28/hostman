package io.github.chinmay28.hostman

import android.content.Intent
import android.os.Bundle
import android.view.View
import android.widget.LinearLayout
import androidx.activity.enableEdgeToEdge
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.widget.doAfterTextChanged
import com.google.android.material.button.MaterialButton
import com.google.android.material.card.MaterialCardView
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import io.github.chinmay28.hostman.core.AppMode
import io.github.chinmay28.hostman.core.DiscoveryProtocol
import io.github.chinmay28.hostman.core.Origin
import kotlin.concurrent.thread

/**
 * The first screen: does HostMan run on another machine, or on this phone?
 *
 * Only the choice is made here. Everything after it — adding hosts, apps,
 * the PIN screen of a server that has one — is HostMan's own page, which
 * comes next. The developer badge sits at the bottom, as it does in the web
 * UI's header.
 */
class SetupActivity : AppCompatActivity() {
    private var mode: AppMode? = null

    private lateinit var remote: MaterialCardView
    private lateinit var phone: MaterialCardView
    private lateinit var remoteFields: View
    private lateinit var addressLayout: TextInputLayout
    private lateinit var address: TextInputEditText
    private lateinit var found: LinearLayout
    private lateinit var find: MaterialButton
    private lateinit var go: MaterialButton

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_setup)
        ViewCompat.setOnApplyWindowInsetsListener(findViewById(R.id.root)) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.ime())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            WindowInsetsCompat.CONSUMED
        }

        remote = findViewById(R.id.card_remote)
        phone = findViewById(R.id.card_phone)
        remoteFields = findViewById(R.id.remote_fields)
        addressLayout = findViewById(R.id.address_layout)
        address = findViewById(R.id.address)
        found = findViewById(R.id.found)
        find = findViewById(R.id.find)
        go = findViewById(R.id.go)
        findViewById<android.widget.TextView>(R.id.version).text =
            getString(R.string.setup_version, BuildConfig.VERSION_NAME)
        findViewById<View>(R.id.dev_badge).setOnClickListener { DevFlash.show(this) }

        val store = ConfigStore(this)
        val existing = store.load()
        address.setText(existing.serverAddress)

        val canRunHere = LocalServer.available(this)
        if (!canRunHere) {
            phone.isEnabled = false
            phone.alpha = 0.5f
            findViewById<android.widget.TextView>(R.id.phone_body).setText(R.string.setup_phone_unavailable)
        }

        remote.setOnClickListener { choose(AppMode.REMOTE) }
        phone.setOnClickListener { if (canRunHere) choose(AppMode.ON_PHONE) }
        address.doAfterTextChanged { addressLayout.error = null; refresh() }
        find.setOnClickListener { discover() }
        go.setOnClickListener {
            val m = mode ?: return@setOnClickListener
            var c = store.load().copy(mode = m)
            if (m == AppMode.REMOTE) {
                val o = Origin.parse(address.text.toString()).getOrElse {
                    addressLayout.error = it.message
                    return@setOnClickListener
                }
                c = c.copy(serverAddress = o.toString())
            }
            store.save(c)
            startActivity(Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP))
            finish()
        }
        choose(existing.mode ?: if (canRunHere) null else AppMode.REMOTE)
    }

    private fun choose(m: AppMode?) {
        mode = m
        remote.isChecked = m == AppMode.REMOTE
        phone.isChecked = m == AppMode.ON_PHONE
        remoteFields.visibility = if (m == AppMode.REMOTE) View.VISIBLE else View.GONE
        refresh()
    }

    private fun refresh() {
        go.isEnabled = when (mode) {
            AppMode.REMOTE -> !address.text.isNullOrBlank()
            AppMode.ON_PHONE -> true
            null -> false
        }
    }

    private fun discover() {
        find.isEnabled = false
        find.setText(R.string.setup_finding)
        found.removeAllViews()
        thread(name = "hostman-discovery") {
            val servers = LanDiscovery.find(this)
            runOnUiThread {
                if (isDestroyed) return@runOnUiThread
                find.isEnabled = true
                find.setText(R.string.setup_find)
                showFound(servers)
            }
        }
    }

    private fun showFound(servers: List<DiscoveryProtocol.Found>) {
        found.removeAllViews()
        if (servers.isEmpty()) {
            found.addView(android.widget.TextView(this).apply {
                setText(R.string.setup_found_none)
                setTextColor(getColor(R.color.hm_muted))
            })
            return
        }
        for (s in servers) {
            found.addView(MaterialButton(this, null, com.google.android.material.R.attr.materialButtonOutlinedStyle).apply {
                text = getString(R.string.setup_found_item, s.name, s.origin.toString())
                isAllCaps = false
                setOnClickListener { address.setText(s.origin.toString()) }
            })
        }
    }
}
