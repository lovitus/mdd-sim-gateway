package com.mdd.cardagent.service

import android.util.Log
import com.mdd.cardagent.smartcard.ApduGuard
import com.mdd.cardagent.smartcard.ISmartCardChannel
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.isActive
import kotlinx.coroutines.withContext
import java.io.DataInputStream
import java.io.DataOutputStream
import java.net.InetSocketAddress
import java.net.Socket
import kotlin.coroutines.coroutineContext

class VpcdClient(
    private val host: String,
    private val port: Int,
    private val channel: ISmartCardChannel,
    private val onLog: (String) -> Unit
) {

    private val tag = "VpcdClient"

    companion object {
        const val VPCD_CTRL_OFF = 0x01.toByte()
        const val VPCD_CTRL_ON = 0x02.toByte()
        const val VPCD_CTRL_RESET = 0x03.toByte()
        const val VPCD_CTRL_ATR = 0x04.toByte()
    }

    suspend fun run() = withContext(Dispatchers.IO) {
        onLog("Connecting to SmartCard Channel [${channel.channelName}]...")
        val connectRes = channel.connect()
        if (connectRes.isFailure) {
            val err = connectRes.exceptionOrNull()?.message ?: "Unknown error"
            onLog("❌ SmartCard connection failed: $err")
            return@withContext
        }

        val atr = connectRes.getOrNull() ?: byteArrayOf()
        val atrHex = atr.joinToString("") { "%02X".format(it) }
        onLog("✅ SmartCard connected. ATR: $atrHex")

        val socket = Socket()
        try {
            onLog("Connecting to Gateway VPCD bridge at $host:$port...")
            socket.connect(InetSocketAddress(host, port), 10000)
            socket.tcpNoDelay = true
            onLog("🔗 VPCD bridge connected to $host:$port. Ready for APDU forwarding.")

            val input = DataInputStream(socket.getInputStream())
            val output = DataOutputStream(socket.getOutputStream())

            while (coroutineContext.isActive && socket.isConnected && !socket.isClosed) {
                val length = try {
                    input.readUnsignedShort()
                } catch (e: Exception) {
                    onLog("Connection closed by server: ${e.message}")
                    break
                }

                if (length == 0) continue

                val payload = ByteArray(length)
                input.readFully(payload)

                if (length == 1) {
                    val ctrl = payload[0]
                    when (ctrl) {
                        VPCD_CTRL_ATR -> {
                            onLog(">> [VPCD] Server requested ATR -> Sending $atrHex")
                            output.writeShort(atr.size)
                            output.write(atr)
                            output.flush()
                        }
                        VPCD_CTRL_RESET, VPCD_CTRL_ON, VPCD_CTRL_OFF -> {
                            onLog(">> [VPCD] Server requested Card Reset (ctrl=$ctrl)")
                            channel.reset()
                        }
                    }
                    continue
                }

                // APDU command forwarding
                val apduHex = payload.joinToString("") { "%02X".format(it) }
                val startMs = System.currentTimeMillis()

                val resp: ByteArray = if (ApduGuard.isForbidden(payload)) {
                    onLog("🛡️ [BLOCKED] Prevented dangerous APDU: $apduHex")
                    byteArrayOf(0x69.toByte(), 0x85.toByte())
                } else {
                    val rapdu = channel.transmit(payload)
                    rapdu
                }

                val elapsed = System.currentTimeMillis() - startMs
                val respHex = resp.joinToString("") { "%02X".format(it) }
                onLog("TX: $apduHex  -->  RX: $respHex (${elapsed}ms)")

                output.writeShort(resp.size)
                output.write(resp)
                output.flush()
            }
        } catch (e: Exception) {
            onLog("❌ Session terminated: ${e.message}")
            Log.e(tag, "VPCD error: ${e.message}", e)
        } finally {
            try { socket.close() } catch (_: Exception) {}
            channel.disconnect()
            onLog("Session ended. Disconnected from SmartCard.")
        }
    }
}
