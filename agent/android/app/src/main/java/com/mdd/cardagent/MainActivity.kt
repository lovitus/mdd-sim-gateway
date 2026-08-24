package com.mdd.cardagent

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.widget.ArrayAdapter
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.app.ActivityCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.lifecycleScope
import com.mdd.cardagent.databinding.ActivityMainBinding
import com.mdd.cardagent.service.CardForwarderService
import com.mdd.cardagent.smartcard.SmartCardManager
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private var resetPinNextRun = false

    private val channelOptions = listOf(
        "自动检测 (USB OTG 优先，其次 OMAPI)" to SmartCardManager.ChannelType.AUTO,
        "内置 SIM / eSIM (OMAPI)" to SmartCardManager.ChannelType.OMAPI,
        "外接 USB-C 读卡器 (CCID 免驱动)" to SmartCardManager.ChannelType.USB_CCID,
        "系统 Telephony UICC 降级通道" to SmartCardManager.ChannelType.TELEPHONY
    )

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        loadSavedConfig()
        requestPermissions()
        setupUI()
        observeServiceState()
    }

    private fun loadSavedConfig() {
        val prefs = getSharedPreferences("mdd_agent_config", Context.MODE_PRIVATE)
        binding.editHost.setText(prefs.getString("host", ""))
        binding.editPort.setText(prefs.getString("port", "8443"))
        binding.editToken.setText(prefs.getString("token", ""))
        binding.switchWss.isChecked = prefs.getBoolean("use_wss", true)
    }

    private fun saveConfig() {
        val prefs = getSharedPreferences("mdd_agent_config", Context.MODE_PRIVATE)
        prefs.edit()
            .putString("host", binding.editHost.text?.toString()?.trim())
            .putString("port", binding.editPort.text?.toString()?.trim())
            .putString("token", binding.editToken.text?.toString()?.trim())
            .putBoolean("use_wss", binding.switchWss.isChecked)
            .apply()
    }

    private fun setupUI() {
        val adapter = ArrayAdapter(
            this,
            android.R.layout.simple_spinner_dropdown_item,
            channelOptions.map { it.first }
        )
        binding.spinnerChannel.adapter = adapter

        binding.switchWss.setOnCheckedChangeListener { _, isChecked ->
            if (isChecked && binding.editPort.text?.toString() == "35963") {
                binding.editPort.setText("8443")
            } else if (!isChecked && binding.editPort.text?.toString() == "8443") {
                binding.editPort.setText("35963")
            }
        }

        binding.btnResetPin.setOnClickListener {
            val host = binding.editHost.text?.toString()?.trim() ?: ""
            if (host.isNotEmpty()) {
                val pins = getSharedPreferences("mdd_tls_pins", Context.MODE_PRIVATE)
                pins.edit().remove("pin_$host").apply()
                resetPinNextRun = true
                Toast.makeText(this, "已清除 $host 的证书指纹锁定，下次连接将重新学习信任", Toast.LENGTH_LONG).show()
                binding.tvLogs.append("[安全] 已清除 $host 的证书指纹锁定，下次连接时自动重新锁定新证书指纹\n")
            }
        }

        binding.btnToggleService.setOnClickListener {
            val isRunning = CardForwarderService.isRunningFlow.value
            if (isRunning) {
                stopService(Intent(this, CardForwarderService::class.java))
            } else {
                saveConfig()
                requestBatteryOptimizationExemption()
                val host = binding.editHost.text?.toString()?.trim().orEmpty()
                if (host.isEmpty()) {
                    binding.editHost.error = "请输入网关地址"
                    return@setOnClickListener
                }
                val port = binding.editPort.text?.toString()?.trim()?.toIntOrNull() ?: 8443
                val token = binding.editToken.text?.toString()?.trim() ?: ""
                val useWss = binding.switchWss.isChecked
                val selectedChannel = channelOptions[binding.spinnerChannel.selectedItemPosition].second

                val serviceIntent = Intent(this, CardForwarderService::class.java).apply {
                    putExtra(CardForwarderService.EXTRA_HOST, host)
                    putExtra(CardForwarderService.EXTRA_PORT, port)
                    putExtra(CardForwarderService.EXTRA_TOKEN, token)
                    putExtra(CardForwarderService.EXTRA_USE_WSS, useWss)
                    putExtra(CardForwarderService.EXTRA_RESET_PIN, resetPinNextRun)
                    putExtra(CardForwarderService.EXTRA_CHANNEL_TYPE, selectedChannel.name)
                }
                resetPinNextRun = false

                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                    startForegroundService(serviceIntent)
                } else {
                    startService(serviceIntent)
                }
            }
        }

        binding.btnClearLog.setOnClickListener {
            binding.tvLogs.text = ""
        }
    }

    private fun requestBatteryOptimizationExemption() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return
        val powerManager = getSystemService(Context.POWER_SERVICE) as PowerManager
        if (powerManager.isIgnoringBatteryOptimizations(packageName)) return
        try {
            startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS).apply {
                data = Uri.parse("package:$packageName")
            })
        } catch (_: Exception) {
            // Some vendor ROMs do not implement the per-app request screen.
            try { startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)) }
            catch (_: Exception) {}
        }
    }

    private fun observeServiceState() {
        lifecycleScope.launch {
            CardForwarderService.isRunningFlow.collectLatest { running ->
                binding.btnToggleService.text = if (running) {
                    getString(R.string.stop_service)
                } else {
                    getString(R.string.start_service)
                }
                binding.btnToggleService.setBackgroundColor(
                    if (running) ContextCompat.getColor(this@MainActivity, R.color.status_red)
                    else ContextCompat.getColor(this@MainActivity, R.color.primary)
                )
                binding.editHost.isEnabled = !running
                binding.editPort.isEnabled = !running
                binding.editToken.isEnabled = !running
                binding.switchWss.isEnabled = !running
                binding.spinnerChannel.isEnabled = !running
            }
        }

        lifecycleScope.launch {
            CardForwarderService.logFlow.collect { logLine ->
                binding.tvLogs.append(logLine + "\n")
                binding.scrollLog.post {
                    binding.scrollLog.fullScroll(android.view.View.FOCUS_DOWN)
                }
            }
        }
    }

    private fun requestPermissions() {
        val permissions = mutableListOf<String>()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            if (ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
                permissions.add(Manifest.permission.POST_NOTIFICATIONS)
            }
        }
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.READ_PHONE_STATE) != PackageManager.PERMISSION_GRANTED) {
            permissions.add(Manifest.permission.READ_PHONE_STATE)
        }

        if (permissions.isNotEmpty()) {
            ActivityCompat.requestPermissions(this, permissions.toTypedArray(), 100)
        }
    }
}
