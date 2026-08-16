# MDD Card Agent (跨平台智能卡转发代理)

`mdd-card-agent` 是一个轻量级、跨平台的 PC/SC 智能卡转发客户端。

你可以把 USB 读卡器（如普通 CCID 读卡器、ESTKme、SIM 卡槽）插在 **Windows、macOS 或 Linux** 电脑上，运行该代理，即可将智能卡 APDU 鉴权透明转发给后端的 VoWiFi 网关（无论是部署在本地、虚拟机、家庭 NAS 还是云端 VPS）。

---

## 核心特性

1. **零配置跨平台**：
   - **Windows**：原生使用 Windows 系统的 WinSCard 驱动，无需安装额外服务。
   - **macOS**：原生使用系统的 `PCSC.framework`，即插即用。
   - **Linux**：使用标准 `pcscd`。
2. **极低网络流量与低延迟**：
   - VoWiFi 仅在 IKE 握手与 SIP REGISTER 鉴权时调用 SIM 卡（每次几十毫秒），平时无通信，流量消耗几乎为 0。
3. **内置 APDU 安全防火墙 (Safety Guard)**：
   - 客户端底层自动拦截并阻断 `ES10c.DeleteProfile`（物理删除 eSIM）与 `DELETE FILE` 指令，彻底杜绝意外擦除卡内数据。
4. **热插拔与自动重连**：
   - 拔插读卡器或网络波动时自动重连。

---

## 使用方法

### 方式一：Python 版本（无需编译，直接运行）

#### 1. 安装依赖
```bash
pip install pyscard
```

#### 2. 运行代理
```bash
# 连接到指定网关 IP（默认为 127.0.0.1:35963）
python card_agent.py --gateway 192.168.1.100

# 如果有多台读卡器，可指定读卡器名称关键字过滤：
python card_agent.py --gateway 192.168.1.100 --reader "ESTKme"
```

---

### 方式二：Go 独立二进制版本（单文件免环境）

进入 `agent/go-agent/` 目录：

```bash
cd agent/go-agent
go build -o mdd-card-agent main.go

# 运行
./mdd-card-agent -gateway 192.168.1.100 -port 35963
```

在 Windows 上可直接编译为 `mdd-card-agent.exe` 双击或后台运行。

---

## 参数说明

| 参数 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `--gateway` / `-g` | `127.0.0.1` | VoWiFi 服务端 IP 地址或域名 |
| `--port` / `-p` | `35963` | 服务端 VPCD 监听端口 |
| `--reader` / `-r` | 无 (首选读卡器) | 读卡器名称子串匹配 |
| `--retry` | `3.0` | 断线重试间隔时间（秒） |
