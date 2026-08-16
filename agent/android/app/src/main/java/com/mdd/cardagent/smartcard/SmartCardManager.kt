package com.mdd.cardagent.smartcard

import android.content.Context
import android.hardware.usb.UsbManager

object SmartCardManager {

    enum class ChannelType {
        AUTO,
        OMAPI,
        USB_CCID,
        TELEPHONY
    }

    fun createChannel(context: Context, type: ChannelType): ISmartCardChannel {
        return when (type) {
            ChannelType.AUTO -> {
                val usbManager = context.getSystemService(Context.USB_SERVICE) as UsbManager
                val hasUsbReader = usbManager.deviceList.values.any { dev ->
                    (0 until dev.interfaceCount).any { dev.getInterface(it).interfaceClass == 11 }
                }
                if (hasUsbReader) {
                    UsbCcidCardChannel(context)
                } else {
                    OmapiCardChannel(context)
                }
            }
            ChannelType.OMAPI -> OmapiCardChannel(context)
            ChannelType.USB_CCID -> UsbCcidCardChannel(context)
            ChannelType.TELEPHONY -> TelephonyCardChannel(context)
        }
    }
}
