package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Test

class NavigationPolicyTest {
    private val app = Origin.loopback(Origin.DEFAULT_PORT)

    private fun check(url: String, want: Navigation) = assertEquals(url, want, NavigationPolicy.decide(app, url))

    @Test
    fun hostMansOwnPagesStayInTheApp() {
        check("http://127.0.0.1:8899/", Navigation.IN_APP)
        check("http://127.0.0.1:8899/hosts/3/shell", Navigation.IN_APP)
        check("http://127.0.0.1:8899/deployments/41", Navigation.IN_APP)
        check("blob:http://127.0.0.1:8899/0b1c", Navigation.IN_APP)
        check("data:image/png;base64,AAAA", Navigation.IN_APP)
        check("about:blank", Navigation.IN_APP)
    }

    @Test
    fun otherSitesGoToThePhone() {
        // An app's forwarded port on the same machine is another site.
        check("http://127.0.0.1:8443/", Navigation.EXTERNAL)
        check("https://127.0.0.1:8899/", Navigation.EXTERNAL)
        check("http://nakedpi.local:3000/", Navigation.EXTERNAL)
        check("https://claude.ai/oauth/authorize?code=true", Navigation.EXTERNAL)
        check("https://ollama.com/library", Navigation.EXTERNAL)
        check("mailto:someone@example.com", Navigation.EXTERNAL)
        check("tel:+15555550100", Navigation.EXTERNAL)
        check("ssh://pi@nakedpi.local", Navigation.EXTERNAL)
    }

    @Test
    fun intentUrlsAreTheirOwnCase() {
        check("intent://nakedpi.local:8096/#Intent;scheme=http;package=org.jellyfin.mobile;end", Navigation.INTENT)
    }

    @Test
    fun dangerousSchemesAreBlocked() {
        check("javascript:alert(1)", Navigation.BLOCK)
        check("file:///data/data/io.github.chinmay28.hostman/files/hostman/hostman.db", Navigation.BLOCK)
        check("content://com.android.contacts/contacts", Navigation.BLOCK)
        check("about:config", Navigation.BLOCK)
        check("ws://127.0.0.1:8899/api/hosts/1/shell", Navigation.BLOCK)
        check("weird", Navigation.BLOCK)
        check("", Navigation.BLOCK)
    }
}
