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

### 统一服务、SSH CLI 与 GUI

正式 Windows 部署只允许 SCM 服务 `MddAgent` 打开 Modem、串口和 PC/SC 读卡器。
`mdd-agent.exe` 的其他命令和 `mdd-agent-gui.exe` 都是本地控制客户端，不会启动第二个
设备运行时；关闭 GUI 或 SSH 会话不会停止 Agent。旧 `MDDModemAgent` 计划任务和
`run-windows.bat` 不再是正式运行入口。

初次安装是唯一需要管理员权限的正常步骤。在非提权 SSH 会话中执行安装会立即返回退出码
`10` 和 `elevation_required`，不会弹出一个 SSH 用户看不到的 UAC 对话框。先在管理员
PowerShell 中执行（Token 从 stdin 读取，不进入命令行或项目文件）：

```powershell
$token = Read-Host -AsSecureString "Agent token"
$plain = [System.Net.NetworkCredential]::new('', $token).Password
$plain | .\mdd-agent.exe service install --server gateway.example.com:8443 --token-stdin --json
Remove-Variable plain, token
```

安装器会创建 `%ProgramData%\MDD\Agent`、`MDD Agent Operators` 本地组和自动恢复服务。
它先启动并检查新服务，成功后才禁用旧计划任务；失败会恢复旧任务。它不会强杀未知的旧
Agent 进程。安装时的账户会加入 Operators 组，新登录的 SSH/桌面会话可日常启停、查看状态
而不再触发 UAC。为避免 SYSTEM 服务执行被低权限用户替换的 helper，任何配置修改（包括
helper 路径和 Token）与卸载都要求已提权管理员身份；只读状态、设备、自检、日志以及服务
启停由 Operators 完成。

升级或重启时不能只依赖 SCM 的 `STOPPED`：单文件进程可能仍在完成退出。CLI 与安装器会在
有限期限内等待安装级运行租约真正释放，再启动替代进程；回滚也使用同一规则。超时会关闭
失败并保留可恢复状态，不以固定 sleep 猜测，也不强杀仍持有设备的进程。

从旧 `MDDModemAgent` 用户计划任务首次迁移时，安装器会从该任务账户的用户配置中导入经过
格式校验的安装 `agent_id` 到 ProgramData。只有当前提权安装账户与已启用旧任务的账户 SID
一致时才允许自动迁移；其他账户需要显式处理。已有 ProgramData 身份只校验并保留，旧身份
格式异常时安装关闭失败，迁移写入也纳入安装回滚。这样切换到 LocalSystem 不会因用户 profile
改变而把同一台主机误报成新 Agent；普通升级、重装及已禁用的遗留任务不会覆盖现有安装身份。

不支持原子维护门禁的旧版只能在操作者已从服务端和本机确认没有活动、未知或正在清理的
付费通话后，执行一次受监督迁移：在上述 `service install` 命令增加
`--supervised-legacy-idle-migration`。该选项只为首次旧版本迁移提供显式授权；安装器仍会
检查旧任务账户的付费通话标记和音频 helper，发现任一证据就拒绝停止旧 Agent。现代 Agent
返回冲突或正在关闭时不会降级到此路径。后续升级由原子维护门禁自动保护，不再使用该选项。

SSH/命令行管理：

```powershell
$agent = Join-Path $env:ProgramFiles 'MDD\Agent\mdd-agent.exe'
& $agent status --json
& $agent devices --json
& $agent doctor --json
& $agent logs --lines 300 --json
& $agent config show --json
& $agent config validate server gateway.example.com:8443 --json
& $agent config set server gateway.example.com:8443 --json
& $agent reconnect --json
& $agent self-test --json
& $agent service status --json  # status 也可替换为 start、stop 或 restart
```

从任意 SSH 工作目录调用已安装版本时，应使用上述完整规范路径，不要把
`C:\Program Files` 改写成 `C:\Progra~1`。单文件发布包会校验父子进程来自同一个规范化
可执行文件路径；8.3 短路径与长路径指向同一文件，但会被安全校验视为路径不一致。

CLI 的 stdout 只输出结果，诊断写入 stderr；JSON 模式输出单个版本化对象。主要退出码：
`0` 成功、`3` 配置错误、`4` 未安装、`5` 服务不可用、`6` 权限拒绝、`7` 动作失败、
`8` 自检不健康、`9` 设备运行时冲突、`10` 需要管理员授权。GUI 使用完全相同的本地
named-pipe client 与动作，不直接访问硬件或维护第二份状态。多个 SSH/GUI 客户端同时连接时，
客户端会在原请求截止时间内处理管道实例轮换，不把瞬时 `PIPE_BUSY` 暴露成设备故障。

Windows GUI 使用与 Web 后台一致的 MDD 图标并常驻系统通知区域。点击窗口关闭按钮默认只隐藏
窗口；单击托盘图标重新打开，右键可打开、重启服务或“退出 GUI（服务继续运行）”。Explorer
重启后托盘图标会自动恢复。GUI 退出、崩溃或用户注销均不停止 SCM 服务，也不释放或重新接管
Modem/PC/SC 设备。

配置位于 `%ProgramData%\MDD\Agent\config.json`；Token 单独使用 machine-scope DPAPI
保护，`config show` 永远只返回 `<redacted>`。日志在 `logs\agent.log` 有界轮转。本地管道拒绝
远程客户端、限制消息为 1 MiB，并在 impersonation 后按 Administrators / Operators / 只读
用户分权。浏览器麦克风和扬声器仍由登录用户的 WebRTC 会话拥有；SCM Session 0 只管理
Modem 同一 PnP container 的 UAC 音频端点，不枚举默认用户声卡。

Windows Event Log 仅作为可选诊断镜像，不是服务启动依赖。裁剪版或受管系统即使禁用了
`EventLog`，Agent 仍必须启动并写入上述有界文件日志；安装器不会为了 Agent 擅自改变系统的
Event Log 启动策略。

发布包必须先由 `windows/Build-Windows-Package.ps1` 组装并生成 SHA-256 manifest；脚本只接收
已有的三个 helper release 二进制，不会重复本地编译它们。安装器逐项校验 manifest 后，把
完整包复制到不可由 Users/Operators 写入的 `%ProgramFiles%\MDD\Agent`，服务绝不直接执行
下载目录或用户目录中的 EXE。发布包包含两个入口：console/SSH 用 `mdd-agent.exe`，无控制台窗口的桌面 GUI 用
`mdd-agent-gui.exe`。`mdd-network-guard.exe`、`mdd-windows-mbn.exe`、
`mdd-call-audio-helper.exe` 与可选 `gammu.exe` 仍是边界清晰的相邻 helper，不应重新链接或
偷偷内嵌不同许可证的实现。`MODEM_AGENT.md` 也是受 manifest 校验并随安装复制的发布组件，
保证目标机上的操作说明与已安装二进制来自同一版本。

### Windows 语音能力与 UAC 自检

语音信令、SIM APDU、短信和数据是相互独立的能力。Windows WWAN 占用 SIM 通道并导致
`AT+CPIN?` / `AT+CUAD` 失败时，只要同一 IMEI 的辅助串口实际通过 `AT+CLCC`，Agent 仍会注册
语音信令；不能因为 APDU 不可用而隐藏通话。每项能力都由当前插入设备的非破坏性命令单独探测，
不按 EC20 型号名称硬编码。

对于声明支持 `AT+QPCMV` UAC 模式的模块，Agent 启动时读取 `AT+QCFG="USBCFG"`。只有响应严格
符合 Quectel 文档中的 VID、PID 加七个二进制功能位格式时，才允许自动准备 USB 音频：保留
VID/PID 和前六位原值，仅把第七位 UAC 从 `0` 改为 `1`，读回完全一致后执行一次
`AT+CFUN=1,1`。重枚举后该检查变为只读，不会重复重启。未知命令、未知字段数量、非二进制值、
写入失败或读回不一致都会保持 fail-closed，不修改模块。

现场排障应先保存完整查询结果作为该设备的回滚值：

```text
AT+QCFG="USBCFG"
```

若需回滚，只能使用该设备先前保存的完整 VID/PID 与七个功能位，不能复制另一设备的值。
已验证的 EC20 运行时在呼叫媒体开始/结束分别使用 `AT+QPCMV=1,2` / `AT+QPCMV=0`；这些运行时
命令不持久化，也不改变 Windows 默认声卡。Agent 只有在同一 PnP container 下找到状态正常的
播放与录音端点，并由音频 helper 实测通过 8 kHz 单声道全双工后，才上报 `call_audio=true`。

### Modem Provider

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

旧版前台调试示例（不得与正式 `MddAgent` 服务同时运行）：

```powershell
.\mdd-modem-agent.exe `
  --server gateway.example.com:8443 `
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
PC/SC supervisor 以连续两个成功发现周期确认读卡器确实消失，再停止该 reader 的独立 worker；
这样既不会因 Windows 单次枚举空值制造抖动，也能在同名读卡器重插后用稳定 identity 重建桥接。
仅拔出卡片但保留读卡器时必须清空旧 ATR；后续 ATR 或 APDU 请求会重新连接新插入的卡，不能
继续返回旧卡身份或要求重启整个 Agent。

## macOS 与 Linux

macOS 的统一发布包内置 `mdd-cellular-io` 与 `mdd-call-audio-helper`，不要求 Homebrew、系统
`pppd`、Python 或 Gammu。菜单栏 GUI 和 `mdd-agent run` 是同一 `AgentHost` 的两种入口，不能
同时拥有硬件；重复启动退出码为 `9`。首版没有 launchd，登录/重启后需要用户启动 GUI，或由
用户自己的 SSH/任务系统以 `printf '%s\n' "$MDD_AGENT_TOKEN" | nohup mdd-agent run
--token-stdin` 启动。持久 Token 与其他设置统一保存在当前用户的 owner-only `config.json`；GUI
只读取该配置，不访问 Keychain，也不使用 FIFO/argv 传递 Token。CLI 在配置未设置 Token 时额外
支持 `--token`、`--token-stdin` 和 `MDD_AGENT_TOKEN`，优先级为配置文件、命令行/标准输入、环境
变量；临时回退不写回配置。需要让 CLI 与 GUI 长期共用 Token 时，使用
`printf '%s\\n' "$MDD_AGENT_TOKEN" | mdd-agent config set token --stdin` 写入同一配置文件。
GUI 与本地 CLI 每次启动都检查麦克风 TCC，未决定时请求系统授权；授权结果只重检音频，不重启
读卡器、数据线路或整个 Agent。纯 SSH 且没有已登录桌面会话时，macOS 可能无法显示 TCC 对话框；
CLI 会明确报告该限制，其他功能继续运行，不能把未授权状态误报成语音可用。

macOS 发布包必须通过 `agent/macos/Build-MacOS-Package.sh` 做最终组装。脚本把 CLI、GUI app、
`mdd-cellular-io`、`mdd-call-audio-helper`、文档和版本文件放入同一个 package root，只在 package
root 生成 `manifest.json`。GUI 从 `MDD Agent.app/Contents/MacOS` 向上找到同一个 package root
manifest；不得把 manifest 写入已签名 `.app/Contents/Resources`，否则会破坏签名顺序。

真实 release 构建应使用 `agent/macos/Build-MacOS-Release.sh --release`：它在外部 build root
中构建/校验锁定的 libusb、lwIP、`mdd-cellular-io`、`mdd-call-audio-helper` 和 PyInstaller
CLI/GUI 产物；必须提供已校验的离线 wheelhouse manifest 及其 SHA-256，并在生成 root manifest
前对 CLI、GUI app 和两个 helper 完成 codesign/verify。只有这种 release 路径会生成
`control-agent-allowlist.env`；`install.sh` 在部署时会把其中的
`MDD_ALLOWED_AGENT_PACKAGE_DIGESTS` 传给 Control。

没有签名或没有 wheelhouse 信任锚时，只能使用 `--development`/`--unsigned-development` 生成开发
自检包。开发包仍有 root `manifest.json` 供 Agent 运行时自检，但不会生成
`control-agent-allowlist.env`，不能用于 Control 蜂窝语音放行。如果没有 manifest、digest 不匹配、
包内存在未列 payload、嵌套 metadata 或符号链接，蜂窝语音能力会 fail-closed。

Mac 数据 Provider 通过原始 USB + 私有 lwIP PPP 暴露仅供 Agent 使用的 dial API，不创建系统
network interface、route 或 DNS，也不回退 Wi-Fi/Ethernet。短信、通话、SIM APDU 与多 PC/SC
读卡器继续复用本文件前述领域逻辑；物理 USIM 对 eUICC 探测 APDU 的合法拒绝不会撤销整个 UICC。
若 raw-USB Modem 无法安全并发 SIM logical channel 与 PPP，数据启用前只暂停该 Modem 自身的
VPCD APDU，数据关闭后自动恢复；外接 PC/SC/eSIM 读卡器仍独立运行。
当前实机门禁是 macOS 15.2 arm64 + EC20F + 两个同型号 PC/SC reader；其他架构/协议只有完成
发布矩阵后才能宣称支持。

Debian/Ubuntu 可通过系统包安装：

```bash
sudo apt install gammu
```

Linux 正常启动 Agent 后可从 `PATH` 发现 `gammu`。Linux 数据 Provider 仍需完成独立的系统级
移动宽带与隔离验收；安装 Gammu 本身只提供通用 AT 短信/通话信令，不会绕过驱动或网络配置。

### Agent 主机健康上报

Windows 服务与 macOS CLI/GUI 共用一个独立的主机级健康通道，稳定身份只是安装时
持久化的 `agent_id`，不使用 ICCID、EID、Modem、读卡器或 VPCD 槽位作为主机身份。
语义状态改变时发送完整快照；没有变化时每 10 秒只发送心跳。快照只读取 Agent 已有的
运行时缓存与数据目录磁盘摘要，不执行 AT、PC/SC、音频探测、重连或任何修复操作。
通道断开不会影响设备、数据、短信、通话租约或安装租约。有付费通话处于清理隔离时，
健康线程仍继续心跳，直到权威终止证据允许运行时真正退出。

管理页在“诊断 → Agent 主机”显示运行方式、版本、心跳时间、缓存的模块数量与隔离/
存储状态。Agent 在线不代表 SIM、4G、短信或通话一定可用；这些仍由各自业务能力状态表示。
服务端纯心跳只更新内存中的 `seen_at`，不写盘、不广播，因此不会让设备页重排或丢失选中态。
Linux 当前只报告“Agent 在线，主机健康采集尚未实现”，不将其误报为故障。

健康通道配置项为 `health_path`，默认 `/mdd/api/agent/health/ws`；通常不需要修改。它与
`control_path`、VPCD 路径相互独立，但使用同一 `server`、Token 与 TLS 指纹。Token 只在完成
TLS 指纹校验后通过 `Authorization` 请求头发送，绝不出现在 URL。服务端以 10 秒为正常节拍，
25 秒未见帧显示“心跳延迟”，40 秒未见帧显示离线并关闭旧会话以强制重新注册。若“Agent 主机”
显示离线而设备仍暂时在线，应依次检查 Agent 日志中的 health WSS 错误、系统时间/TLS 指纹及
到网关 8443 的连接；不要用重启 Modem、读卡器或通话线路来修复健康通道。

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

### UICC 健康自检与有限恢复

UICC 健康独立于数据、短信和语音 Provider。Agent 每 60 秒至多检查一次标准注册和 CPIN 状态；
仅当此前已取得同一 Modem 的 IMEI 与 ICCID、`AT+CPIN?` 明确返回 `SIM failure` / `SIM not
inserted`，并且 CS/EPS 均未注册或插卡状态同时证明卡已掉线时，才执行一次标准
`AT+CFUN=0` → `AT+CFUN=1` 重新初始化。这样既不会被切换瞬间的过期注册缓存骗过，也不会把
Windows MBIM 正常占有 CPIN 的已注册模块误判为故障。`AT+QSIMSTAT?` 仅作为支持它的模块上的
佐证，不是厂商或型号选择条件。

恢复动作在改变 CFUN 前写入项目外状态目录。若进程中断且 Modem 留在非完整功能模式，下次
启动只补做 `CFUN=1`；若完整周期后仍失败，则锁存故障，不循环重启。后续检测到 `CPIN READY`
或任一注册域恢复后自动清除锁存，因此真正的拔插、重新接触或下一次独立故障仍可再次恢复。
SIM PIN/PUK、未知状态、从未识别过的空卡槽均不会自动重置。恢复未完成时短信提交和拨号失败
关闭，且不会继续执行 IMS/MBN 修复；外部 PC/SC/eSIM 读卡器不经过这一 Modem UICC 状态机。
恢复后的 CPIN 等待窗口为 45 秒，覆盖真实 Windows USB/MBN 重枚举延迟；它只发生在已确认故障
后的单次恢复，不增加正常心跳频率，也不会自动触发任何计费操作。

Windows 启动阶段还有一个更窄的引导恢复：已由 AT 读出 IMEI、但 MBN 尚未枚举时，若直接读到
`CFUN=0`，Agent 只补做一次 `CFUN=1` 来完成被中断的转换；不会覆盖 `CFUN=4`/飞行模式，也
不会启动新的 0/1 周期。若所有 AT function 已经完全无响应，PnP 软重启不等同于硬件断电，
页面/API 必须提示冷断电/重新插拔一次；重新枚举后 Agent 会自动恢复发现和期望状态。

每次 Provider 成功返回 IMEI+ICCID 后，Agent 会把这一硬件到 SIM 的最后成功关联保存在本机状态
目录，供下一次 MBN 暂时丢卡时授权上述有限恢复；该缓存不作为在线 SIM 上报值。由旧版升级且
本机还没有此状态时，只在 Windows SubscriptionManager 中恰好存在一个合法 ICCID 的情况下把它
作为一次迁移证据；存在多个历史 SIM 时拒绝猜测，仍需重新插拔/恢复后由 Provider 建立关联。

### 语音注册自检与有限恢复

Agent 只在实际提供 `call_signalling` 的 Provider 上检查语音注册；Windows MBN 数据 function
和辅助 AT 信令 function 不得互相代替判断。状态通过 `voice_registration` 上报，只有
`ready=true` 才注册为可拨号，未知、恢复中和失败都必须在发出 `ATD` 前关闭失败。
数据漫游开关只约束蜂窝数据连接和出口代理，不参与 `call_ready` 判断；即使当前网络被标记
为 roaming 且禁止数据漫游，只要 CS 或 IMS 语音承载及音频预检就绪，拨号和来电仍可使用。

自检每 60 秒至多执行一次，只做非计费查询，并按以下顺序进行幂等恢复：

1. `CREG/CEREG` 已注册，或 EPS 已注册且模块确认 `VoLTE_cap=1`：标记可用，不写配置；
2. `COPS=2` 明确处于手动注销：只恢复标准自动选网 `COPS=0`，等待 120 秒；
3. LTE limited service、IMS 为 `0/0`，且当前 MBN 已选中、已激活并明确包含 VoLTE：启用
   文档化 IMS 功能并软重启一次；
4. IMS 已为 `1/0`、`AutoSel=1`、同一个 VoLTE MBN 仍处于已选中和已激活状态：反激活该
   MBN 并软重启，让自动选择在 IMS 开启后重新应用。该 NVM 动作以 IMEI、ICCID、固件和
   Profile 生成指纹持久化到 Agent 状态目录，同一条件只尝试一次，服务重启也不会循环写入；
5. 上一步超过完整冷却周期仍无承载时，允许一次不再写 NVM 的纯软重启，并单独持久化指纹；
6. 仍无语音承载：保持 `pending`，上报完整诊断，不拨号、不盲选其他 MBN、不自动发短信。

`QCFG="ims",2` 是明确的人工禁用，Agent 永不覆盖。自动维护也不修改 `AutoSel`，不按产品名
或 COM 号匹配设备，不选择 ROW/其他运营商 Profile。插拔和服务重启后使用相同状态机重新
发现能力；任何重启动作都会先撤销可拨号状态，重新枚举和自检完成后才恢复。

Windows MBN 拥有 Modem 内部 UICC 时，辅助 AT 信令 Provider 不执行 `CUAD/CSIM/CGLA`
探测，也不注册该 Modem 为 VPCD 读卡器，避免 SIM 会话冲突破坏注册、短信或通话。外接 USB
读卡器和 eSIM 读卡器仍由同一 Agent 的 PC/SC supervisor 自动发现、热插拔和注册，两条能力
链彼此独立。

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
