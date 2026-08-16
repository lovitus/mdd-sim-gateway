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
    private val defaultAtr = byteArrayOf(0x3B, 0x9F, 0x95, 0x80, 0x1F, 0xC7, 0x80, 0x31, 0xE0, 0x73, 0xFE, 0x21, 0x1B, 0x64, 0x01)

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
            val tm = telephonyManager ?: return@withContext byteArrayOf(0x6F, 0x00)
            if (channelId <= 0 || apdu.size < 4) return@withContext byteArrayOf(0x6F, 0x00)

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

            val hexResp = tm.iccTransmitApduLogicalChannel(channelId, cla, ins, p1, p2, p3, data)
            if (hexResp.isNullOrEmpty()) {
                return@withContext byteArrayOf(0x90.toByte(), 0x00.toByte())
            }

            return@withContext hexStringToByteArray(hexResp)
        } catch (e: Exception) {
            Log.e(tag, "Telephony transmit error: ${e.message}", e)
            return@withContext byteArrayOf(0x6F, 0x00)
        }
    }

    override suspend fun reset(): Boolean = withContext(Dispatchers.IO) {
        disconnect()
        connect().isSuccess
    }

    override fun disconnect() {
        try {
            if (channelId > 0) {
                telephonyManager?.iccCloseLogicalChannel(channelId)
            }
        } catch (e: Exception) {
            Log.w(tag, "Telephony disconnect error: ${e.message}")
        } finally {
            channelId = -1
        }
    }

    private fun hexStringToByteArray(hex: String): ByteArray {
        val s = hex.replace(" ", "").trim()
        val len = s.length
        val data = ByteArray(len / 2)
        for (i in 0 until len step 2) {
            data[i / 2] = ((Character.digit(s[i], 16) shl 4) + Character.digit(s[i + 1], 16)).toByte()
        }
        return data
    }
}
