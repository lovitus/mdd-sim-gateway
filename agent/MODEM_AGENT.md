# MDD 远程 4G/5G 模块 Agent

`modem_agent.py` 把蜂窝模块的数据、短信和通话能力接入 MDD。它按硬件功能拆分所有权，
不会按 EC20、Quectel 或某个 COM 号写死：

- 同一进程默认持续发现并管理所有外接 PC/SC 智能卡/eSIM 读卡器；读卡器与 Modem 使用独立
  worker 和连接，任一设备拔插或失败不会中断另一类设备。可用 `--pcsc-reader` 过滤，或仅在
  特殊部署中用 `--no-pcsc` 禁用；
- Windows MBN/RMNET 管理数据连接；
- 持久 `AuxiliaryAtProvider` 优先独占另一个空闲 AT/Modem function，提供短信、通话信令和受限
  SIM APDU；只有持久 AT 后端不可用时才回退到独立安装的 Gammu；
- MDD 用模块 IMEI 校验两条控制面确实属于同一台物理设备；
- 每个串口只有一个进程打开。不要同时启动 Gammu SMSD、串口终端或第二个 Agent。

目前 Gammu 以独立 GPLv2 可执行程序运行，MDD 不链接或复制 libGammu。这样不会把
MDD Agent 与 Gammu 的许可证和生命周期耦合在一起。发布包以后可以把官方 Gammu
可执行程序作为独立组件放在 Agent 旁边，但发布者还必须一并满足 Gammu 的许可证、
源代码和声明要求；当前版本由用户安装。

## Windows（当前主路径）

1. 从 [Gammu 官方安装文档](https://docs.gammu.org/project/install.html)所指向的 Windows
   构建安装 Gammu。无需 WSL。
2. 任选一种发现方式：
   - 把 `gammu.exe` 放在 `mdd-modem-agent.exe` 同一目录；
   - 把 Gammu 安装目录加入 `PATH`；
   - 启动 Agent 时传入 `--gammu "C:\\Program Files\\Gammu\\bin\\gammu.exe"`。
3. 通常不要指定串口。Agent 会排除自己占用的数据/AT、DM、NMEA、GPS 和蓝牙端口，
   再逐一读取 IMEI，只采用与 MBN 数据设备 IMEI 一致的空闲端口。
4. 如果操作系统描述不完整，可以显式指定，例如 `--gammu-port COM16`。这个端口必须
   与 `--port` 不同。

示例：

```powershell
.\mdd-modem-agent.exe `
  --server 10.44.0.14:8443 `
  --token '<AGENT_TOKEN>' `
  --port COM14 `
  --gammu-port COM16
```

Agent 会为每次调用生成临时 Gammu 配置并在结束后删除；用户不需要维护
`gammurc`。Token、密码和网关凭据不要写入项目或打包进可执行文件。

正式发布的单文件 Agent 必须把 `pyscard` 一并打包；Windows 使用系统 `WinSCard.dll`，macOS
使用 `PCSC.framework`，Linux 使用 `libpcsclite`。用户无需再启动第二个 Card Agent。若运行环境
没有 PC/SC Provider，Modem 功能仍会继续运行并明确记录诊断，而不会假装已经管理读卡器。

维护者可在 `agent` 目录使用固定依赖构建单文件包：

```powershell
python -m pip install -r requirements-modem-build.txt
pyinstaller --noconfirm --clean mdd-modem-agent.spec
```

构建日志必须出现 `smartcard/scard/_scard`；发布前还要在目标系统以“启动时无读卡器、运行后插入”
和“同时连接 Modem + PC/SC 读卡器”两种方式验收，避免生成一个只能管理 Modem 的残缺包。

## macOS 与 Linux

macOS 可通过 Homebrew 安装：

```bash
brew install gammu
```

Debian/Ubuntu 可通过系统包安装：

```bash
sudo apt install gammu
```

之后正常启动 Agent 即可自动从 `PATH` 发现 `gammu`。macOS/Linux 的数据 Provider
仍需各自完成系统级移动宽带接入；安装 Gammu 本身只提供通用 AT 短信/通话信令，
不会绕过驱动或系统网络配置。

## 状态与故障定位

设备状态的 Provider 为 `composite`，并且 `sms_ready=true`，表示数据与独立信令 function
已经按 IMEI 组合成功。Windows MBN 的静态 SMS capability 不代表运行时可用；MBN 明确报告未就绪
时，组合 Provider 会使用已验证并独占的 AT 后端。若只有 `windows_mbn`：

1. 运行 `gammu --version` 检查安装；
2. 检查是否有另一个程序占用 Modem/AT 串口；
3. 必要时用 `--gammu-port` 指定独立端口；
4. 查看 Agent 日志中的 `Gammu signalling attached on ...` 或发现失败信息。

短信发送和拨号属于可能计费操作。若 Gammu 超时，Agent 返回 `status=unknown` 且
`retryable=false`；服务端和 UI 不得自动重试，必须先人工核对实际结果。

## 固件兼容性与升级

固件升级不是普通的 Agent 更新。模块完整固件号中的产品分支和基线都必须匹配；例如
`EC20CEHDLGR06A07M1G` 中的 `HDLG` 与 `R06` 都是兼容性边界，不能只按页面上的
“EC20F”选择固件。

### 自动检测与页面提示

Agent 可以自动读取并上报以下只读信息：

```text
ATI
AT+QGMR
AT+QMBNCFG="AutoSel"
AT+QMBNCFG="List"
AT+QCFG="ims"
AT+QCFG="usbcfg"
AT+CREG?
AT+CGREG?
AT+CEREG?
```

服务端只能根据经过维护者签名的兼容矩阵给出三类结果：`supported`、`update_available`
或 `hardware_replacement_required`。未知版本只能提示“需要厂商确认”，不得猜测兼容，
也不得把固件包 URL、管理员凭据或设备 QCN 写进项目。

允许自动检查版本；允许在用户明确确认后下载并校验同产品分支、同基线的升级包；禁止
无人值守静默刷写。出现以下任一情况必须拒绝自动升级：

- 产品分支、硬件 SKU、存储布局或基线不同；
- 厂商说明需要工厂包、重新校准、USB_BOOT/9008 恢复或现场 FAE；
- 缺少官方发布说明、SHA-256、匹配的 QFlash 版本或原版本回退包；
- 无法备份本机唯一的 QCN/EFS/RF 校准数据；
- 供电、USB 连接不稳定，或仍有线路/代理依赖该模块。

已知 EC20-CE R06 与 R08 的 CEFS 不同。移远说明 R06 不能通过普通在线升级迁移为 R08，
需要工厂包并重新校准；这种情况必须显示“更换 R08 硬件或联系授权服务”，不得提供自动
刷写按钮。QCN/EFS 含每台模块唯一的射频校准、IMEI/SN 等数据，不能使用另一台模块的
备份恢复。

### Windows QFlash 受控升级步骤

只有兼容矩阵明确允许的同分支、同基线版本才能按以下流程执行：

1. 从移远或授权经销商取得与完整 `AT+QGMR`、标签 SKU 一致的固件、发布说明、校验值、
   QFlash 版本和原版本回退包；先离线验证 SHA-256。
2. 使用 QPST/厂商工具备份本机 QCN/EFS，并将 `ATI`、IMEI、ICCID、MBN 列表、IMS、APN、
   USB function、radio/roaming/data 意图和依赖该出口的线路记录到项目外的受保护目录。
3. 告知用户升级期间蜂窝数据、短信、通话和出口代理都会中断；停止 Agent、Gammu SMSD、
   串口终端和所有依赖进程，确认 AT/Modem/DM 端口没有被占用。
4. 使用稳定外部供电和直连 USB；在 Windows 以管理员身份运行官方 QFlash。工具和固件路径
   不得包含空格。ECxx 选择 `Quectel USB DM Port`，加载完整解压后的匹配固件包。
5. 点击 Start 后不得退出 QFlash、拔除 USB、休眠、重启或断电；等待工具明确显示 `PASS`。
6. 模块重新枚举后先只读核对 `AT+QGMR`、IMEI、SIM、QCN/RF、MBN、IMS 和注册状态，再恢复
   APN、数据、WFP 隔离和反向代理。最后人工各做一次数据出口及授权短信验收，禁止自动重试。
7. 任一步骤出现版本不符、FTM/CFUN 5、无信号、IMEI/SN 丢失或端口不再枚举，应立即停止业务
   验收并进入厂商恢复流程；只能使用该模块自己的 QCN 和精确原固件回退。

参考：[Quectel QFlash User Guide](https://forums.quectel.com/uploads/short-url/ApQc64SUOmNIU7ibGcQNPB5prxD.pdf)。

EC20-CE HDLG 的 Windows 逐台盘点、QCN/EFS 备份、QFlash 操作、升级后核验和批量台账见
[`windows/ec20-upgrade/README.md`](windows/ec20-upgrade/README.md)。仓库只维护脚本和流程，
不提交固件、工具、QCN、IMEI 或现场升级台账。
