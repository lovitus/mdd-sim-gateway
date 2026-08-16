package com.mdd.cardagent

import com.mdd.cardagent.service.VpcdClient
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class VpcdClientTest {

    @Test
    fun testFormatFingerprint() {
        val dummyDer = "dummy-cert-bytes-123".toByteArray(Charsets.UTF_8)
        val fp = VpcdClient.formatFingerprint(dummyDer)
        assertEquals(95, fp.length) // 32 hex pairs + 31 colons
        assertTrue(fp.contains(":"))
    }
}
