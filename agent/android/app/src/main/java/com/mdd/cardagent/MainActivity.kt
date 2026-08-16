package com.mdd.cardagent

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.widget.ArrayAdapter
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

        requestPermissions()
        setupUI()
        observeServiceState()
    }

    private fun setupUI() {
        val adapter = ArrayAdapter(
            this,
            android.R.layout.simple_spinner_dropdown_item,
            channelOptions.map { it.first }
        )
        binding.spinnerChannel.adapter = adapter

        binding.btnToggleService.setOnClickListener {
            val isRunning = CardForwarderService.isRunningFlow.value
            if (isRunning) {
                stopService(Intent(this, CardForwarderService::class.java))
            } else {
                val host = binding.editHost.text?.toString()?.trim() ?: "10.44.0.14"
                val port = binding.editPort.text?.toString()?.trim()?.toIntOrNull() ?: 35963
                val selectedChannel = channelOptions[binding.spinnerChannel.selectedItemPosition].second

                val serviceIntent = Intent(this, CardForwarderService::class.java).apply {
                    putExtra(CardForwarderService.EXTRA_HOST, host)
                    putExtra(CardForwarderService.EXTRA_PORT, port)
                    putExtra(CardForwarderService.EXTRA_CHANNEL_TYPE, selectedChannel.name)
                }
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
