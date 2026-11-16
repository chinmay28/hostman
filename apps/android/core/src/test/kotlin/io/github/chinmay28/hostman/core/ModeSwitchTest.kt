package io.github.chinmay28.hostman.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ModeSwitchTest {
    @Test
    fun onPhoneNeedsAServerInTheBuild() {
        assertEquals(ModeChange.Apply, ModeSwitch.check(AppMode.ON_PHONE, AppConfig(), canRunHere = true))
        assertEquals(ModeChange.Unavailable, ModeSwitch.check(AppMode.ON_PHONE, AppConfig(), canRunHere = false))
    }

    @Test
    fun remoteAsksForAnAddressItCannotRead() {
        assertEquals(ModeChange.NeedsAddress, ModeSwitch.check(AppMode.REMOTE, AppConfig(), canRunHere = true))
        assertEquals(ModeChange.NeedsAddress, ModeSwitch.check(AppMode.REMOTE, AppConfig(serverAddress = "ftp://x"), canRunHere = true))
        assertEquals(ModeChange.Apply, ModeSwitch.check(AppMode.REMOTE, AppConfig(serverAddress = "nakedpi.local"), canRunHere = false))
    }

    @Test
    fun sharingNeedsAPin() {
        assertFalse(ModeSwitch.canShare(AppConfig()))
        assertFalse(ModeSwitch.canShare(AppConfig(pin = "12")))
        assertFalse(ModeSwitch.canShare(AppConfig(pin = "    ")))
        assertTrue(ModeSwitch.canShare(AppConfig(pin = "1234")))
    }
}
