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
    private var currentAtr: ByteArray = byteArrayOf(0x3B, 0x9F, 0x95, 0x80, 0x1F, 0xC7, 0x80, 0x31, 0xE0, 0x73, 0xFE, 0x21, 0x1B, 0x64, 0x01)

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

            targetReader.atr?.let {
                if (it.isNotEmpty()) currentAtr = it
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
            Result.failure(e)
        }
    }

    override suspend fun transmit(apdu: ByteArray): ByteArray = withContext(Dispatchers.IO) {
        try {
            val ch = channel ?: throw IllegalStateException("OMAPI Channel is not open")
            val resp = ch.transmit(apdu)
            return@withContext resp ?: byteArrayOf(0x6F, 0x00)
        } catch (e: Exception) {
            Log.e(tag, "OMAPI transmit error: ${e.message}", e)
            return@withContext byteArrayOf(0x6F, 0x00)
        }
    }

    override suspend fun reset(): Boolean = withContext(Dispatchers.IO) {
        disconnect()
        connect().isSuccess
    }

    override fun disconnect() {
        try {
            channel?.close()
            session?.close()
            seService?.shutdown()
        } catch (e: Exception) {
            Log.w(tag, "OMAPI disconnect warning: ${e.message}")
        } finally {
            channel = null
            session = null
            seService = null
        }
    }
}
