package com.mdd.cardagent.smartcard

import android.content.Context
import android.se.omapi.Channel
import android.se.omapi.Reader
import android.se.omapi.SEService
import android.se.omapi.Session
import android.util.Log
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.util.concurrent.Executors

class OmapiCardChannel(
    private val context: Context,
    private val preferredReader: String = "SIM1"
) : ISmartCardChannel {

    private val tag = "OmapiCardChannel"
    private val executor = Executors.newSingleThreadExecutor()

    private var seService: SEService? = null
    private var session: Session? = null
    private var channel: Channel? = null
    private var currentAtr: ByteArray = byteArrayOf(
        0x3B.toByte(), 0x9F.toByte(), 0x95.toByte(), 0x80.toByte(),
        0x1F.toByte(), 0xC7.toByte(), 0x80.toByte(), 0x31.toByte(),
        0xE0.toByte(), 0x73.toByte(), 0xFE.toByte(), 0x21.toByte(),
        0x1B.toByte(), 0x64.toByte(), 0x01.toByte()
    )

    override val channelName: String
        get() = "OMAPI ($preferredReader)"

    override val isConnected: Boolean
        get() = channel?.isOpen == true

    override suspend fun connect(): Result<ByteArray> = withContext(Dispatchers.IO) {
        try {
            val deferred = CompletableDeferred<Boolean>()
            seService = SEService(context, executor) {
                deferred.complete(true)
            }
            deferred.await()

            val service = seService ?: return@withContext Result.failure(IllegalStateException("SEService not initialized"))
            val readers = service.readers
            if (readers.isEmpty()) {
                return@withContext Result.failure(IllegalStateException("No OMAPI Secure Element readers found"))
            }

            var targetReader: Reader = readers[0]
            for (r in readers) {
                if (r.name.contains(preferredReader, ignoreCase = true)) {
                    targetReader = r
                    break
                }
            }

            if (!targetReader.isSecureElementPresent) {
                return@withContext Result.failure(IllegalStateException("No SIM/SE card present in reader ${targetReader.name}"))
            }

            try {
                val getAtrMethod = targetReader.javaClass.getMethod("getAtr")
                val atrBytes = getAtrMethod.invoke(targetReader) as? ByteArray
                if (atrBytes != null && atrBytes.isNotEmpty()) {
                    currentAtr = atrBytes
                }
            } catch (_: Exception) {
                // Fallback to default ATR if getAtr is not available on this API level
            }

            val currentSession = targetReader.openSession()
            session = currentSession

            // Open Basic Channel by default (or logical channel if basic is unavailable)
            channel = try {
                currentSession.openBasicChannel(null)
            } catch (e: Exception) {
                Log.w(tag, "openBasicChannel failed (${e.message}), trying openLogicalChannel...")
                currentSession.openLogicalChannel(null)
            }

            Log.i(tag, "Connected to OMAPI reader: ${targetReader.name}")
            Result.success(currentAtr)
        } catch (e: Exception) {
            Log.e(tag, "OMAPI connect failed: ${e.message}", e)
            disconnect()
            Result.failure(e)
        }
    }

    override suspend fun transmit(apdu: ByteArray): ByteArray = withContext(Dispatchers.IO) {
        try {
            val ch = channel ?: return@withContext byteArrayOf(0x6F.toByte(), 0x00.toByte())
            if (!ch.isOpen) return@withContext byteArrayOf(0x6F.toByte(), 0x00.toByte())

            val response = ch.transmit(apdu)
            response ?: byteArrayOf(0x6F.toByte(), 0x00.toByte())
        } catch (e: Exception) {
            Log.e(tag, "OMAPI transmit failed: ${e.message}", e)
            byteArrayOf(0x6F.toByte(), 0x00.toByte())
        }
    }

    override suspend fun reset(): Boolean = withContext(Dispatchers.IO) {
        disconnect()
        connect().isSuccess
    }

    override fun disconnect() {
        try {
            channel?.close()
        } catch (_: Exception) {}
        channel = null

        try {
            session?.close()
        } catch (_: Exception) {}
        session = null

        try {
            seService?.shutdown()
        } catch (_: Exception) {}
        seService = null
    }
}
