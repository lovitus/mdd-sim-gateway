# MDD Card Agent (跨平台智能卡转发客户端)

需要管理 4G/5G 模块的主机应运行统一入口 `mdd-modem-agent`；它也会自动管理同一主机上的
所有 PC/SC/eSIM 读卡器，不需要再启动本程序。安装和 Provider 说明见
[MODEM_AGENT.md](MODEM_AGENT.md)。仅有读卡器的轻量部署仍可使用本文的 `mdd-card-agent`。

`mdd-card-agent` 是专为 MDD VoWiFi 网关打造的轻量级、安全、跨平台 PC/SC 智能卡转发客户端。

你可以把 USB 智能卡读卡器（如通用 CCID 读卡器、ESTKme、9e SIM 卡槽、Gemalto 读卡器）插在任意 **Windows、macOS 或 Linux** 电脑上，运行该客户端，即可通过 **加密 WSS 管道 + TOFU 证书指纹锁定 + 共享 Token 鉴权** 将 SIM 卡的鉴权 APDU 安全转发至远端 VoWiFi 网关。

---

## 核心安全与技术特性

1. **WSS 加密通道与 IP 直连 TOFU 证书指纹锁定**：
   - 客户端默认通过 `wss://<IP>:8443/mdd/api/vpcd/ws` 加密连接，公网无需额外暴露明文 `35963` 端口。
   - 首次连接自动信任并保存服务端证书的 SHA-256 指纹（TOFU 机制）；后续连接自动比对，杜绝中间人劫持与证书伪造。
2. **多端共享 Agent Token 鉴权**：
   - 支持在 WebUI【系统设置】->【安全】中自定义设置助记 Token。
   - 多个 Android 手机、多台 PC/树莓派客户端可以同时配置**同一个 Token** 并发连接网关。
3. **单文件零依赖（全平台原生编译）**：
   - **Windows**：原生调用系统的 `WinSCard.dll`，无需额外安装驱动。
   - **macOS**：原生链接 Apple `PCSC.framework`，即插即用。
   - **Linux**：使用标准 `libpcsclite`。
4. **硬件级物理防删保护 (Safety Guard)**：
   - 客户端在发送 APDU 到物理卡前进行硬编码拦截，严禁 `ES10c.DeleteProfile`（物理删除 eSIM 卡数据，Tag `0xBF33`）以及 `DELETE FILE`（INS `0xE4`）指令，彻底保护卡内资产。
5. **极低流量与断线无缝重连**：
   - VoWiFi 仅在 IKEv2 协商与 SIP REGISTER 鉴权时调用 SIM 运算（每次仅产生几百字节流量，耗时数十毫秒）。
   - 网络波动或热插拔读卡器后会自动重连并恢复鉴权通道。

---

## 快速使用说明 (GitHub Release 二进制)

可直接在 [GitHub Releases](https://github.com/lovitus/mdd-sim-gateway/releases) 下载对应平台的二进制文件：

### 1. Windows (x64)
```cmd
:: 首次连接（自动记录证书指纹）：
mdd-card-agent-windows-amd64.exe -gateway 10.44.0.14 -port 8443 -token "<YOUR_AGENT_TOKEN>"

:: 简写格式：
mdd-card-agent-windows-amd64.exe -server 10.44.0.14:8443 -token "<YOUR_AGENT_TOKEN>"
```

### 2. macOS (Apple Silicon M1~M4 / Intel)
```bash
chmod +x mdd-card-agent-darwin-arm64
./mdd-card-agent-darwin-arm64 -gateway 10.44.0.14 -port 8443 -token "<YOUR_AGENT_TOKEN>"
```

### 3. Linux (Ubuntu / Debian / CentOS / Alpine / 树莓派)
```bash
chmod +x mdd-card-agent-linux-amd64
./mdd-card-agent-linux-amd64 -gateway 10.44.0.14 -port 8443 -token "<YOUR_AGENT_TOKEN>"
```

---

## 命令行参数一览

| 参数 | 默认值 | 作用说明 |
| :--- | :--- | :--- |
| `-gateway` | `127.0.0.1` | 网关宿主机 IP 地址或域名 |
| `-port` | `8443` | 网关 HTTPS / WSS 端口（默认 8443） |
| `-server` | `""` | 简写地址，格式为 `ip:port`（如 `10.44.0.14:8443`） |
| `-token` | `""` | 网关 Agent Token（也可通过环境变量 `MDD_AGENT_TOKEN` 传入） |
| `-wss` | `true` | 是否启用 WSS 加密传输与 TOFU 证书指纹锁定 |
| `-pin` | `""` | 显式指定期望的服务端证书 SHA-256 指纹（如 `75:9E:08:...`） |
| `-reset-pin` | `false` | 重置本地持久化的证书指纹并重新信任当前服务端证书 |
| `-reader` | `""` | 读卡器名称关键字过滤（如 `"ESTKme"` 或 `"OMNIKEY"`） |
| `-retry` | `3` | 掉线或读卡器异常时的自动重试间隔（秒） |

---

## 进阶使用示例

```bash
# 1. 过滤指定读卡器连接
./mdd-card-agent -server 10.44.0.14:8443 -token "my-secret-token" -reader "ESTKme"

# 2. 服务端重签证书后，重置信任指纹
./mdd-card-agent -server 10.44.0.14:8443 -token "my-secret-token" -reset-pin

# 3. 显式锁定指定证书指纹
./mdd-card-agent -server 10.44.0.14:8443 -token "my-secret-token" -pin "75:9E:08:73:9F:B3:5D:31:1B:22:8E:69:E1:A4:DD:B2:51:11:45:87:87:28:78:90:11:A0:67:A1:92:3A:05:BF"

# 4. 局域网无加密旧版明文 TCP 传输（不推荐跨公网使用）
./mdd-card-agent -gateway 10.44.0.14 -port 35963 -wss=false
```
