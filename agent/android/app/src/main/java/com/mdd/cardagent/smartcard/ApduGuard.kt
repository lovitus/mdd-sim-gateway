package com.mdd.cardagent.smartcard

import android.util.Log

object ApduGuard {
    private const val TAG = "ApduGuard"

    /**
     * Checks if the incoming APDU contains dangerous instructions that could
     * erase the eSIM profile or delete critical files on the physical SIM.
     */
    fun isForbidden(apdu: ByteArray): Boolean {
        if (apdu.size < 2) return false

        // ISO 7816-4 DELETE FILE (INS = 0xE4)
        if (apdu[1] == 0xE4.toByte()) {
            safeLog("[GUARD] Blocked ISO 7816 DELETE FILE APDU (INS=0xE4)")
            return true
        }

        // SGP.22 ES10c.DeleteProfile tag: 0xBF33
        if (containsSequence(apdu, byteArrayOf(0xBF.toByte(), 0x33.toByte()))) {
            safeLog("[GUARD] Blocked ES10c.DeleteProfile APDU (tag 0xBF33)")
            return true
        }

        return false
    }

    private fun safeLog(msg: String) {
        try {
            Log.w(TAG, msg)
        } catch (_: Throwable) {
            println("$TAG: $msg")
        }
    }

    private fun containsSequence(source: ByteArray, target: ByteArray): Boolean {
        if (target.isEmpty() || source.size < target.size) return false
        val maxIndex = source.size - target.size
        for (i in 0..maxIndex) {
            var match = true
            for (j in target.indices) {
                if (source[i + j] != target[j]) {
                    match = false
                    break
                }
            }
            if (match) return true
        }
        return false
    }
}
