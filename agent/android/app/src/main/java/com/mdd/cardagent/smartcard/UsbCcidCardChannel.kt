package com.mdd.cardagent.smartcard

import android.content.Context
import android.hardware.usb.UsbConstants
import android.hardware.usb.UsbDevice
import android.hardware.usb.UsbDeviceConnection
import android.hardware.usb.UsbEndpoint
import android.hardware.usb.UsbInterface
import android.hardware.usb.UsbManager
import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.nio.ByteBuffer
import java.nio.ByteOrder

class UsbCcidCardChannel(
    private val context: Context,
    private val targetDevice: UsbDevice? = null
) : ISmartCardChannel {

    private val tag = "UsbCcidCardChannel"
    private var usbConnection: UsbDeviceConnection? = null
    private var usbInterface: UsbInterface? = null
    private var bulkIn: UsbEndpoint? = null
    private var bulkOut: UsbEndpoint? = null
    private var seq: Byte = 0
    private var atrBytes: ByteArray = byteArrayOf()

    companion object {
        private const val CCID_CMD_ICC_POWER_ON = 0x62.toByte()
        private const val CCID_CMD_ICC_POWER_OFF = 0x63.toByte()
        private const val CCID_CMD_XFR_BLOCK = 0x6F.toByte()
        private const val CCID_RSP_DATA_BLOCK = 0x80.toByte()
        private const val CCID_RSP_SLOT_STATUS = 0x81.toByte()
        private const val USB_TIMEOUT_MS = 5000
    }

    override val channelName: String
        get() = targetDevice?.productName ?: "USB CCID Reader"

    override val isConnected: Boolean
        get() = usbConnection != null && bulkIn != null && bulkOut != null

    override suspend fun connect(): Result<ByteArray> = withContext(Dispatchers.IO) {
        try {
            val usbManager = context.getSystemService(Context.USB_SERVICE) as UsbManager
            val device = targetDevice ?: findCcidDevice(usbManager)
                ?: return@withContext Result.failure(IllegalStateException("No USB CCID smartcard reader detected"))

            val intf = findCcidInterface(device)
                ?: return@withContext Result.failure(IllegalStateException("Device does not expose CCID interface"))

            val connection = usbManager.openDevice(device)
                ?: return@withContext Result.failure(SecurityException("Unable to open USB device. Permission required."))

            if (!connection.claimInterface(intf, true)) {
                connection.close()
                return@withContext Result.failure(IllegalStateException("Could not claim USB CCID interface"))
            }

            usbConnection = connection
            usbInterface = intf

            for (i in 0 until intf.endpointCount) {
                val ep = intf.getEndpoint(i)
                if (ep.type == UsbConstants.USB_ENDPOINT_XFER_BULK) {
                    if (ep.direction == UsbConstants.USB_DIR_IN) {
                        bulkIn = ep
                    } else {
                        bulkOut = ep
                    }
                }
            }

            if (bulkIn == null || bulkOut == null) {
                disconnect()
                return@withContext Result.failure(IllegalStateException("CCID Bulk IN/OUT endpoints not found"))
            }

            // Power on the card (PC_to_RDR_IccPowerOn)
            val powerOnAtr = iccPowerOn()
            if (powerOnAtr.isEmpty()) {
                disconnect()
                return@withContext Result.failure(IllegalStateException("ICC Power ON failed or no card inserted"))
            }

            atrBytes = powerOnAtr
            Log.i(tag, "Successfully powered on USB Smart Card. ATR: ${powerOnAtr.joinToString("") { "%02X".format(it) }}")
            Result.success(atrBytes)
        } catch (e: Exception) {
            Log.e(tag, "USB CCID connect failed: ${e.message}", e)
            disconnect()
            Result.failure(e)
        }
    }

    override suspend fun transmit(apdu: ByteArray): ByteArray = withContext(Dispatchers.IO) {
        try {
            val conn = usbConnection ?: return@withContext byteArrayOf(0x6F, 0x00)
            val epOut = bulkOut ?: return@withContext byteArrayOf(0x6F, 0x00)
            val epIn = bulkIn ?: return@withContext byteArrayOf(0x6F, 0x00)

            val currentSeq = seq++
            val header = ByteBuffer.allocate(10).order(ByteOrder.LITTLE_ENDIAN).apply {
                put(CCID_CMD_XFR_BLOCK)
                putInt(apdu.size)     // dwLength
                put(0.toByte())        // bSlot
                put(currentSeq)        // bSeq
                put(0.toByte())        // bBWI
                putShort(0.toShort())  // wLevelParameter
            }.array()

            val request = header + apdu
            val sent = conn.bulkTransfer(epOut, request, request.size, USB_TIMEOUT_MS)
            if (sent < 0) {
                Log.e(tag, "USB CCID transmit write failed")
                return@withContext byteArrayOf(0x6F, 0x00)
            }

            val buffer = ByteArray(512)
            val received = conn.bulkTransfer(epIn, buffer, buffer.size, USB_TIMEOUT_MS)
            if (received < 10) {
                Log.e(tag, "USB CCID transmit read failed or truncated (len=$received)")
                return@withContext byteArrayOf(0x6F, 0x00)
            }

            val msgType = buffer[0]
            val dataLen = ByteBuffer.wrap(buffer, 1, 4).order(ByteOrder.LITTLE_ENDIAN).int
            val status = buffer[7]
            val error = buffer[8]

            if (msgType == CCID_RSP_DATA_BLOCK && dataLen >= 2) {
                val rapdu = ByteArray(dataLen)
                System.arraycopy(buffer, 10, rapdu, 0, minOf(dataLen, received - 10))
                return@withContext rapdu
            }

            Log.w(tag, "USB CCID unexpected response (msgType=$msgType, status=$status, error=$error)")
            return@withContext byteArrayOf(0x6F, 0x00)
        } catch (e: Exception) {
            Log.e(tag, "USB CCID transmit error: ${e.message}", e)
            return@withContext byteArrayOf(0x6F, 0x00)
        }
    }

    override suspend fun reset(): Boolean = withContext(Dispatchers.IO) {
        try {
            val newAtr = iccPowerOn()
            if (newAtr.isNotEmpty()) {
                atrBytes = newAtr
                true
            } else {
                false
            }
        } catch (e: Exception) {
            false
        }
    }

    override fun disconnect() {
        try {
            iccPowerOff()
            usbInterface?.let { usbConnection?.releaseInterface(it) }
            usbConnection?.close()
        } catch (e: Exception) {
            Log.w(tag, "USB CCID disconnect warning: ${e.message}")
        } finally {
            usbConnection = null
            usbInterface = null
            bulkIn = null
            bulkOut = null
        }
    }

    private fun iccPowerOn(): ByteArray {
        val conn = usbConnection ?: return byteArrayOf()
        val epOut = bulkOut ?: return byteArrayOf()
        val epIn = bulkIn ?: return byteArrayOf()

        val currentSeq = seq++
        val header = ByteBuffer.allocate(10).order(ByteOrder.LITTLE_ENDIAN).apply {
            put(CCID_CMD_ICC_POWER_ON)
            putInt(0)             // dwLength
            put(0.toByte())        // bSlot
            put(currentSeq)        // bSeq
            put(0.toByte())        // bPowerSelect (0=Auto)
            putShort(0.toShort())  // abRFU
        }.array()

        conn.bulkTransfer(epOut, header, header.size, USB_TIMEOUT_MS)
        val buffer = ByteArray(512)
        val received = conn.bulkTransfer(epIn, buffer, buffer.size, USB_TIMEOUT_MS)
        if (received >= 10 && buffer[0] == CCID_RSP_DATA_BLOCK) {
            val dataLen = ByteBuffer.wrap(buffer, 1, 4).order(ByteOrder.LITTLE_ENDIAN).int
            if (dataLen > 0 && received >= 10 + dataLen) {
                val atr = ByteArray(dataLen)
                System.arraycopy(buffer, 10, atr, 0, dataLen)
                return atr
            }
        }
        return byteArrayOf()
    }

    private fun iccPowerOff() {
        val conn = usbConnection ?: return
        val epOut = bulkOut ?: return
        val currentSeq = seq++
        val header = ByteBuffer.allocate(10).order(ByteOrder.LITTLE_ENDIAN).apply {
            put(CCID_CMD_ICC_POWER_OFF)
            putInt(0)
            put(0.toByte())
            put(currentSeq)
            put(0.toByte())
            putShort(0.toShort())
        }.array()
        conn.bulkTransfer(epOut, header, header.size, USB_TIMEOUT_MS)
    }

    private fun findCcidDevice(manager: UsbManager): UsbDevice? {
        for (device in manager.deviceList.values) {
            if (findCcidInterface(device) != null) {
                return device
            }
        }
        return null
    }

    private fun findCcidInterface(device: UsbDevice): UsbInterface? {
        for (i in 0 until device.interfaceCount) {
            val intf = device.getInterface(i)
            // Class 11 (0x0B) is USB CCID Smart Card
            if (intf.interfaceClass == 11) {
                return intf
            }
        }
        return null
    }
}
