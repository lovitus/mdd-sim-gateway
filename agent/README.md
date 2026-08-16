# MDD Card Agent (跨平台智能卡转发客户端)

`mdd-card-agent` 是专为 MDD VoWiFi 网关打造的轻量级、跨平台 PC/SC 智能卡转发代理。

你可以把 USB 智能卡读卡器（如通用 CCID 读卡器、ESTKme、9e SIM 卡槽、Gemalto 读卡器）插在任意 **Windows、macOS 或 Linux** 电脑上，运行该客户端，即可将 SIM 卡的鉴权 APDU 透明转发至远端 VoWiFi 网关服务端。

---

## 核心技术特性

1. **单文件零依赖（Go 编译产物）**：
   - **Windows**：原生调用系统的 `WinSCard.dll`，无需额外安装任何驱动或运行时。
   - **macOS**：原生链接 Apple `PCSC.framework`，即插即用。
   - **Linux**：使用标准 `libpcsclite`。
2. **极低流量与断线无缝重连**：
   - VoWiFi 仅在 IKEv2 协商与 SIP REGISTER 鉴权时调用 SIM 运算（每次仅产生几百字节流量，耗时数十毫秒）。
   - 内置智能心跳检测与热插拔重连：网络波动或插拔读卡器后会自动重连并恢复鉴权通道。
3. **硬件级物理防删保护 (Safety Guard)**：
   - 客户端在发送 APDU 到物理卡前进行硬编码拦截，严禁 `ES10c.DeleteProfile`（物理删除 eSIM 卡数据，Tag `0xBF33`）以及 `DELETE FILE`（INS `0xE4`）指令，彻底避免误操作擦除卡内资产。
4. **多读卡器支持与名称过滤**：
   - 支持插入多个读卡器，通过 `-reader` 参数指定子串匹配。

---

## 快速使用 (已编译二进制文件)

可直接在 GitHub Releases 中下载对应平台的编译包：

### 1. Windows (x64)
在命令行中执行：
```cmd
mdd-card-agent-windows-amd64.exe -gateway 10.44.0.14 -port 35963
```
*如需随开机启动，可创建快捷方式放入 `shell:startup`，或使用 NSSM 注册为 Windows 系统服务。*

### 2. macOS (Apple Silicon M1~M4 / Intel)
```bash
chmod +x mdd-card-agent-darwin-arm64
./mdd-card-agent-darwin-arm64 -gateway 10.44.0.14 -port 35963
```

### 3. Linux (Ubuntu / Debian / CentOS / Alpine / NAS)
```bash
chmod +x mdd-card-agent-linux-amd64
./mdd-card-agent-linux-amd64 -gateway 10.44.0.14 -port 35963
```

---

## 命令行参数一览

| 参数 | 缩写 | 默认值 | 作用说明 |
| :--- | :--- | :--- | :--- |
| `-gateway` | `-g` | `127.0.0.1` | VoWiFi 服务端宿主机 IP 地址或域名 |
| `-port` | `-p` | `35963` | 服务端 VPCD 监听端口（默认 35963） |
| `-reader` | `-r` | `""` (首个可用读卡器) | 读卡器名称关键字过滤（如 `"ESTKme"` 或 `"Gemalto"`） |
| `-retry` | | `3` | 掉线或读卡器异常时的自动重试间隔（秒） |

---

## 源码与二次开发编译

源码目录：[agent/go-agent/](file:///Volumes/micron512g/tmp-project/mdd-gateway/agent/go-agent)

### 本地编译（需 Go 1.21+ 环境）：
```bash
cd agent/go-agent
go build -ldflags="-s -w" -o mdd-card-agent main.go
```

### 全平台一键编译脚本：
```bash
cd agent/go-agent
./build.sh
```
*(脚本会自动通过 Docker 与交叉编译工具链生成 macOS、Linux、Windows 三端二进制产物)*
