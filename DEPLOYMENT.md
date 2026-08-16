# MDD VoWiFi Gateway 安装部署与使用全指南

本指南包含 **服务端（云端/本地 Linux）部署**、**跨平台读卡器客户端（Windows/macOS/Linux/Android）接入** 以及 **VoWiFi / 短信 / eSIM / 代理分流 / 自定义 IMEI** 的完整配置说明。

---

## 一、架构概览

```
+-------------------------------------------------------------------+
|               用户端 (Windows / macOS / Linux / Android)          |
|  1. USB 读卡器 + mdd-card-agent (极简 Python/Go APDU 鉴权转发)     |
|  2. 浏览器打开 WebUI (WebRTC 软电话接打电话 + 实时收发短信)        |
+-------------------------------------------------------------------+
                                  |
                   TCP / VPCD 端口 (默认 35963)
                                  |
+-------------------------------------------------------------------+
|                   服务端 (Linux VPS / 本地服务器)                  |
|  - control/: FastAPI Web 控制面 + 安全 SQLite 账本 + LPA 引擎      |
|  - engine/:  标准 Linux IPsec/IKEv2 (strongSwan) + Asterisk IMS 引擎 |
+-------------------------------------------------------------------+
```

---

## 二、服务端安装部署 (Linux 服务器 / VPS)

### 环境要求
- **操作系统**：Ubuntu 22.04+ / Debian 12+ / Rocky Linux / Arch Linux
- **权限**：Root 权限（VoWiFi 底层需创建 IPsec xfrm 隧道接口）
- **网络**：允许开放 WebUI 端口（默认 `8443`）及 VPCD 转发端口（默认 `35963`）

---

### 方式 A：官方脚本一键安装 (推荐)

一键脚本会自动锁定 `pcsc-lite` 驱动版本、编译 WebUI 并配置 systemd 后台常驻服务：

```bash
# 1. 克隆代码仓库
git clone https://github.com/MddIdd/mdd-sim-gateway.git /opt/mdd-gateway
cd /opt/mdd-gateway

# 2. 执行安装（默认本地原生控制面 + Docker VoWiFi 引擎模式）
sudo ./install.sh install

# 3. 检查运行状态
sudo ./install.sh status
```

---

### 方式 B：Docker Compose 一键部署

适合习惯使用 Docker 容器化编排的环境：

```bash
cd /opt/mdd-gateway
docker compose up -d --build
```

---

## 三、跨平台读卡器客户端配置 (Card Agent)

读卡器插在客户端电脑上，运行 Thin Agent 即可将 APDU 鉴权转发到服务端。

### 1. Windows 系统
1. 安装 Python 3 (勾选 Add to PATH)
2. 进入 `agent/` 目录，双击运行 **`run-windows.bat`**
3. 按照提示输入您的服务器 IP 地址（如 `1.2.3.4`）即可建立桥接。

### 2. macOS 系统
1. 系统自带 PC/SC 框架，无需额外驱动。
2. 安装依赖：`pip3 install pyscard`
3. 双击运行 `agent/` 目录下的 **`run-macos.command`**（或终端执行 `python3 card_agent.py --gateway 你的服务器IP`）。

### 3. Linux / 树莓派 (配置后台自启)
```bash
# 1. 安装驱动
sudo apt update && sudo apt install -y pcscd libpcsclite-dev python3-pip
pip3 install pyscard

# 2. 配置 systemd 服务
sudo cp agent/mdd-card-agent.service /etc/systemd/system/
sudo sed -i 's/127.0.0.1/你的服务器IP/g' /etc/systemd/system/mdd-card-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now mdd-card-agent
```

### 4. Android 手机接入
- **OTG 外接读卡器**：利用 Type-C OTG 线将 CCID 读卡器插入安卓手机，基于 Android USB Host API 进行 TCP 转发。
- **手机内置 SIM/eSIM**：具备权限（或 Root）的安卓设备，可通过 Android 标准 OMAPI (`TelephonyManager.iccTransmitApduLogicalChannel`) 将手机内置卡槽的鉴权能力直接共享给网关。

---

## 四、功能配置与使用

### 1. 登录 Web 控制台
浏览器访问 `https://<你的服务器IP>:8443`。
首次登录请按照屏幕提示设置管理员密码。

### 2. 绑定与自定义 IMEI (规避机型白名单限制)
有些运营商限制只有特定手机型号才能激活 VoWiFi：
1. 打开 WebUI -> 进入 **“设备 (Devices)”** 页面。
2. 切换至 **“硬件 (Hardware)”** 标签页。
3. 在 **“硬件 IMEI (Hardware IMEI)”** 输入框中填入合法的 15 位目标手机 IMEI，点击 **“保存”**。
4. 网关在向基站发起 IKEv2 / IMS 注册时，将自动以该自定义 IMEI 作为终端身份。

### 3. 绑定出海代理与国家自动分流
如果国内网络直接连接国外运营商 ePDG 遇到阻断或丢包：
1. 进入 WebUI -> **“系统设置”** -> **“国家出口 / 代理订阅”**。
2. 填入 **Clash 订阅地址** 或 **SOCKS5 / HTTP 代理**。
3. 系统会自动测试节点 **UDP ASSOCIATE** 连通性，并根据每张 SIM 卡的属地国家（如英国、香港、美国）将 VoWiFi 流量无缝走对应代理出口。

### 4. 浏览器 WebRTC 软电话与短信
- **短信**：点击 **“信息 (Messages)”** 页面，可实时查看收到的验证码、发送短信。
- **高清通话**：点击 **“通话 (Calls)”** 页面，利用浏览器内置的 WebRTC 软电话即可使用麦克风和扬声器接打电话。

### 5. 安全 eSIM 管理与故障恢复
- **彻底禁用物理删除**：杜绝误操作导致实体卡内 Profile 丢失。
- **卡片软删除与回收站**：SIM 配置页点击“移入回收站”，即可随时隐藏已停用卡片；进入“回收站”标签页可一键无损“恢复”（所有短信通话历史均保留）。
- **极端网络重放删除通知**：在换机解绑失败或极端网络丢包时，进入 eSIM 通知列表点击“重放”，弹窗二次安全确认后即可向运营商同步注销状态。

---

## 五、防火墙与安全建议

在云服务器安全组中，建议按需开放以下端口：

| 端口 | 协议 | 用途 | 建议策略 |
| :--- | :--- | :--- | :--- |
| `8443` | TCP | WebUI 控制台 & WebRTC 信令 | 仅允许自己的 IP 或绑定 HTTPS 域名 |
| `35963` | TCP | 智能卡 VPCD APDU 转发 | 建议配合安全组仅允许读卡器客户端 IP |
| `500` / `4500` | UDP | IPsec IKEv2 流量 (如需直连 ePDG) | 放行出站/入站 |
| `10000-20000` | UDP | WebRTC / RTP 语音媒体流 | 放行入站 |
