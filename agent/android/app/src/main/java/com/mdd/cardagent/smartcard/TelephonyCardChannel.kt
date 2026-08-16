package com.mdd.cardagent.smartcard

import android.content.Context
import android.telephony.IccOpenLogicalChannelResponse
import android.telephony.TelephonyManager
import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

class TelephonyCardChannel(
    private val context: Context,
    private val subId: Int = -1
) : ISmartCardChannel {

    private val tag = "TelephonyCardChannel"
    private var channelId: Int = -1
    private var telephonyManager: TelephonyManager? = null
    private val defaultAtr = byteArrayOf(
        0x3B.toByte(), 0x9F.toByte(), 0x95.toByte(), 0x80.toByte(),
        0x1F.toByte(), 0xC7.toByte(), 0x80.toByte(), 0x31.toByte(),
        0xE0.toByte(), 0x73.toByte(), 0xFE.toByte(), 0x21.toByte(),
        0x1B.toByte(), 0x64.toByte(), 0x01.toByte()
    )

    override val channelName: String
        get() = "Telephony UICC"

    override val isConnected: Boolean
        get() = channelId > 0

    override suspend fun connect(): Result<ByteArray> = withContext(Dispatchers.IO) {
        try {
            val tm = context.getSystemService(Context.TELEPHONY_SERVICE) as TelephonyManager
            telephonyManager = tm

            // Open logical channel to ISIM / USIM
            val response: IccOpenLogicalChannelResponse = tm.iccOpenLogicalChannel("")
            val ch = response.channel
            if (ch <= 0) {
                return@withContext Result.failure(IllegalStateException("Failed to open Telephony UICC logical channel (status=${response.status})"))
            }

            channelId = ch
            Log.i(tag, "Opened Telephony UICC logical channel: $ch")
            Result.success(defaultAtr)
        } catch (e: Exception) {
            Log.e(tag, "Telephony connect failed: ${e.message}", e)
            Result.failure(e)
        }
    }

    override suspend fun transmit(apdu: ByteArray): ByteArray = withContext(Dispatchers.IO) {
        try {
            val tm = telephonyManager ?: return@withContext byteArrayOf(0x6F.toByte(), 0x00.toByte())
            if (channelId <= 0 || apdu.size < 4) return@withContext byteArrayOf(0x6F.toByte(), 0x00.toByte())

            val cla = apdu[0].toInt() and 0xFF
            val ins = apdu[1].toInt() and 0xFF
            val p1 = apdu[2].toInt() and 0xFF
            val p2 = apdu[3].toInt() and 0xFF
            val p3 = if (apdu.size > 4) apdu[4].toInt() and 0xFF else 0
            val data = if (apdu.size > 5) {
                val len = minOf(p3, apdu.size - 5)
                val d = ByteArray(len)
                System.arraycopy(apdu, 5, d, 0, len)
                d.joinToString("") { "%02X".format(it) }
            } else {
                ""
            }

            val hexResp = tm.iccTransmitApduLogicalChannel(
                channelId, cla, ins, p1, p2, p3, data
            )

            if (hexResp.isNullOrEmpty()) {
                return@withContext byteArrayOf(0x6F.toByte(), 0x00.toByte())
            }

            hexStringToByteArray(hexResp)
        } catch (e: Exception) {
            Log.e(tag, "Telephony transmit failed: ${e.message}", e)
            byteArrayOf(0x6F.toByte(), 0x00.toByte())
        }
    }

    override suspend fun reset(): Boolean = withContext(Dispatchers.IO) {
        disconnect()
        connect().isSuccess
    }

    override fun disconnect() {
        if (channelId > 0) {
            try {
                telephonyManager?.iccCloseLogicalChannel(channelId)
            } catch (e: Exception) {
                Log.w(tag, "Error closing Telephony logical channel: ${e.message}")
            }
            channelId = -1
        }
        telephonyManager = null
    }

    private fun hexStringToByteArray(s: String): ByteArray {
        val len = s.length
        val data = ByteArray(len / 2)
        var i = 0
        while (i < len) {
            data[i / 2] = ((Character.digit(s[i], 16) shl 4) + Character.digit(s[i + 1], 16)).toByte()
            i += 2
        }
        return data
    }
}
