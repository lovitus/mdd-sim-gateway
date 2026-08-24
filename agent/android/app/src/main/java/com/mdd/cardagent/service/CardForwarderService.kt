package com.mdd.cardagent.service

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.Binder
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import android.os.SystemClock
import androidx.core.app.NotificationCompat
import com.mdd.cardagent.MainActivity
import com.mdd.cardagent.R
import com.mdd.cardagent.smartcard.SmartCardManager
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

class CardForwarderService : Service() {

    private val binder = LocalBinder()
    private val serviceScope = CoroutineScope(Dispatchers.Default + Job())
    private var workerJob: Job? = null
    private var wakeLock: PowerManager.WakeLock? = null

    companion object {
        const val CHANNEL_ID = "mdd_forwarder_channel"
        const val NOTIFICATION_ID = 1001

        const val EXTRA_HOST = "extra_host"
        const val EXTRA_PORT = "extra_port"
        const val EXTRA_TOKEN = "extra_token"
        const val EXTRA_USE_WSS = "extra_use_wss"
        const val EXTRA_RESET_PIN = "extra_reset_pin"
        const val EXTRA_CHANNEL_TYPE = "extra_channel_type"

        private val _isRunningFlow = MutableStateFlow(false)
        val isRunningFlow: StateFlow<Boolean> = _isRunningFlow.asStateFlow()

        private val _logFlow = MutableSharedFlow<String>(replay = 50)
        val logFlow: SharedFlow<String> = _logFlow.asSharedFlow()
    }

    inner class LocalBinder : Binder() {
        fun getService(): CardForwarderService = this@CardForwarderService
    }

    override fun onBind(intent: Intent?): IBinder = binder

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()

        val powerManager = getSystemService(Context.POWER_SERVICE) as PowerManager
        wakeLock = powerManager.newWakeLock(
            PowerManager.PARTIAL_WAKE_LOCK, "MddCardAgent::ForwarderWakeLock"
        ).apply { setReferenceCounted(false) }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val saved = getSharedPreferences("mdd_agent_config", Context.MODE_PRIVATE)
        val host = intent?.getStringExtra(EXTRA_HOST) ?: saved.getString("host", "").orEmpty()
        val port = intent?.getIntExtra(EXTRA_PORT, 8443)
            ?: saved.getString("port", "8443")?.toIntOrNull() ?: 8443
        val token = intent?.getStringExtra(EXTRA_TOKEN) ?: saved.getString("token", "")!!
        val useWss = intent?.getBooleanExtra(EXTRA_USE_WSS, port == 8443)
            ?: saved.getBoolean("use_wss", port == 8443)
        var resetPin = intent?.getBooleanExtra(EXTRA_RESET_PIN, false) ?: false
        val channelTypeStr = intent?.getStringExtra(EXTRA_CHANNEL_TYPE) ?: SmartCardManager.ChannelType.AUTO.name
        val channelType = try {
            SmartCardManager.ChannelType.valueOf(channelTypeStr)
        } catch (_: Exception) {
            SmartCardManager.ChannelType.AUTO
        }

        val modeLabel = if (useWss) "WSS 加密" else "Raw TCP"
        if (host.isBlank()) {
            startForeground(NOTIFICATION_ID, buildNotification("网关地址未配置"))
            emitLog("Gateway address is not configured; service stopped")
            stopSelf(startId)
            return START_NOT_STICKY
        }
        startForeground(NOTIFICATION_ID, buildNotification("正在运行 [$modeLabel] -> $host:$port"))
        _isRunningFlow.value = true
        try {
            if (wakeLock?.isHeld != true) wakeLock?.acquire()
        } catch (_: Exception) {}

        workerJob?.cancel()
        workerJob = serviceScope.launch {
            emitLog("=== MDD Card Agent Service Started ===")
            var reconnectDelayMs = 3_000L
            while (isActive) {
                val channel = SmartCardManager.createChannel(applicationContext, channelType)
                val client = VpcdClient(
                    host = host,
                    port = port,
                    token = token,
                    useWss = useWss,
                    resetPin = resetPin,
                    context = applicationContext,
                    channel = channel
                ) { logMsg ->
                    emitLog(logMsg)
                    if (logMsg.contains("WebSocket 连接成功") || logMsg.contains("已连接到 VPCD")) {
                        updateNotification("已连接 [$modeLabel] -> $host:$port")
                    }
                }

                // Reset pin only once on service start
                resetPin = false

                val sessionStartedAt = SystemClock.elapsedRealtime()
                client.run()
                val sessionDurationMs = SystemClock.elapsedRealtime() - sessionStartedAt

                if (isActive) {
                    // A durable session resets the backoff. Repeated short failures increase
                    // it to avoid battery/network churn while the gateway or mobile path is
                    // unavailable; stable reader identity still preserves the same slot.
                    if (sessionDurationMs >= 120_000L) reconnectDelayMs = 3_000L
                    val waitSeconds = reconnectDelayMs / 1_000L
                    updateNotification("连接已断开，${waitSeconds}秒后重连…")
                    emitLog("Reconnecting in ${waitSeconds} seconds...")
                    delay(reconnectDelayMs)
                    if (sessionDurationMs < 120_000L) {
                        reconnectDelayMs = (reconnectDelayMs * 2L).coerceAtMost(60_000L)
                    }
                }
            }
        }

        return START_STICKY
    }

    private fun emitLog(msg: String) {
        serviceScope.launch {
            _logFlow.emit(msg)
        }
    }

    private fun updateNotification(statusText: String) {
        getSystemService(NotificationManager::class.java)
            ?.notify(NOTIFICATION_ID, buildNotification(statusText))
    }

    private fun buildNotification(statusText: String): Notification {
        val pendingIntent = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )

        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle("MDD SIM 转发服务")
            .setContentText(statusText)
            .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
            .setContentIntent(pendingIntent)
            .setOngoing(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                getString(R.string.notification_channel_name),
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = getString(R.string.notification_channel_desc)
            }
            val manager = getSystemService(NotificationManager::class.java)
            manager?.createNotificationChannel(channel)
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        workerJob?.cancel()
        serviceScope.cancel()
        _isRunningFlow.value = false
        try { if (wakeLock?.isHeld == true) wakeLock?.release() } catch (_: Exception) {}
        emitLog("=== MDD Card Agent Service Stopped ===")
    }
}
