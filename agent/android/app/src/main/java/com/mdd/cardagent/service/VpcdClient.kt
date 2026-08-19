package com.mdd.cardagent.service

import android.content.Context
import android.util.Base64
import com.mdd.cardagent.smartcard.ApduGuard
import com.mdd.cardagent.smartcard.ISmartCardChannel
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.DataOutputStream
import java.io.InputStream
import java.io.OutputStream
import java.net.InetSocketAddress
import java.net.Socket
import java.security.MessageDigest
import java.security.SecureRandom
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLSocket
import javax.net.ssl.TrustManager
import javax.net.ssl.X509TrustManager
import kotlin.coroutines.coroutineContext
import java.util.UUID

class VpcdClient(
    private val host: String,
    private val port: Int,
    private val token: String = "",
    private val useWss: Boolean = true,
    private val wsPath: String = "/mdd/api/vpcd/ws",
    private val resetPin: Boolean = false,
    private val context: Context? = null,
    private val channel: ISmartCardChannel,
    private val onLog: (String) -> Unit
) {

    private val writeLock = Any()

    companion object {
        const val VPCD_CTRL_OFF = 0x01.toByte()
        const val VPCD_CTRL_ON = 0x02.toByte()
        const val VPCD_CTRL_RESET = 0x03.toByte()
        const val VPCD_CTRL_ATR = 0x04.toByte()

        fun formatFingerprint(der: ByteArray): String {
            val digest = MessageDigest.getInstance("SHA-256").digest(der)
            return digest.joinToString(":") { "%02X".format(it) }
        }
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

        val protocol = if (useWss) "WSS (加密 + TOFU指纹锁定)" else "Raw TCP (明文)"
        onLog("正在连接网关 [$protocol] -> $host:$port...")

        var socket: Socket? = null
        try {
            if (useWss) {
                socket = connectWSS()
            } else {
                val rawSocket = Socket()
                rawSocket.connect(InetSocketAddress(host, port), 10000)
                rawSocket.tcpNoDelay = true
                socket = rawSocket
                onLog("🔗 已连接到 VPCD 明文端口 $host:$port")
            }

            val input = DataInputStream(socket.getInputStream())
            val output = DataOutputStream(socket.getOutputStream())

            // Application-level pings complement the server's WebSocket heartbeat and keep
            // mobile NAT mappings alive while no APDU is being exchanged.
            val heartbeat = if (useWss) {
                launch {
                    while (isActive && !socket.isClosed) {
                        delay(25_000)
                        try {
                            synchronized(writeLock) { writeWsControlFrame(output, 0x09, byteArrayOf()) }
                        } catch (_: Exception) {
                            break
                        }
                    }
                }
            } else null

            try {
                while (coroutineContext.isActive && socket.isConnected && !socket.isClosed) {
                    val payload: ByteArray = if (useWss) {
                        readWsBinaryFrame(input, output) ?: break
                    } else {
                    val length = try {
                        input.readUnsignedShort()
                    } catch (e: Exception) {
                        onLog("Connection closed by server: ${e.message}")
                        break
                    }
                    if (length == 0) continue
                    val p = ByteArray(length)
                    input.readFully(p)
                    p
                    }

                    if (payload.isEmpty()) continue

                if (payload.size == 1) {
                    val ctrl = payload[0]
                    when (ctrl) {
                        VPCD_CTRL_ATR -> {
                            onLog(">> [VPCD] Server requested ATR -> Sending (${atr.size} bytes)")
                            synchronized(writeLock) {
                                if (useWss) {
                                    writeWsBinaryFrame(output, atr)
                                } else {
                                    output.writeShort(atr.size)
                                    output.write(atr)
                                    output.flush()
                                }
                            }
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
                    channel.transmit(payload)
                }

                val elapsed = System.currentTimeMillis() - startMs
                val respHex = resp.joinToString("") { "%02X".format(it) }
                onLog("TX: $apduHex  -->  RX: $respHex (${elapsed}ms)")

                    synchronized(writeLock) {
                        if (useWss) {
                            writeWsBinaryFrame(output, resp)
                        } else {
                            output.writeShort(resp.size)
                            output.write(resp)
                            output.flush()
                        }
                    }
                }
            } finally {
                heartbeat?.cancel()
            }
        } catch (e: Exception) {
            onLog("❌ Connection error: ${e.message}")
        } finally {
            try {
                socket?.close()
            } catch (_: Exception) {}
            onLog("VPCD session closed.")
        }
    }

    private fun connectWSS(): Socket {
        val trustManager = object : X509TrustManager {
            override fun getAcceptedIssuers(): Array<X509Certificate> = arrayOf()
            override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {}

            override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
                if (chain.isNullOrEmpty()) {
                    throw CertificateException("Server presented no TLS certificate")
                }
                val currentFp = formatFingerprint(chain[0].encoded)
                val prefs = context?.getSharedPreferences("mdd_tls_pins", Context.MODE_PRIVATE)

                if (resetPin) {
                    prefs?.edit()?.putString("pin_$host", currentFp)?.apply()
                    onLog("🔄 [安全] 已重置并信任新证书指纹:\n$currentFp")
                    return
                }

                val pinnedFp = prefs?.getString("pin_$host", null)
                if (pinnedFp == null) {
                    // Trust On First Use (TOFU)
                    prefs?.edit()?.putString("pin_$host", currentFp)?.apply()
                    onLog("🔒 [安全] 首次连接已锁定服务端证书指纹 (SHA-256):\n$currentFp")
                } else if (pinnedFp != currentFp) {
                    val alert = "⚠️ [安全告警] 服务端证书指纹发生突变！\n" +
                            "  已记录指纹: $pinnedFp\n" +
                            "  当前服务端: $currentFp\n" +
                            "可能遭受中间人 (MITM) 攻击！连接已中止。若确认服务端已更换证书，请点击【重置指纹】。"
                    onLog(alert)
                    throw CertificateException(alert)
                } else {
                    onLog("✅ [安全] TLS 证书指纹校验通过 ($currentFp)")
                }
            }
        }

        val sslContext = SSLContext.getInstance("TLS")
        sslContext.init(null, arrayOf<TrustManager>(trustManager), SecureRandom())
        val sslSocket = sslContext.socketFactory.createSocket() as SSLSocket
        sslSocket.connect(InetSocketAddress(host, port), 10000)
        sslSocket.startHandshake()

        // Send HTTP WebSocket Upgrade Request
        val randomBytes = ByteArray(16)
        SecureRandom().nextBytes(randomBytes)
        val secKey = Base64.encodeToString(randomBytes, Base64.NO_WRAP)

        var pathWithToken = if (wsPath.startsWith("/")) wsPath else "/$wsPath"
        val identityPrefs = context?.getSharedPreferences("mdd_agent_identity", Context.MODE_PRIVATE)
        var agentId = identityPrefs?.getString("agent_id", null)
        if (agentId.isNullOrBlank()) {
            agentId = UUID.randomUUID().toString()
            identityPrefs?.edit()?.putString("agent_id", agentId)?.apply()
        }
        val readerDigest = MessageDigest.getInstance("SHA-256")
            .digest(channel.channelName.trim().lowercase().toByteArray(Charsets.UTF_8))
            .take(12).joinToString("") { "%02x".format(it) }
        val params = mutableListOf(
            "slot=auto",
            "agent_id=${java.net.URLEncoder.encode(agentId, "UTF-8")}",
            "reader_id=${java.net.URLEncoder.encode(readerDigest, "UTF-8")}",
            "reader_name=${java.net.URLEncoder.encode(channel.channelName, "UTF-8")}",
        )
        if (token.isNotEmpty()) params.add("token=${java.net.URLEncoder.encode(token, "UTF-8")}")
        val sep = if (pathWithToken.contains("?")) "&" else "?"
        pathWithToken += sep + params.joinToString("&")

        val req = StringBuilder()
            .append("GET $pathWithToken HTTP/1.1\r\n")
            .append("Host: $host:$port\r\n")
            .append("Upgrade: websocket\r\n")
            .append("Connection: Upgrade\r\n")
            .append("Sec-WebSocket-Key: $secKey\r\n")
            .append("Sec-WebSocket-Version: 13\r\n")
        if (token.isNotEmpty()) {
            req.append("X-Agent-Token: $token\r\n")
        }
        req.append("\r\n")

        val os = sslSocket.getOutputStream()
        os.write(req.toString().toByteArray(Charsets.UTF_8))
        os.flush()

        // Read HTTP 101 Switching Protocols Response
        // Read exactly through CRLF-CRLF. A buffered wrapper may consume the first VPCD
        // WebSocket frame too; discarding that wrapper then loses the frame permanently.
        val statusLine = readHttpHeader(sslSocket.getInputStream()).lineSequence().firstOrNull() ?: ""
        if (!statusLine.contains("101")) {
            if (statusLine.contains("401") || statusLine.contains("403")) {
                throw IllegalStateException("网关拒绝连接 (Token 认证失败，请检查 Agent Token)")
            }
            throw IllegalStateException("WebSocket upgrade 失败: $statusLine")
        }

        onLog("✅ WSS WebSocket 连接成功: https://$host:$port${pathWithToken.substringBefore('?')}")
        return sslSocket
    }

    private fun readHttpHeader(inputStream: InputStream): String {
        val baos = ByteArrayOutputStream()
        var matched = 0
        val terminator = byteArrayOf('\r'.code.toByte(), '\n'.code.toByte(), '\r'.code.toByte(), '\n'.code.toByte())
        while (true) {
            val b = inputStream.read()
            if (b == -1) break
            baos.write(b)
            matched = if (b.toByte() == terminator[matched]) matched + 1
                else if (b == '\r'.code) 1 else 0
            if (matched == terminator.size) break
        }
        return baos.toString(Charsets.UTF_8.name())
    }

    private fun readWsBinaryFrame(input: DataInputStream, output: DataOutputStream): ByteArray? {
        while (true) {
            val b1 = try { input.readUnsignedByte() } catch (_: Exception) { return null }
            val opcode = b1 and 0x0F

            val b2 = input.readUnsignedByte()
            val isMasked = (b2 and 0x80) != 0
            var length = (b2 and 0x7F).toLong()

            if (length == 126L) {
                length = input.readUnsignedShort().toLong()
            } else if (length == 127L) {
                length = input.readLong()
            }

            val maskKey = if (isMasked) {
                val m = ByteArray(4)
                input.readFully(m)
                m
            } else null

            val payload = ByteArray(length.toInt())
            input.readFully(payload)

            if (maskKey != null) {
                for (i in payload.indices) {
                    payload[i] = (payload[i].toInt() xor maskKey[i % 4].toInt()).toByte()
                }
            }

            if (opcode == 0x08) { // Close
                return null
            }
            if (opcode == 0x09) { // Ping
                synchronized(writeLock) { writeWsControlFrame(output, 0x0A, payload) }
                continue
            }
            if (opcode == 0x0A) { // Pong
                continue
            }

            if (opcode == 0x02 || opcode == 0x01) { // Binary or Text
                return payload
            }
        }
    }

    private fun writeWsControlFrame(output: DataOutputStream, opcode: Int, data: ByteArray) {
        require(data.size <= 125)
        output.writeByte(0x80 or opcode)
        val maskKey = ByteArray(4).also { SecureRandom().nextBytes(it) }
        output.writeByte(0x80 or data.size)
        output.write(maskKey)
        data.forEachIndexed { index, value ->
            output.writeByte(value.toInt() xor maskKey[index % 4].toInt())
        }
        output.flush()
    }

    private fun writeWsBinaryFrame(output: DataOutputStream, data: ByteArray) {
        val b1 = 0x80 or 0x02 // FIN + Binary
        output.writeByte(b1)

        val maskKey = ByteArray(4)
        SecureRandom().nextBytes(maskKey)

        val len = data.size
        if (len < 126) {
            output.writeByte(0x80 or len) // Masked
        } else if (len <= 65535) {
            output.writeByte(0x80 or 126)
            output.writeShort(len)
        } else {
            output.writeByte(0x80 or 127)
            output.writeLong(len.toLong())
        }

        output.write(maskKey)

        val masked = ByteArray(len)
        for (i in 0 until len) {
            masked[i] = (data[i].toInt() xor maskKey[i % 4].toInt()).toByte()
        }
        output.write(masked)
        output.flush()
    }
}
