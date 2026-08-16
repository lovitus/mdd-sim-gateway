# MDD Card Agent for Android (SIM / eSIM 读卡与转发客户端)

MDD Card Agent (Android) 是专为 Android 移动设备设计的智能卡 / SIM 卡硬件转发客户端。参考了 **EasyEUICC** 与 **OpenEUICC** 的硬件访问设计，支持将手机插槽中的 SIM 卡、内置 eSIM 或通过 Type-C OTG 外接的 USB 智能卡读卡器安全透明地转发给 MDD VoWiFi 网关。

---

## 🌟 核心特性

1. **多硬件通道支持**：
   - **OMAPI 智能卡通道** (`android.se.omapi`): 直接读取 Android 手机卡槽内 SIM 卡（SIM1/SIM2/eUICC/eSE）。
   - **USB-OTG CCID 免驱动通道**: 零 Root、零权限限制，直连 Type-C USB 智能卡读卡器（如 ACR39U、Identiv 等）。
   - **Telephony UICC 降级通道**: 针对特定系统支持的底层 APDU 传输 fallback。
2. **硬件安全级防护 (`ApduGuard`)**：
   - 内置硬隔离规则，100% 自动拦截 SGP.22 `ES10c.DeleteProfile` (0xBF33) 以及 ISO 7816 `DELETE FILE` (0xE4) 指令，确保远端无法误删 eSIM Profile 或破坏物理卡。
3. **24/7 常驻前台服务 (`ForegroundService`)**：
   - 自动持有 `PartialWakeLock` 与常驻通知栏状态，锁屏和省电模式下保持长连接 APDU 毫秒级转发不掉线。
4. **实时 APDU 调试控制台**：
   - Material 3 现代化界面，实时滚动展示 APDU 指令、ATR 特征码、时延统计与状态字。

---

## 📱 使用说明

1. **安装 APK**：从 Release 下载 `mdd-card-agent.apk` 并安装到 Android 手机。
2. **连接配置**：
   - **网关地址**：输入网关服务器 IP 或域名（如 `10.44.0.14`）。
   - **端口**：默认为 `8443`（加密 WSS 端口）或 `35963`（本地明文端口）。
   - **Agent Token**：填入在 WebUI【系统设置】->【安全】中配置的共享 Agent Token。
   - **WSS 加密**：保持勾选，首次连接自动信任并保存服务端证书指纹。
   - **读取通道**：推荐保留默认“自动检测”。
3. **启动转发**：
   - 点击 **“启动 SIM 转发”**，通知栏将显示常驻运行状态，控制台开始实时输出 APDU 数据流。
   - 如服务端证书更换，可点击 **“重置指纹”** 重新信任。
