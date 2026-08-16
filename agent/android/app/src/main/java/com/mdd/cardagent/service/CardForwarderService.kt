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
        wakeLock = powerManager.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "MddCardAgent::ForwarderWakeLock")
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val host = intent?.getStringExtra(EXTRA_HOST) ?: "10.44.0.14"
        val port = intent?.getIntExtra(EXTRA_PORT, 35963) ?: 35963
        val channelTypeStr = intent?.getStringExtra(EXTRA_CHANNEL_TYPE) ?: SmartCardManager.ChannelType.AUTO.name
        val channelType = try {
            SmartCardManager.ChannelType.valueOf(channelTypeStr)
        } catch (_: Exception) {
            SmartCardManager.ChannelType.AUTO
        }

        startForeground(NOTIFICATION_ID, buildNotification("正在运行 - 连接目标: $host:$port"))
        _isRunningFlow.value = true
        wakeLock?.acquire(24 * 60 * 60 * 1000L) // 24 hours max

        workerJob?.cancel()
        workerJob = serviceScope.launch {
            emitLog("=== MDD Card Agent Service Started ===")
            while (isActive) {
                val channel = SmartCardManager.createChannel(applicationContext, channelType)
                val client = VpcdClient(host, port, channel) { logMsg ->
                    emitLog(logMsg)
                }

                client.run()

                if (isActive) {
                    emitLog("Reconnecting in 3 seconds...")
                    delay(3000)
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
        if (wakeLock?.isHeld == true) {
            wakeLock?.release()
        }
        emitLog("=== MDD Card Agent Service Stopped ===")
    }
}
