# 归档：调研与选型记录（2026-08-18）

> 从 `TODO.md` 拆分归档，2026-08-27。这是一次性调研/选型过程记录，结论已经沉淀进
> `TODO.md` 的架构章节（`1. 最小架构` 起）。此处只保留历史依据，不代表当前待办，
> 不得据此重新实现或重复调研。

## 工程规则：先研究，后实现

- [ ] **硬验收优先级**：远程蜂窝数据出口和短信收发必须实现；语音可以延期，但 Provider、设备
      所有权和事务恢复模型必须预留拨号、来电、挂断及音频 transport，不能为短信做一次性架构。
- [ ] **Windows 本机先行门槛**：任何 Windows 能力必须先在完全排除 Web、服务端和 Agent RPC 的
      本机脚本/成熟工具中走通真实硬件，再实现最薄桥接。未在本机成功的能力不得靠继续修改页面、
      API 或状态映射来“修复”，也不得标为完成。

- [ ] 每项新增 Modem 能力在编码前必须留下简短设计记录，至少包含：参考实现或官方协议链接、
      准备复用的状态机/接口语义、平台差异、许可证和不直接复用的原因。不得只凭单台设备的
      日志为 EC20、某个 COM 口或某个操作系统版本添加业务层特判。
- [ ] 优先借鉴成熟项目的“能力接口 + 平台/协议 Provider + 厂商小型覆盖层”结构：
      [ModemManager](https://github.com/linux-mobile-broadband/ModemManager) 作为能力与插件模型参考；
      Windows 使用官方 [Mobile Broadband Win32 API](https://learn.microsoft.com/en-us/windows/win32/mbn/mbn-start-page)
      和 [Windows SMS API](https://learn.microsoft.com/en-us/windows-hardware/drivers/mobilebroadband/developing-sms-apps)；
      Linux 优先复用 ModemManager/libmbim/libqmi；Quectel 等厂商文档只用于设备初始化、音频和
      固件差异，不替代通用 Provider。
- [ ] GitHub 项目只用于借鉴接口、状态机和已知兼容性；采用代码前必须核对许可证。不得把 GPL
      实现复制进不兼容模块，也不得因为某个开源项目支持某设备就假设本机驱动暴露了相同能力。
- [ ] 原生 API 可用时，不把本地化 CLI 文本解析作为权威状态源。CLI 只能用于诊断或无原生绑定时
      的受控兼容层，并必须使用结构化返回码、超时和可回放测试覆盖。
- [ ] Provider 只依据系统枚举出的协议与能力选择，不依据产品名：Windows MBIM/MBN、Linux
      ModemManager（内部可为 MBIM/QMI/AT）、macOS 系统能力或受控厂商适配、最后才是通用 AT。
      通用 AT Provider 只能独占选中的 AT function；操作系统网络管理器占用另一个网络 function
      并不自动冲突，但必须通过同设备并发验收和统一协调者管理。
- [ ] 每个物理 Modem 只有一个协调者，但 USB 组合设备按 function 分配唯一 Owner：同一个网络
      function、AT/Modem COM 口或 NMEA/UAC 音频 function 绝不能被两个进程并发打开；不同 function
      可以在通过实机并发验收后分别交给系统数据栈、独立 Modem 服务和媒体桥。`system_managed`
      不再等同于“禁止所有 AT”；禁止的是同一 function 争用和没有协调/恢复规则的隐式半接管。
- [ ] 协调者必须声明设备的并发能力。支持并发的设备直接并行数据与短信/语音；不能并发的设备使用
      可回滚的独占事务：快照数据 Profile、代理与隔离 → 暂停数据 → 执行一次短信或通话会话 →
      恢复原数据、隔离和代理。任何失败或进程退出都必须恢复；付费操作绝不自动重放。该策略位于
      本机 Modem 服务，不得泄漏为服务端的 EC20/Windows 特判。
- [ ] 优先采用 `system_managed` 保持通用安装和驱动兼容；允许设备和平台验证通过后选择
      `agent_managed`，以获得统一的数据、信令和流量隔离。直管模式必须具备权限预检、占用检测、
      原配置快照、失败回滚和卸载恢复，不能通过永久改驱动绑定或全局停用系统蜂窝服务实现。
- [ ] “unsupported” 只能来自已选 Provider 明确报告的能力缺失；单个 AT 命令失败、端口忙或一种
      后备路径失败只能记为该 Provider/transport 失败，不能据此把正常卡片或读卡器判为不支持。
- [ ] 每个 Provider 必须先通过同一组领域合约、状态机回放和断线/重连测试，再进行有限次数的实机
      数据、短信和付费通话验证。短信发送和拨号测试禁止自动重试。

### 架构纠偏：复用成熟控制面（2026-08-18）

- [ ] 停止在 MDD 业务层继续扩写自制的 MBN/QMI/MBIM/AT 状态机。通用的是领域合约、能力语义、
      Agent 通信和恢复规则，不是假设 Windows、Linux、macOS 能共享同一个底层 Modem 实现。
- [ ] Linux 生产实现直接复用 ModemManager D-Bus；不再解析 `mmcli` 文本或复制其设备插件。
- [ ] 把 Gammu/SMSD 作为 `GenericAtSmsProvider` 的首选现成后端进行兼容性验证，而不是继续维护
      自制 PDU/短信存储状态机。它只在能够独占 AT 控制口时启用；作为 GPLv2 独立进程集成，采用前
      必须完成分发与许可证审查，不能链接或复制进许可证不兼容的 MDD 模块。
- [ ] Windows `system_managed` 只发布经过实机验收的能力。MBN/RMNET 可以独占网络 function，独立
      Modem 服务可以独占另一个 AT function，但必须由同一协调者按 PnP parent/IMEI 绑定、串行化
      状态改变并通过“数据在线时短信/通话”并发测试；不得让 MDD Agent 与 Gammu 同时打开 COM 口。
      Windows SMS 应用 API 需要设备授权、元数据和用户同意，不能被当作无人值守服务必然可用的
      通用实现。
- [ ] macOS 没有可验证的系统蜂窝控制面时，只允许专项 TODO 验收通过的原始 USB/独占 AT/私有
      数据 Provider，或明确 `unsupported`；不为每个 USB Modem 编写业务层特判。
- [ ] 只有无法按独立 USB function 安全分工的设备才进入 `agent_managed` 整机模式：先把整个 Modem
      从宿主网络管理器安全释放，再交给一个成熟控制面独占。没有原配置快照、回滚和拔插恢复前，
      该模式不得进入生产，也不得在页面显示为可用；它不是当前 Windows EC20 的首选路径。
- [ ] 在确实需要该权限模型的平台，安装器一次性申请管理员权限并安装两个进程：日常低权限 Agent
      负责发现、状态和 WSS；最小特权守卫只负责设备 lease、隔离和恢复。macOS 首版若经专项 TODO
      的“无宿主网络 function”资格门禁通过，不为架构对称额外引入常驻守卫。运行期 IPC 必须带访问控制。
- [ ] 发布包不得假定用户手工安装 Python、Gammu、ModemManager 或厂商工具；每个平台的安装器应
      探测受支持后端及版本，能合法捆绑的固定版本随包提供，系统后端则做明确的版本/能力预检。

### 现成后端调研与选型记录（2026-08-18）

- [x] [Sigmo](https://github.com/damonto/sigmo) 是目前最接近“独立 Modem 服务”的可复用实现：
      MIT、单个 Go 二进制、版本化 `/api/v1`，公开版直接管理 QMI/MBIM，已提供 Modem/SIM、SMS、
      USSD、网络扫描与注册、飞行模式、频段、数据连接、eSIM，以及绑定蜂窝接口的 HTTP/SOCKS5
      （含 UDP）出口。第一轮 PoC 优先把它作为独立进程运行，由 MDD 只写小型 API Adapter；不得
      把其页面、消息数据库或用户体系并入 MDD。
- [ ] Sigmo 进入正式依赖前必须补齐固定提交、可重复构建、API 合约夹具和断线恢复验证。其底层
      [wwan-go](https://github.com/damonto/wwan-go) 也是 MIT/pure Go，协议层覆盖 QMI、MBIM、AT、
      UICC，但目前无稳定版本标签；直接 QMI/MBIM transport 仅支持 Linux，Windows 只支持 AT
      串口。因此不得把“Go 可交叉编译”误写成 Windows/macOS 已支持直管蜂窝数据。
- [ ] Sigmo 仓库中的 IMS、VoLTE/VoWiFi 通话及 WebRTC 路由不能作为当前可交付依赖：相关 Pro
      代码虽声明 MIT，但构建依赖私有 `ims-go` 等模块。公开版先只承担已可构建和验收的数据、
      SMS、USSD、SIM/eSIM 与网络控制；通话继续走下述 ModemManager/Asterisk 能力链。
- [x] [ModemManager](https://github.com/linux-mobile-broadband/ModemManager) 仍是稳定生产基线：
      D-Bus 统一 AT/QMI/MBIM，通用 QMI Provider 已实现 Voice service 的拨号、接听、挂断、DTMF
      与来电状态；是否可用由模块实际暴露的 QMI Voice service 决定，而不是按 EC20 型号猜测。
      音频是独立能力，不能因信令成功就宣告浏览器语音可用。
- [ ] Linux 通话 PoC 先验证
      [asterisk-chan-modemmanager](https://github.com/koreapyj/asterisk-chan-modemmanager) 作为独立 GPLv2
      Asterisk 模块：它复用 ModemManager Voice 并支持 USB Audio/ALSA、SMS 和 Quectel 初始化命令，
      有机会替代自制信令/媒体胶水。采用前需核对目标模块是否枚举 USB Audio，及 GPL 分发边界；
      EC20 的 NMEA PCM 仍可能需要一个小型厂商媒体 transport，不能污染通用 CallBackend。
- [x] [ModemDeck](https://github.com/human-agent65535/ModemDeck) 已证明“Linux 独占模块 +
      ModemManager + 薄 HTTP Agent + Asterisk/WebRTC + 独立数据面”架构可运行，但许可证为 PolyForm
      Noncommercial。它只作为 Provider/lease/恢复/媒体分层的参考，未经单独授权不得复制、链接或
      作为商业分发依赖。
- [x] 未找到成熟、活跃且跨平台的 ModemManager REST/gRPC 包装层；低活跃度包装项目不能替代
      Sigmo 或 MDD 自己的薄 Adapter。MDD Adapter 只转换现有领域小接口与后端 API/D-Bus，不实现
      QMI/MBIM、PDU、网络注册或 Modem 插件。
- [x] WSL2/usbipd-win 方案经用户否决：运行时、安装和维护成本过重，不进入 MDD 主线；不得安装、
      升级或启用目标机现有 WSL，也不得要求用户维护 Linux VM。其调研只证明 USB 整机独占可解决
      Windows MBN 争用，不作为产品依赖或 PoC 前置条件。
- [ ] Windows 首选轻量组合已收敛为：保留系统 MBN/RMNET 独占 MI_04 高速数据 function；一个独立
      `ManagedModemService` 独占 MI_02 AT function，并复用 Gammu/libGammu 的短信、USSD、来电事件
      和通话信令状态机；媒体桥首选同 USB container 的 UAC/WASAPI，失败时才租用 MI_01 NMEA PCM。
      PnP parent/IMEI 只做硬件 attachment 绑定，线路、出口和业务仍稳定映射 ICCID。
- [ ] Windows RAS/PPP 只保留为无 MBN/MBIM 数据 function 设备的兼容 Provider，不作为当前 EC20
      主线：目标机 `Quectel USB Modem #2` 的系统属性报告 `MaxBaudRateToSerialPort=115200`，未证明
      实际吞吐不受限前不能用它替代已实测 150 Mbps 链路的 RMNET 网卡。
- [ ] 优先安装一个独立服务并暴露版本化 localhost IPC/API；MDD 只写
      `ManagedModemServiceProvider`。若 Gammu 没有现成的统一服务 API，只允许写一个持有单一
      libGammu state machine 的小型 GPL companion daemon，不能让业务 Agent 反复 shell-out 或
      与 SMSD 争抢串口。不得自行实现 PPP、PDU、QMI/MBIM 或 Windows 虚拟网卡协议。
- [x] [OpenGSMGateway](https://github.com/demogorgonz/OpenGSMGateway) 虽标称 Windows/Linux Call/SMS
      API，但实现主要是 PowerShell 串口脚本、固定 sleep 后挂断和粗粒度文件锁，没有持久 URC/
      call state machine；只作为“Windows 串口路径可行”的参考，不作为生产依赖。
- [x] 不采用 EC20 `RNDIS/ECM + AT` 作为通用主线：USB mode 会写入模块配置，Windows 驱动切换可能
      丢失 COM 口；EC20 RNDIS 数据会话还可能只能通过 `CFUN` 断开并连带破坏短信/通话。该方案只
      保留为明确型号、可恢复且用户主动选择的兼容模式。
- [ ] macOS 没有 WSL/usbipd-win 等价的成熟系统路径；Lima 的通用 USB passthrough 仍未成为稳定
      能力。第一版保持同一 MDD Adapter/领域合约，但仅启用经过验证的独占 AT/CCID 后端；数据和
      通话缺失时明确 `unsupported`。不得为追求表面上的三平台一致而内嵌一套新的 QMI/MBIM 栈。
- [x] 2026-08-19 已在 macOS 15.2 (Apple Silicon) 取得 EC20 的真实枚举基线，此前"USB 树完全没有
      设备"是 Type-C 直连协议握手问题，经转接降级到 USB 2.0 后解决，与驱动和 Agent 代码无关。
      实测 composition 为 `2c7c:0125`，`Device Speed=2`(480 Mb/s)，`Current Required=500 mA`
      且可用余量恰为 500 mA（长时间 RF 需外部供电或有源 Hub）。5 个 function 全为
      `bInterfaceClass=255` 厂商自定义（DIAG/NMEA/AT/MODEM/QMI-RMNET），因此 macOS 不创建任何
      `/dev/cu.*`，也没有蜂窝网卡：AT 通道必须由原始 USB transport 独占 claim。数据面不得为了
      方便先切成会被 macOS 使用的 CDC ECM；首个安全 PoC 按专项 TODO 验证私有 PPP 数据面。
- [x] macOS 通话音频不是阻塞项：同一 EC20 还暴露 `bInterfaceClass=1` USB Audio Class function，
      macOS 已自动匹配 `AppleUSBAudioDevice`，`SPAudioDataType` 中出现两个 Quectel/USB 端点
      （capture + render）。与 Windows UAC 路径同源，因此 macOS 侧可复用 CoreAudio 而无需厂商
      驱动；设备选择仍必须按同一 USB container 匹配，禁止按名称或音频索引猜测。

#### 首轮 PoC 判定门槛

- [ ] 当前优先级固定为 Windows EC20 主流程；Linux 只作为已知可用的成熟后端基线，不继续占用
      当前实机验证时间。Windows 数据、SMS、通话信令与媒体的 Owner 方案走通后，才开始 macOS
      独立调研和实现；不得以 Linux 构建通过替代 Windows 完成度。
- [ ] 只在获得切换授权后改动 EC20 Windows 主机：先快照 MBN Profile、PnP/USB identity、当前
      数据与隔离状态；轻量直管失败时必须一键恢复现有 Windows MBN 数据出口，不改变持久 USB mode。
- [ ] 组合 Provider 以同一物理模块完成 ICCID/IMEI、注册状态、APN、漫游、数据启停、绑定模块接口的
      SOCKS5 TCP+UDP，以及断开/重连。普通 Windows 应用不得使用该蜂窝接口；Agent/后端退出时
      出口必须立即关闭。
- [ ] SMS 先做无费用的列表/接收和状态回放；仅在后端同时保持数据连接且明确报告可发送后，人工
      授权一次发给本机号码，禁止自动重试。若本机验证证明该驱动不能并发，则允许由协调者受控暂停
      数据完成一次发送并自动恢复，但必须先单独验证“暂停后的本机发送成功”和“恢复后出口可用”。
      通话先验证信令能力与音频端点，再按相同所有权/恢复规则仅拨打一次授权号码并立即挂断。
- [ ] PoC 通过后，MDD 只保留 `ManagedModemServiceProvider` Adapter；Windows/Linux 的业务 API、
      页面、ICCID 稳定入口、短信历史和 Asterisk/WebRTC 路径完全复用。PoC 未通过则保留当前已验收
      的 Windows MBN 数据出口，不再继续扩写 MBN 短信/通话补丁。

#### Windows 轻量组合 PoC 证据（2026-08-18，无短信/通话费用）

- [x] 目标机 USB 组合设备按 function 枚举为 MI_04 `Quectel Wireless Ethernet Adapter #2`、MI_03
      `Quectel USB Modem #2`、MI_02 `Quectel USB AT Port`、MI_01 `Quectel USB NMEA Port` 和 MI_00
      DM；PnP parent 与 IMEI 用于把这些临时 attachment 组合为一个 Modem，生产代码不得硬编码
      COM14/15/16 或 MI 编号。
- [x] 未安装 WSL、未停 Agent、未断 MBN、未改驱动或 USB mode；便携版 Gammu 1.43.3 独占当前
      空闲的 COM16，成功识别 EC20F、固件、IMEI、IMSI，枚举 SIM/ME 短信文件夹并完成存储扫描，
      `monitor 1` 成功启用 incoming SMS/CB/call/USSD 通知。退出后 COM 口立即释放。
- [x] 上述 Gammu 操作期间，MI_04 始终保持 LTE Connected、漫游 460-11 China Telecom、APN
      `ctnet`；Gammu 同时读取到 GPRS attached、信号和网络状态。因此当前设备已证明“MBN 高速
      数据 function + 独立 AT/Modem function”基础并发成立，不能再以物理模块为粒度一刀切禁止。
- [x] 当前 Windows MBN helper 的 `GetSmsConfiguration` 返回 `E_PENDING (0x8000000A)`；系统
      `netsh mbn show smsconfig` 同时能读到 PDU 格式、SMSC `+85362101201` 和存储容量，但明确显示
      `Ready to send SMS: No`。这证明当前失败是 MBN miniport/SMS control path 的限制或异步状态，
      不是 SIM 不支持短信；主线改用独立 AT 后端，不继续把 MBN SMS 补丁当成唯一出路。
- [x] 2026-08-19 已在停止 Agent、排除服务端后用三条 Windows 本机路径复核：系统 `netsh mbn`、
      独立 MBN COM helper、独立串口脚本/Gammu。SIM ready，短信配置可读，SMSC 保持卡片提供的
      `+85362101201`（香港号码使用澳门 SMSC 不构成错误）；AT 显示 CS 未注册、PS/LTE 漫游注册、
      IMS 未注册。关键可重复差异是：断开 `ctnet` 数据后 Windows 显示 `Ready to send SMS: Yes`，
      Agent 恢复数据连接后变为 `No`。因此下一门槛是在 Windows 本机断数据状态完成一次人工授权
      发送，再验证数据/Profile/隔离/代理恢复；此前不得继续扩写远端桥接。
- [x] 进一步官方资料核对已确认当前实机是硬件支持短信/语音、但固件基线与目标网络承载不匹配：
      当前 `EC20CEHDLGR06A07M1G` 自动激活 `OpenMkt-Commercial-CT`，却保持 IMS `0,0`；移远官方社区
      针对同一完整固件号明确回复“R06 基线不支持电信 VoLTE，需要重新采购 R08 基线版本”。这与
      CS 未注册、LTE 漫游注册、MBN/Gammu 两条发送路径均在网络完成阶段失败一致。第三方博客也只
      能证明 HDLG 型号硬件表含 SMS/Voice，并明确提醒旧固件不支持纯 VoLTE 短信。不得把该限制
      归因于 `+85362101201` SMSC（香港卡使用澳门短信中心可以正常），也不得用换 CLI、重写 PDU、
      强刷跨基线固件或继续修改服务端掩盖。
- [x] 2026-08-19 在用户明确授权可报废当前调试硬件后，已先保存该模块独有的 QCN/EFS、原 R06
      固件和完整只读基线，再人工使用 QFlash 把精确 HDLG 包升级为 `EC20CEHDLGR08A06M1G`；工具
      显示 PASS，IMEI/ICCID/RF 均保留。升级后 `CREG=5`、`CEREG=5`，自动激活
      `VoLTE_OPNMKT_CT`，IMS/VoLTE 已开启。该一次性实验成功不改变生产安全规则：未知 SKU、无本机
      QCN、无精确回退包或无人值守场景仍禁止跨基线自动刷写。
- [x] 已记录固件升级安全流程和用户引导规则：自动读取版本并提示兼容性是允许的；同分支同基线
      升级必须具备官方包、发布说明、SHA-256、匹配 QFlash、原固件和本机 QCN/EFS 备份，并由用户
      明确确认停机后执行。R06→R08 因 CEFS/校准差异必须拒绝自动刷写并提示更换硬件/厂商服务。
- [x] 已建立 Windows 离线批量升级包和仓库维护脚本：套件总清单覆盖 740 个工具、固件、文档与
      脚本文件并通过 SHA-256/Quectel 签名校验；逐台脚本实测取得 986 个默认 NV、53 个 SIM NV、
      1184 个 EFS 文件的 XQCN，完整备份约 25 分钟。脚本按 IMEI 隔离备份、拒绝缺失/过小/校验不符
      的 XQCN，输出 QFlash DM 端口/460800/精确 Firehose 路径，并在 DIAG 后执行受控 CFUN 复位、
      等待 Agent 与真实蜂窝数据恢复。工具/固件/QCN 不进入 Git；流程维护于
      `agent/windows/ec20-upgrade/`。
- [x] 已实现签名固件兼容矩阵及设备页提示：`control/app/firmware_matrix.py` 只做检测，不下载、
      不解包、不刷写。硬件分支与基线一律从 `AT+GMR` 修订串推导，不使用 `ATI` 的营销型号名
      （同一 `EC20F` 覆盖多个不兼容分支）。Agent 新增 `firmware` 字段（Provider snapshot 优先，
      否则 `AT+GMR`；解析失败保持空串而不猜测），经 hello 进入 registry 并出现在设备页。
      `EC20CEHDLGR08A06M1G` 为已验收基线；`EC20CEHDLGR06A13M1G` 记录为已知缺陷基线并声明
      `impact=sms/ims`，设备页直接说明“该基线未启用 IMS/VoLTE，LTE-only 附着下 MO 短信会以
      未指明错误被拒绝”。“引导升级”在结构上要求三个条件同时成立：同基线、矩阵记录了官方包
      SHA-256、且不涉及改写校准/CEFS；当前 R06→R08 跨基线且未记录包摘要，因此只能显示
      “需人工停机升级或更换硬件”并指向 `agent/windows/ec20-upgrade/README.md`。未收录分支或
      同分支未验收基线一律为 `unknown`，只要求人工核对，不给升级引导也不判定故障。
- [ ] 补录 `EC20CEHDLGR08A06M1G` 官方包 SHA-256（来自离线套件 `SHA256SUMS.txt`）到
      `Branch.target_package_sha256`。在补录前矩阵按设计无法显示任何“引导升级”；补录也只解锁
      同基线场景，跨基线仍必须人工。远程无人值守刷写在至少一种合格 Windows 模块完成升级、
      断电恢复、数据与短信实机验收前不实现。
- [x] 短信提交前置条件已可诊断，不再依赖复现：`sms_service_center` 之前只由 Windows MBN 上报
      且被服务端丢弃，现在通用 AT 路径也读取只读 `AT+CSCA?`（60 秒缓存、从不回写），服务端在
      `sms_diagnostics` 中同时暴露短信中心和 advisory。advisory 在 `sms_ready=True` 时也保留，
      因为 2026-08-18 的失败正是“驱动报告就绪但提交仍被拒绝”。提交失败信息附加当时的短信中心、
      bearer 和 CREG，把“error 350 / 未指明错误”变成可判断的前置条件缺失。香港卡使用澳门 SMSC
      属正常，矩阵与 advisory 都不据此判定故障。
- [x] APN 候选为空或有多个时不再返回死胡同文案：`_apn_guidance` 区分“系统 Profile 有多个”、
      “Modem 报告多个候选”和“完全没有可用 APN”三种情况，并附带 MCC/MNC 前缀（只取 IMSI 前 5 位，
      不回显完整 IMSI）。该文案只使用 Agent 已知值，连接路径不新增 AT 往返，避免重现按连接
      重复探测导致的握手超时。
- [ ] 未知 MCCMNC 的 APN 最终解法仍未验证：候选为空时是否可用“空 APN / 网络指派默认 APN”附着，
      必须先按 Windows 本机先行门槛在本机脚本（netsh mbn / MBN profile 无 AccessString）走通真实
      硬件，再决定是否放开 `_save_cellular_profile` 的非空 APN 校验。当前实现只负责把缺失说清楚。
- [x] APN 候选为空时优先补两条通用来源，不自建运营商表：
      1. 激活后读 `AT+CGCONTRDP`（3GPP TS 27.007）取网络实际指派的 APN 并回写为 profile，只读、
         无费用、与运营商无关，可覆盖“网络会指派默认 APN”的多数场景；
         已实现于 `ModemControl._modem_profile_candidates`，仅当 `CGDCONT` 无可用的非服务上下文时
         回退读取 `CGCONTRDP?`。
      2. 引入现成运营商 APN 数据库
         [mobile-broadband-provider-info](https://gitlab.gnome.org/GNOME/mobile-broadband-provider-info)
         作为候选来源。已核对许可证为 **CC-PDDC**（Creative Commons 公共领域奉献），
         NetworkManager/ModemManager 长期使用，属数据而非 GPL 代码，可安全随发布包分发。
         已随包置于 `agent/data/serviceproviders.xml`，`agent/apn_providers.py` 按 MCC/MNC 解析
         并过滤 `usage="internet"`，结果只加入 `suggested_profiles` / APN guidance，不作为自动创建
         profile 的依据。
      两者都只提供“候选”，最终 profile 仍由用户确认；不得据此自动改写已在用的 profile。
- [x] 2026-08-19 实机数据推翻“SMSC 为空即将失败”的假设，已按证据回退该判定：EC20 在 R08 上
      `sms_ready=true`、`sms_provider=auxiliary_at`、短信可发，但 Windows MBN 报告的
      `sms_service_center` 为空串——模块把短信中心维护在 MBN 接口之下。因此空 SMSC 只作为事实
      展示，不再产生 advisory（否则会把正常设备标成可疑）；`service_centre()` 改为平台值为空时
      回落到只读 `AT+CSCA?`，把“缺信息”补成真实值而不是发警告。失败时仍记录当时的短信中心。
- [ ] SMSC 仍缺一块，需要实机验证后再做：
      1. 蜂窝路径没有“显式设置 SMSC”的入口（VoWiFi/线路路径已有 EF_SMSP 读取与手工覆盖）。若要
         支持 `AT+CSCA=` 写入，必须先快照原值、仅由用户显式触发、可回滚，并单独验证写入是否
         持久化到 EF_SMSP；不得在自动恢复或心跳路径里写。
- [x] 按 ICCID 记录“最近一次成功提交时的 SMSC”，之后仅在该值发生变化时提示。这是唯一能区分
      “SMSC 缺失”与“SMSC 错误”的低成本手段，且不需要额外收费尝试。已实现于 `agent/sms_history.py`，
      在 `ModemCard.sms_send` 成功后写入 `~/.mdd-agent/sms_smsc.json`，状态页暴露
      `sms_service_center_changed` 与 advisory。
- [x] R08 实机 `AT+QPCMV=? -> (0,1),(0-2)`，支持 USB NMEA PCM、Debug UART 和 UAC。已用
      guarded compare-and-set 只把 `usbcfg` 最后的 UAC 位从 0 改为 1，其余 VID/PID 和 6 个 function
      位原样保留；配置会立即触发 USB 重枚举，因此实现不能把旧 COM handle 的预期写超时误判为
      配置失败，必须等待同一 USB container 重现并回读。Windows 随后使用系统 `usbaudio.sys`
      枚举 `AC Interface`、capture 和 render endpoint，三者与该 Modem 的 AT/NMEA/MBN function
      具有同一个 PnP ContainerId；设备选择必须按 container 映射，禁止按 `AC Interface` 名称或
      PortAudio 数字索引猜测。Agent 与 `ctnet` 数据在重枚举后已恢复并保持 Connected。
- [x] 无声卡/AudioSrv 不可用时的 NMEA fallback 生命周期已在同一 Windows 实机完成无通话 dry-run：
      快照 `QGPSCFG outport=usbnmea` → 临时 `none` → `QPCMV=1,0` → 独占 NMEA COM 并写入一帧
      1600-byte PCM → `QPCMV=0` → 恢复 `usbnmea`；最终 Agent 和 MBN 数据均恢复。真实实现必须把
      GNSS/NMEA 当作可恢复租约，任何异常、拔插或进程退出都执行幂等清理。
- [x] Windows UAC 运行时候选已做脱离项目的本机 PoC：`malgo/miniaudio` 生成约 4.8 MiB 单文件，
      只依赖 Windows 系统 DLL；按明确 WASAPI endpoint ID（不是默认声卡或名称）以
      8 kHz/S16/mono duplex 连续打开、读写、关闭 10/10 次。临时 SYSTEM 计划任务的非交互会话也
      成功打开同一 capture/render endpoint 并取得 16000 capture frames。因此客户机没有实体声卡
      不是阻塞；EC20 UAC 自身就是虚拟声卡。该结果尚不替代拔插恢复、AudioSrv 故障注入和真实接通
      上行验收，后两类失败必须自动切换到 NMEA provider 或明确 fail-closed。
- [ ] 双向蜂窝媒体最后门槛：一次有界外呼已通过 UAC 捕获 100800 帧非静音下行振铃 PCM
      （8 kHz/16-bit/mono），但对端处于 dialing → alerting 后未接听，因此没有注入上行测试音，
      不得据此声明 `call_audio=true`。取得一个会接听的测试端后只做一次短时双向验收，必须
      `ATH`、`QPCMV=0` 并恢复 Agent/数据；失败和超时均禁止自动重拨。官方 `AT+QAUDLOOP`
      在本固件只回环模拟 MIC/SPEAKER 路径；空闲 UAC 注入 997 Hz 后 capture 仍为静音，不能用它
      代替真实接通验收或伪造上行成功。

#### Gammu CLI Adapter 实施与实机结果（2026-08-18）

- [x] 已实现进程外 `GammuCliProvider` 和组合 Provider：Windows MBN 继续拥有数据 function，Gammu
      只探测未被占用的独立 Modem/AT 端口，并且必须读取到与 MBN 相同的 IMEI 才能组合。端口号、
      型号和 USB MI 编号均未写死；当前实机自动得到 COM14（Agent）+ COM16（Gammu）。
- [x] Gammu 以官方 1.43.3 独立 GPLv2 可执行程序旁挂，MDD 不链接或复制 libGammu；Windows 目标机
      同目录部署了 `gammu.exe` 和许可证文本，未安装 WSL、未切换驱动、未改变持久 USB mode。
      用户自行安装、PATH、同目录和 `--gammu/--gammu-port` 四种发现方式已写入安装文档。
- [x] 已处理 Windows 中文环境的 UTF-8/GBK 输出差异、opaque SMS 存储 ID、SMS 列表短缓存、
      Gammu 不实现 `getdisplaystatus` 时的操作状态回退，以及 OS 数据心跳不再被 Gammu 串口锁饿死。
      付费操作超时统一返回 `unknown/retryable=false`，服务端不跨 transport 或断线自动重试。
- [x] 当时自动化回归为 `589 passed, 25 subtests passed`。Windows 计划任务模式实机验证：registry online、
      MBN/RMNET 数据 connected、漫游开、APN profile `MDD-2529-ctnet`、严格隔离 ready、反向 SOCKS
      ready；页面数据/漫游/SMS/Call 均为 on。APN 候选完整返回 `ctnet/ctwap` 的 CID、PDP type、
      auth 和 username 字段；短信列表/历史读取成功。
- [x] 按授权只拨打 `22333322` 一次，Gammu 返回 dialing，随后成功执行 cancelcall 并在服务端记录
      ended；没有自动或人工重拨。该结果只证明通话信令，不证明浏览器双向音频。
- [x] R08 升级后 Windows 本机由持久 `AuxiliaryAtProvider` 所用的标准 AT 路径完成一次授权短信
      发送，网络返回 `+CMGS: 28 / OK`，接收方确认收到；目标是另一测试号码，不是模块本机号码，
      因此不能写成“自发自收”。SMSC 保持 SIM 提供的 `+85362101201`，未修改。组合 Provider 现会
      在 Windows MBN 运行时明确返回 `0x8000000A` 时自动使用已独占的 AT 信令后端，不绑定型号、
      COM 号、国家或运营商，也不会自动重试收费操作。
- [x] 已完成无费用根因诊断：`CREG=2`（CS 未注册），`CGREG/CEREG=5`（PS/LTE 漫游已注册），
      原配置 `CGSMS=1` 却强制 MO SMS 走 CS；按 Quectel/3GPP 定义改为 `CGSMS=2`（PS 优先、CS
      回退）并回读成功，但正确号码的后续实机发送仍为 error 350，证明 `CGSMS=2` 只是必要的
      通用修正而不是本机根治。不得再以此宣告发送已修复。
- [x] 已完成第二轮无费用 IMS 实机探测：在 LTE 漫游已恢复为 `CGREG/CEREG=5` 后，将
      `QCFG="ims"` 强制为 1 并连续观察约 90 秒，18 次均为 `1,0`，旧固件也不支持
      `QIMSREG` 查询；最终自动恢复 `QCFG="ims"=0,0` 并重启 Agent。当前固件
      `EC20CEHDLGR06A07M1G` 的活动 MBN 没有可用 IMS 会话，同时 CS 未注册，这与 error 350
      完全一致。下一步必须取得与 **HDLG** 硬件分支严格匹配、含运营商 VoLTE/IMS MBN 的移远
      官方固件/配置及校验值后另行受控升级；禁止混刷 HCL/FDK/FILG 或来源不明镜像。
- [x] 2026-08-20 在 R08 实机定位并修复“页面仍显示 Windows 漫游缓存、但短信/通话突然全部
      不可用”的独立 UICC 故障：两个 AT function 均返回 `QSIMSTAT=0`、`CPIN=SIM failure`、
      `CREG/CEREG=0`；停止 WWAN AutoConfig 后结果不变，排除 Windows 独占。一次标准
      `CFUN=0` → `CFUN=1` 后恢复 `QSIMSTAT=1`、`CPIN=READY`、正确 ICCID 以及
      `CREG/CEREG=5`，服务端实测 `call.actual=on`、`sms.actual=on`。Agent 已加入与数据、短信、
      语音解耦的通用 UICC maintainer：只在已有 IMEI+ICCID、无注册且明确 SIM failure 时尝试一次，
      跨服务重启锁存，成功后解锁；PIN/PUK、未知状态、空槽和持续硬件故障均不重启循环。网页数据
      漫游仍保持关闭；移远 `roaming/voicecall=0` 经官方手册核对含义为允许漫游语音，未被改写。
- [x] UICC 恢复补齐启动时序：Windows MBN 枚举前若已确认 IMEI 的 AT function 读到
      `CFUN=0`，只补做一次持久化保护的 `CFUN=1`；直接 CPIN 失败且插卡状态为 0 时不再被过期
      的注册缓存掩盖。`CFUN=4` 等人工无线电状态不覆盖。实测所有 AT function 已沉默时 Windows
      PnP restart/disable-enable 均不能替代 USB 冷断电，因此这种明确状态只提示重新插拔，不宣称
      软重启可以修复；重新枚举后仍走同一通用自检和期望状态恢复。
- [x] 成功 attachment 后在 Agent 本地持久化 `IMEI -> last successful ICCID`，只作为 MBN 丢卡时
      一次有限 UICC 恢复的授权证据，不作为当前在线身份上报。旧版升级迁移仅接受 Windows 中唯一
      的合法订阅 ICCID；多个历史 SIM 时 fail-closed，不按 COM、运营商或营销型号猜测。
- [x] 通用 Agent 已把“信令 Provider 已安装”和“当前网络允许发送”解耦：每 60 秒最多一次只读
      承载探测；LTE 已注册但 CS 与 IMS 均不可用时仍保留短信读取能力，把发送标为 unavailable，
      并在调用 Gammu 前拒绝提交，避免继续产生收费尝试。检测不绑定 EC20、COM 口、Windows、
      国家或运营商；CS/IMS 恢复后自动重新判定。最新 R08 实机恢复后为 `sms_ready=true`、
      `call_ready=true`；数据漫游策略仍关闭，因此数据/出口单独保持不可用，不影响短信和通话。
- [ ] 短信页心跳抖动已做两层修复：设备能力心跳不再触发“换线路”清空会话、收件人和草稿；
      重复 WebSocket 数据保持原数组引用；页面再以实际可见字段做 memo，诊断时间戳/心跳变化不再
      重绘 SIM 下拉框与全部气泡。最新静态包 `index-CbLZks4D.js` 已部署，仍需用户刷新后确认视觉
      验收；确认前不能把该项标为完成。
- [ ] 飞行模式切换仍有状态语义问题：开启操作会断开数据，但该次页面样本仍显示
      `flight.desired=true / actual=off`；关闭后 radio、数据、代理和隔离均已恢复。必须用 Windows MBN
      radio postcondition 修正 actual 映射后再宣告该开关通过，不能仅凭 RPC 返回成功。
- [ ] 当前 CLI Adapter 是验证稳定领域边界的过渡实现，不等于下文的长期 companion 完成。生产版
      仍需一个持有单一 libGammu state machine 的独立 GPL companion，通过本地 ACL IPC 同时处理
      命令与 URC；解决 SMS error 350、来电状态持续跟踪和崩溃恢复后才能勾选该项。

#### Windows EC20 数据 / SIM 通道所有权结论（2026-08-18）

- [x] 当前全量自动化回归：`603 passed, 25 subtests passed`。
- [x] 通用 `AuxiliaryAtProvider` 已用标准 `CUAD/CCHO/CGLA/CCHC` 完成只读 SIM 探测；Windows 数据
      关闭且 Modem 干净启动时，实机可完整读取 ICCID、IMSI、SMSC 与 SPN。实现不绑定 EC20、COM
      编号、运营商、AID 或当前 SIM。
- [x] 同机并发验收推翻了“不同 USB function 必然可以安全并发”的假设：当前 Windows 驱动/固件下，
      MBN 数据已连接时打开 USIM logical channel 会导致 ADF.USIM 选择失败，并使已激活的数据上下文
      立即变为 DEACTIVATED。这是实机能力边界，不在服务端或页面加入型号特判。
- [x] Agent 已改为先恢复持久化的 radio/roaming/data 意图，再决定是否开放 VPCD；Windows 蜂窝数据
      活跃期间暂停 APDU/VPCD，设备页继续使用 MBN 身份和缓存 SIM 信息。数据关闭后才允许 APDU
      重新获得 SIM 通道，避免页面轮询破坏数据出口。
- [x] 最新实机验证已保持 MBN profile `MDD-2529-ctnet` 在线、严格 WFP 隔离就绪、反向 SOCKS5
      稳定可用；VPCD 在数据活跃后保持断开，没有再次抢占 SIM 通道。
- [x] ePDG 路由加入公共地址校验：运营商 ePDG DNS 返回 `127.0.0.1` 时明确标记不可用，且不得把
      loopback/private/non-global 地址写入线路路由。当前 SIM 因无公共 ePDG，不能把 VoWiFi 短信
      或通话标为可用。
- [x] 静态适配器能力与运行时网络能力分离：Gammu/ATD 存在只表示“可执行该命令”，只有 CS 注册或
      可用 IMS 承载实际就绪时页面才显示蜂窝 SMS/通话可用；`sms.send`/`call.dial` 在付费提交前
      fail-closed，状态轮询不自动重试。
- [x] 当前香港 SIM 的蜂窝短信发送和远程通话信令已完成实机验收：R08 下 CS/LTE 漫游均注册，
      数据保持在线时 AT 短信提交成功；只拨打 `22333322` 一次，CLCC 从 dialing 进入 alerting 后
      `ATH / OK` 挂断。服务端现显示 SMS/Call 均为 on，短信会话列表可读，通话状态最终为 idle。
      CLCC 解析只接受标准 `mode=0` 语音记录，不再把 `mode=1` 数据上下文误报为活动通话。浏览器
      双向音频仍未完成，必须等独立 UAC/NMEA 媒体桥验收后才能声明可用。

### Provider 边界

业务层继续使用下文的小接口，不引入庞大的全能 Backend；平台 Provider 只负责为这些接口提供
实现并隐藏 MBIM、QMI、D-Bus、COM、AT 端口和系统 Profile 等细节：

- `WindowsMbnProvider`：用 Win32 MBN/COM 获取状态、Profile、连接、短信和设备服务；仅在桌面
  MBN 能力确实不足且权限允许时，增加隔离的 `Windows.Devices.Sms` broker。
- `LinuxModemManagerProvider`：通过 ModemManager D-Bus 提供数据、短信和通话能力，底层协议由
  ModemManager 插件选择。
- `MacOSProvider`：只声明系统或已验证厂商接口真实提供的能力，不仿造 Windows/Linux 成功状态。
- `GenericAtProvider`：作为操作系统未接管设备时的回退；Quectel/其他厂商适配只覆盖 quirks、
  初始化或音频映射，不复制整套短信、通话和数据业务。
- `DirectMbimQmiProvider`：可选的 Agent 直管实现；仅在能够证明设备已从系统管理器安全释放时
  声明能力，并复用与其他 Provider 相同的小接口和领域状态，不产生 EC20 专属业务路径。

### Windows Provider 设计记录（2026-08-18）

- 参考 [ModemManager](https://github.com/linux-mobile-broadband/ModemManager) 的能力接口与插件覆盖层；
  Windows 使用微软 `IMbnInterface/IMbnConnection/IMbnSms` 的异步 request-id/completion 语义。
  原生 COM helper 由 MIT 许可的 Microsoft CsWin32 生成声明，不复制 GPL 实现。
- 当前旧 Quectel miniport 对 legacy `IMbnConnection.Connect` 返回成功回调却不激活；兼容层只用
  系统 `netsh mbn connect` 触发已有 Profile，忽略退出码和本地化文本，最终结果仍由原生 MBN 确认。
- 单一 Owner 为 `system_managed/windows_mbn`：AT 口只用于硬件发现/存活，不执行 SIM、APDU、短信
  或通话业务。Provider 有有界枚举窗口，避免 COM 口先出现时 Generic AT 抢占控制面。
- WFP 只允许打包 Agent、无任意联网功能的 MBN helper 和系统控制程序；普通进程仍被阻断。长操作
  在串行 worker 执行，WebSocket 主循环继续处理 ping，避免操作超时制造 attachment 重连。

### Windows Direct Voice 研究记录（2026-08-18）

- Quectel 官方《EC2x&EG9x Voice Over USB and UAC Application Note》明确覆盖 EC20 R2.0/R2.1：
  `AT+QPCMV` 只负责启停媒体路径，Voice-over-USB 通过 NMEA 串口传输 8 kHz/16-bit/mono PCM，
  UAC 通过 USB 声卡；拨号、接听和挂断仍使用标准 `ATD/ATA/ATH`。该文档只作为厂商媒体
  transport 覆盖层依据，不能把 EC20 型号判断写进短信、通话或页面业务层。
- 当前实机 MBN 能力为 `MBN_VOICE_CLASS_NO_VOICE`，所以 `system_managed/windows_mbn` 本身不能
  宣告通话；但受控启用模块固件已有的 UAC function 后，Windows 已通过系统 `usbaudio.sys` 枚举
  capture/render endpoint，且与 AT/NMEA/MBN function 共享 PnP ContainerId。组合 Provider 因而可
  在不断开 MBN 的情况下用独立 AT function 提供信令、用 UAC（首选）或 NMEA（兜底）提供 PCM。
  控制信令、媒体枚举和真实双向音频仍是三个独立能力，不能互相代替验收。
- 实机进一步证明 MI_04 的非持久化 PnP lease 本身可由原生看门狗安全持有和释放，父进程崩溃后
  也能恢复 ProblemCode 0；但当前旧 Quectel miniport 在 MI_04 恢复后不会重新注册 MBN，必须对
  整个组合设备执行受控模块重启才恢复。因此“单 function 自动切换”已从生产 Agent 撤回，不能
  仅凭 AT 拨号成功就标记 Direct Provider 完成。当前主线不再切换 MI_04，而是让其持续由 MBN
  持有；整机 `agent_managed` 仅为无法按 function 分工的后备方案。

