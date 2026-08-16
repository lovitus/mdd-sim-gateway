package com.mdd.cardagent

import com.mdd.cardagent.smartcard.ApduGuard
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ApduGuardTest {

    @Test
    fun testSelectAidAllowed() {
        val selectIsim = byteArrayOf(
            0x00.toByte(), 0xA4.toByte(), 0x04.toByte(), 0x00.toByte(), 0x0A.toByte(),
            0xA0.toByte(), 0x00.toByte(), 0x00.toByte(), 0x00.toByte(), 0x87.toByte(),
            0x10.toByte(), 0x04.toByte(), 0xFF.toByte(), 0xFF.toByte(), 0xFF.toByte(), 0xFF.toByte()
        )
        assertFalse(ApduGuard.isForbidden(selectIsim))
    }

    @Test
    fun testAuthenticateAkaAllowed() {
        val authAka = byteArrayOf(
            0x00.toByte(), 0x88.toByte(), 0x00.toByte(), 0x81.toByte(), 0x22.toByte(),
            0x10.toByte(), 0x01.toByte(), 0x02.toByte(), 0x03.toByte()
        )
        assertFalse(ApduGuard.isForbidden(authAka))
    }

    @Test
    fun testDeleteProfileBlocked() {
        // Tag 0xBF33 in SGP.22 ES10c.DeleteProfile
        val deleteProfile = byteArrayOf(
            0x80.toByte(), 0xE2.toByte(), 0x91.toByte(), 0x00.toByte(), 0x05.toByte(),
            0xBF.toByte(), 0x33.toByte(), 0x02.toByte(), 0x5A.toByte(), 0x00.toByte()
        )
        assertTrue(ApduGuard.isForbidden(deleteProfile))
    }

    @Test
    fun testDeleteFileBlocked() {
        // ISO 7816-4 DELETE FILE (INS = 0xE4)
        val deleteFile = byteArrayOf(0x00.toByte(), 0xE4.toByte(), 0x00.toByte(), 0x00.toByte(), 0x02.toByte(), 0x3F.toByte(), 0x00.toByte())
        assertTrue(ApduGuard.isForbidden(deleteFile))
    }

    @Test
    fun testShortApdu() {
        val shortApdu = byteArrayOf(0x00.toByte())
        assertFalse(ApduGuard.isForbidden(shortApdu))
    }
}
