package com.mdd.cardagent.smartcard

interface ISmartCardChannel {
    val channelName: String
    val isConnected: Boolean
    
    /**
     * Connects to the card, powers it on, and returns the ATR (Answer To Reset).
     */
    suspend fun connect(): Result<ByteArray>

    /**
     * Transmits a raw APDU to the card and returns the response (Data + Status Word SW1 SW2).
     */
    suspend fun transmit(apdu: ByteArray): ByteArray

    /**
     * Resets / power cycles the smart card.
     */
    suspend fun reset(): Boolean

    /**
     * Disconnects from the smart card and releases handles.
     */
    fun disconnect()
}
