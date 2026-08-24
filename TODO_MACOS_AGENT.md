# TODO — macOS 统一 Agent 最小实施基线

> 状态：前置评审、M0.5 与实现后独立复审（M5）均已 PASS；M1、M2 及当前
> EC20/双读卡器的 M3 实机门禁已通过。arm64 CLI/App 已按复审后源码重建并校验；
> universal 签名/公证、第二台 Modem 与另一品牌交叉验证仍未完成。
>
> 本文件从根 `TODO.md` 摘出 macOS 专项边界。产品领域目标仍以根 TODO 为准；macOS 的实现、
> 安全不变量和验收只以本文件为准，避免继续把根 TODO 扩成平台调试日志。

## 1. 用户目标与范围

- 首个隔离 PoC 使用 **1 个 Modem**，但产品运行时从第一天按 `ModemInstanceIdentity -> context`
  集合设计，正式完成前必须并发验收至少 2 个 Modem；同时管理任意多个 PC/SC/eSIM 读卡器，复用现有
  `ManagedAgentRuntime`、Modem 领域接口、Card Agent、服务端 WSS、稳定 ICCID 出口、短信、
  Asterisk/WebRTC 和页面，不建立第二套业务系统。
- 提供等价 CLI 与 GUI（菜单栏/托盘）入口；二者只能有一个充当设备运行时。GUI 关闭窗口默认
  隐藏，显式退出才停止运行时；CLI 可前台运行，SSH 验证允许 `nohup`。
- 重复 CLI、重复 GUI 或 CLI/GUI 混合启动必须在访问配置和硬件前返回稳定冲突退出码 `9`；查询、
  doctor、日志和配置命令只是本地控制客户端，不创建第二个运行时。
- 第一版不要求主 Agent 注册 launchd plist 服务。若以后增加极小特权 helper/DriverKit，它只能
  承担安装、USB capture 或配置漂移防护，不能形成第二个 Modem 状态真相源。
- 蜂窝数据只能被 MDD 借用。首版必须保证 macOS 网络栈没有该 Modem 的 network interface、
  route 或 DNS，因此浏览器、更新器、Tailscale、EasyTier、ZeroTier 等普通网络程序在 Agent
  运行、退出或崩溃后都没有可枚举或绑定的蜂窝路径；Agent 私有会话必须 fail-closed。
- 威胁边界不包括恶意 root、第三方 USB 网络驱动，或另一个主动实现 raw USB PPP/QMI/MBIM 并
  抢占设备的程序。若产品必须抵抗这些对象，就需要常驻特权 USB owner/DriverKit/launchd helper，
  与“不做常驻服务”的首版约束冲突，必须另立方案和授权，不能在首版文案中虚假承诺。
- macOS 现有 Card Agent 已实机完成两个可区分名称的 PC/SC/eSIM reader 并行运行及正常热插拔；
  统一宿主必须原样复用这一能力，不能把它降级或另写第二套 reader manager。

## 2. 明确非目标

- 不在 MDD 业务层实现新的 QMI、MBIM、RNDIS、PPP、PDU 或 Modem 插件平台。
- 不用 PF、路由 metric、删除默认路由、绑定源地址或“专用账户”冒充严格隔离；Apple 已明确 PF
  不是稳定产品 API，这些方法也不能阻止显式接口绑定。
- 不创建 `feth`、`utun`、`ppp`、ECM/NCM 等可被 macOS 使用的蜂窝网络接口作为第一版数据路径。
- 不在首版持久修改 USB composition，也不实现“释放设备”恢复流程；先以只读资格门禁证明当前
  composition 不会暴露 macOS 可绑定的蜂窝 network function。
- 不按 EC20、USB interface 编号、端口名或产品字符串选择业务能力；Provider 依据 USB descriptor、
  标准协议、探测结果和经过审计的厂商配置能力选择。
- 未通过真实硬件 TCP、UDP、DNS、拔插、崩溃和无泄漏验收的 Provider 必须报告 `unsupported`，
  不得以页面状态映射伪装成功。

## 3. 现成工具复核与采用边界（2026-08-21）

| 工具/组件 | 可直接复用部分 | 不能作为完整成品的原因 | 决定 |
| --- | --- | --- | --- |
| macOS `/usr/sbin/pppd` | 标准 PPP 拨号与协议协商 | 创建宿主 `ppp` 接口；其他进程可显式使用，父进程退出也不能单独证明无泄漏 | 不作为严格数据路径 |
| [qcseriald-darwin v1.0.6](https://github.com/iamromulan/qcseriald-darwin/releases/tag/v1.0.6) | 已有 release；IOKit 原始 USB bulk、热插拔与 AT 端口探测参考 | 需要 root，创建 PTY/symlink，启动时清理旧同名进程并改全局 ADB 环境；跳过 QMI 且不提供 PPP/IP | 只作固定版本参考；原版不直接进生产 |
| [TetherKit v0.1.5](https://github.com/XiaoMiku01/TetherKit/releases/tag/v0.1.5) | MIT；libusb RNDIS 状态机；macOS 13–26 与 USB 2.0 实测吞吐证据 | 原版创建 `feth`、DHCP 和宿主路由，违反“蜂窝 IP 不进入 macOS” | 只作后续 RNDIS USB 层参考，不运行原版网络侧 |
| [ocproxy](https://github.com/cernekee/ocproxy) | BSD；lwIP 用户态 SOCKS/端口转发架构，证明无需宿主路由 | 面向 OpenConnect `VPNFD`，不负责 Modem/PPP，内置 SOCKS 仅 TCP | 借鉴私有 socket bridge，不作为完整依赖 |
| [quectool](https://github.com/snowzach/quectool) | 有 release；提供移远 AT/SMS/射频管理 HTTP API 与 generic AT driver | 需要 ADB/root 并安装进可修改的 Modem 固件；不提供私有 TCP+UDP+DNS 数据面，也不通用于其他品牌/封闭固件 | 仅作控制面参考，不作为 macOS 数据依赖 |
| [lwIP STABLE-2_2_1_RELEASE](https://git.savannah.nongnu.org/cgit/lwip.git/tag/?h=STABLE-2_2_1_RELEASE) | BSD；成熟 PPPoS、PAP/CHAP、IPv4/IPv6、TCP、UDP、DNS 与 socket API | 是库而非可直接运行的 Modem Agent | 固定该候选版本验证最小私有数据面 |
| [libusb v1.0.30](https://github.com/libusb/libusb/releases/tag/v1.0.30) | LGPL；macOS/Windows/Linux 用户态 USB 与 hotplug API | 只提供 USB transport，不理解 Modem 或 IP；最终版本仍须经过 Darwin 热插拔/重枚举压力测试 | 固定候选，不在目标机编译 |
| WireGuard/gVisor/tun2socks | 用户态 TCP/IP 可借鉴 | 通常围绕 tunnel/TUN；若充当已暴露网卡外面的守卫，死亡后会泄漏；引入额外协议栈也不能解决 USB Modem 接入 | 第一版不引入 |
| USBDriverKit + Network Extension | Apple 支持的 USB system extension、TCP/UDP App Proxy 与 VPN 生命周期 | USBDriverKit 只解决 claim/transport，仍缺 Modem link 与 IP stack；Packet Tunnel 创建宿主虚拟接口；App Proxy 的 per-app 部署增加 entitlement、签名、用户批准及受管配置约束 | 只作未来强制 USB ownership hardening，不替代私有数据面 |
| On-Modem proxy | 在开放 Linux/ADB/root Modem 内运行时隔离边界很干净 | 需要修改并维护特定 Modem 固件；封闭固件、其他品牌及没有 ADB/root 的设备不可用 | 允许将来作为型号特定 Provider，不作跨品牌基线 |
| VM / Linux guest | 可复用 Linux ModemManager/netns | 仍需 USB passthrough、guest 驱动和私有 IPC；体积、升级、睡眠与重插复杂度远高于窄 companion | 不作为 macOS 产品依赖 |
| Gammu | 已有短信/通话 AT 状态机和当前项目适配 | 不提供安全蜂窝数据面；仍需要独占、稳定的 AT transport | 继续仅作 SMS/Call Provider 候选 |

结论：尚未找到一个可直接下载的成品同时满足“macOS 原始 USB Modem → 蜂窝数据 → TCP+UDP+DNS
私有出口 → 不创建宿主网卡”。因此第一版不得假装只需安装某个工具；也不得扩写完整网络平台。
允许的最小新增候选仅是一个窄职责 `mdd-cellular-io` companion：复用固定版本 libusb + lwIP，负责
`raw USB link <-> private IP stack`，向现有 Agent 提供最小 TCP/UDP dial API，不拥有 SIM、短信、
通话、页面、配置库或服务端协议。

## 4. 最小架构与安全不变量

```text
现有 ManagedAgentRuntime
  └─ DeviceSupervisor（reconcile：desired attachment set ↔ running contexts）
       ├─ PC/SC CardAgent（现有多 reader worker、热插拔与 VPCD）
       └─ ModemContext[ModemInstanceIdentity]
            ├─ SMS/Call Provider（现有领域接口；AT/Gammu/CoreAudio）
            └─ CellularDialBackend
                 └─ 每 Modem 一个 mdd-cellular-io child
                      ├─ RawUsbTransport（libusb/IOKit）
                      ├─ PrivatePppLink（独立 lwIP ppp_pcb/netif）
                      └─ 仅父 Agent 持有的 TCP/UDP dial 通道
```

- 现有网络代码不得把 macOS 私有数据路径接到原生 socket。必须先形成最小 seam：
  `resolve(name)`、`open_tcp(host, port)`、`open_udp()`、`close(handle)`；私有 Provider 的 DNS 在
  lwIP/PPP 内解析，禁止调用宿主 `getaddrinfo()`，companion 不可用时禁止回退宿主 socket。
- Agent 的 WSS 控制连接仍可走 Wi-Fi/Ethernet；只有被借用出口的 TCP/UDP/DNS 走上述私有 backend。
- companion 不监听公共 TCP/SOCKS 端口。Agent 通过启动时继承的匿名 socket/句柄与一次性能力
  通道访问；普通同用户进程没有可用代理入口。companion 不实现 SOCKS、WSS、重连、ICCID 或业务状态。
- 每个 ModemContext 最多一个 companion；一个 child crash/kill/USB removal 只撤销该 Modem 的 dial
  capability 和 session，不影响其他 Modem、reader 或 Agent 控制 WSS。不得用一个全局 lwIP/USB
  child 承载全部 Modem，使单点崩溃或阻塞扩散。
- 首版 EC20 只验证标准 PPP：原始 USB MODEM bulk function → `ATD*99#` → lwIP PPPoS。不得调用
  系统 `pppd`，不得创建系统网络服务、路由或 DNS。
- 数据连接只通过现有反向 WSS/稳定 ICCID 出口被服务端消费；companion 只提供 open/read/write/
  close 与 UDP send/receive，不复制 Agent SOCKS、WSS、重连或出口状态机。
- 对无法并发维持 SIM logical channel 与 PPP 的 raw-USB Modem，数据启用前由共享
  `cellular_active` 所有权门禁暂停该 Modem 自身的 VPCD APDU；ICCID/线路缓存仍可读，独立 PC/SC
  reader 不受影响。数据关闭后自动恢复 VPCD。该门禁按 transport 能力而非型号/运营商判断。
- 数据 Provider 资格门禁必须在冷启动、重插、硬件 reset、睡眠唤醒与 macOS 升级后验证：
  1) 当前 composition 不含 macOS 可绑定的 CDC/ECM/NCM/RNDIS/MBIM network function；或
  2) 将来另有已独立验证、可持续且可逆的禁用机制。
  两者均不能证明时报告 `isolation_not_proven`，不得启用数据。当前实测 `2c7c:0125` 全 vendor
  function 只证明这一台硬件/这一版 macOS 的候选资格，不能推导全部 EC20 或 4G/5G 模块安全。
- 启动、运行、睡眠唤醒、拔插、Agent 退出和重启后都必须枚举验证；出现宿主蜂窝 interface、
  Network Service、route 或 DNS 即撤销私有 endpoint、停止数据并报告 `isolation_not_proven`。
- Agent/companion 崩溃、USB 断开、PPP 失效或安全验证失败时，服务端稳定入口立即拒绝连接，不回退
  Wi-Fi/Ethernet，也不自动重放短信或拨号。
- Root 用户主动刷固件、恢复出厂设置、安装第三方 USB 网络驱动或重新配置模块超出普通应用威胁
  边界；产品必须在下一次 Agent 启动时检测并阻止，但不能声称在无常驻系统扩展时抵抗恶意 root。
- companion crash：Agent 原子撤销 endpoint，所有 session 消失且没有原生 socket fallback。
  Agent 正常退出：继承 IPC 关闭并在有界时间内清理。Agent `kill -9`：companion 必须用
  父存活管道或 `kqueue EVFILT_PROC` watchdog 检测父死亡：父仅持写端，child 仅继承读端；spawn 时
  显式传递这一个监视 FD 并关闭双方多余副本，其余未授权 FD 全部 `CLOEXEC`，child 以 EOF 判定
  父死亡。随后关闭 dial/PPP、释放 USB 并有界退出，不能只假设业务读循环最终读到 EOF。挂死/孤儿
  恢复按 lease + session generation，禁止 kill-by-name。
- USB hotplug 事件只作 reconcile 触发器，不是真相源。libusb 允许 `ENUMERATE` 与事件队列产生重复
  arrival，也可能看到没有对应 arrival 的 removal；DeviceSupervisor 必须重新枚举完整 attachment set，
  以 physical identity + generation 幂等创建/停止 context，忽略迟到旧事件和旧 child 回写。

## 5. 统一宿主、配置与本地控制边界

- `AgentHost.acquire_installation_lease()` 必须先于 ConfigStore、日志、GUI、PC/SC 和 libusb 初始化。
  lease 使用固定安装路径；父目录 `0700`、文件 `0600`，以 `O_NOFOLLOW|O_CREAT` 打开并校验 owner、
  类型和 inode，禁止可预测 `/tmp` lock。重复 host 稳定退出 `9`；doctor/status 等查询不拿硬件 lease。
- CLI `mdd-agent run` 与菜单栏 GUI 都只是 `AgentHost` 入口，内部只能各创建一个相同的
  `ManagedAgentRuntime`。已有 CLI 或 GUI host 时，任何第二个 CLI/GUI host 都必须在启动权限 UI、
  GUI 与硬件前以固定退出码 `9` 拒绝启动；状态窗口始终通过同一本地控制合约查询当前 GUI host，
  不形成第二个 runtime。GUI 显式退出才停止它持有的 runtime。首版不做 launchd，因此重启后不会
  自动启动，文档必须如实说明。
- macOS 配置使用稳定、短且用户私有的 Application Support 路径，不回退 `/tmp`。本地 Unix socket
  的父目录 `0700`、socket `0600`，不能无条件 unlink 不属于当前安装/lease 的 socket。首版若同一
  登录用户拥有完整本地控制权限，必须明确写出；Token 与设置进入 owner-only `config.json`，GUI 不
  依赖 Keychain。配置必须优先于 CLI 参数/标准输入和环境变量，所有状态/控制响应必须脱敏 Token。

## 6. Provider、设备身份与并发边界

- 身份模型严格分三层，禁止互相替代：
  1. **载体/attachment**：PC/SC reader、EC20/4G/5G Modem、USB function 和 Agent 只是当前接入点；
     IMEI、USB parent/path、PC/SC 名称用于发现、独占、路由命令和诊断，不拥有 SIM 业务配置。
  2. **安全元件/卡体**：普通可拔插 USIM 以 ICCID 为稳定身份；eUICC/eSIM 卡体以 EID 为稳定身份。
     eSIM reader 中插普通 USIM 仍是 ICCID 卡，不因载体类型伪装成 eUICC。
  3. **eSIM Profile**：每个 Profile 以 ICCID 为稳定身份并从属于一个 EID；启用 Profile 改变当前
     订阅，但不改变 eUICC 卡体身份。空 Profile 的 eUICC 仍由 EID 稳定保存和恢复。
- EC20/其他 Modem 插普通 USIM 时，线路、号码、短信、通话和出口配置跟随 ICCID；插 eUICC 时，
  卡体与 Profile 列表跟随 EID，具体线路/订阅跟随当前 Profile ICCID。Modem IMEI 只保存硬件能力、
  固件、音频和设备策略，不得覆盖或冒充 EID/ICCID。
- VPCD slot 只是惰性分配、可恢复且可回收的 transport address：在线 attachment 优先复用原 endpoint
  或同一 EID/ICCID 的离线槽；否则分配空槽，容量满时只覆盖最老离线槽。slot 变化、reader 更换、
  Modem 更换或 Agent 迁移都不得删除/重建 EID、Profile ICCID、USIM ICCID、线路和历史数据。
- 同一 EID/ICCID 再次出现时复用既有卡/线路对象并更新当前 attachment；若同一稳定卡身份被两个
  live attachment 同时报送，必须标记 `card_identity_conflict` 并阻止有副作用的 APDU/Profile 操作，
  不能静默合并到错误载体，也不能复制业务对象。
- `RawUsbSerialProvider`：按 descriptor 与探测识别 AT/MODEM function；同一 physical identity 下每个
  function 只能一个 owner。名称、USB interface number、`/dev/cu.*` 和枚举顺序都不能作为稳定身份。
- `PrivatePppProvider`：首版数据 Provider，也是当前 EC20 的唯一数据 PoC；实现只依赖标准 PPP 与
  厂商最小拨号覆盖，不向业务层暴露 USB 编号。
- `ModemInstanceIdentity`：优先使用可验证的 USB registry parent/location + serial，再由探测出的 IMEI
  绑定业务身份；IMEI 未读出前仍须保持 attachment 独立。每个实例独享 transport、PPP、dial session、
  control session、reconnect generation 与 lease；一个 Modem 的拔插或故障不得重启其他实例。
- reader endpoint 与 SIM 业务身份必须解耦。当前可寻址 endpoint 继续使用 PC/SC Resource
  Manager 返回的完整 reader 名称；同型号设备由资源管理器附加的 reader/LUN/slot 单元号区分，现有
  supervisor 已据此为每个 reader 建独立 worker。名称 hash 只是 `agent_id` 内的 endpoint/槽位复用
  hint，不承载 SIM 配置。持久 SIM/eSIM 线路仍由服务端按 EID/ICCID 匹配；reader 换 USB 口、名称或
  VPCD 槽位后，卡身份出现即复用原业务数据，并只解除或 tombstone 旧 attachment/endpoint 关联；
  绝不删除卡体、Profile、线路、短信或历史业务记录。USB serial、IORegistry parent/path 只能作为
  可选诊断/加速 hint，不能成为无序列号读卡器的阻塞条件，也不能取代 EID/ICCID。
- 如果底层驱动真的向 `SCardListReaders` 返回两个完全相同的字符串，它们在 PC/SC API 本身就不可分别
  寻址，不属于 MDD 身份策略可猜测解决的问题；doctor 应报告 PC/SC provider/driver 冲突并持续重扫，
  不能把正常的两个同型号、不同 PC/SC 单元号 reader 误报成歧义。
- Future Work：`PrivateRndisProvider`、`PrivateQmiProvider`、`PrivateMbimProvider`。它们不进入首个
  PPP 产品版本；Linux 工具或协议代码存在不等于 macOS raw-data 已有成品。
- `PcscProvider`、`SmsBackend`、`CallBackend`、`CallAudioBackend` 继续复用现有实现与服务端合约；
  不因数据 Provider 不支持就隐藏独立可用的读卡器、短信或音频能力。

## 7. 实施门槛与顺序

### M0 — 前置评审（2026-08-21 完整基线 PASS）

- [x] 独立评审确认：未发现更小且满足 TCP+UDP、无宿主接口、进程死亡 fail-closed 的可直接依赖。
- [x] 独立评审确认：`mdd-cellular-io` 的职责没有扩成第二个 Agent，安全不变量可自动验证。
- [x] 前置评审在两轮 `NEEDS CHANGES` 修订后返回 `PASS`；评审期间未编写 companion、未改 Agent、
  未修改目标 EC20 配置。实现仍须按 M1→M4 顺序推进，不能把设计 PASS 当成交付完成。
- [x] 卡体/Profile 身份模型、每 Modem 独立 child、多设备 supervisor、热插拔 reconcile 和跨平台
  contract-conformance 矩阵经补充复核后返回 `PASS`；设计评审完成不等于实现或实机验收完成。
- [x] 按用户要求增加“对端先调通、后写项目代码”的 M0.5；污染控制、停止门禁和探针边界经再次
  独立复核返回 `PASS`。目标基线核对可开始，产品实现仍须等待 M0.5 实机 PASS。

### M0.5 — 目标 Mac 一次性私有数据探针（项目代码前强制门禁）

- [x] 在项目 Git 工作树外建立可整体丢弃的窄探针源码目录，只包含固定候选版本 libusb + lwIP、
  RawUsb PPP transport、私有 DNS/TCP/UDP 自测和父死亡清理；记录源码快照、许可证、版本、构建命令
  和 SHA-256。探针不复用或改写 Agent 产品代码，成功后只把已证明的最小边界重新实现进项目。
- [x] 探针在本地受控构建环境或 CI 预编译；目标 Mac 只接收带哈希的预构建产物，禁止在目标机安装
  Homebrew/pkg/开发环境或编译。目标机使用唯一临时目录，所有文件和 PID 建清单；清理只能按该清单，
  禁止 kill-by-name 或删除目录外文件。
- [x] 探针必须由 lwIP 自身完成 PPP、私有 DNS、TCP 和 UDP 往返，禁止用系统 `pppd`、TUN/utun、
  宿主 `curl`/`nc`/`getaddrinfo()`/socket 代替私有数据面证据。不得监听公共 TCP/UDP/SOCKS 端口。
- [x] PoC 期间不启动 `ManagedAgentRuntime`、`ModemControl`、`embedded_socks` 或反向 WSS；只允许运行
  独立探针及既有 PC/SC reader supervisor。禁止短信、通话、Profile 写入和其他收费操作。
- [x] 禁止 launchd/System Extension/DriverKit、PTY/symlink、PF、route 修改、任何
  `networksetup -set*`、系统 DNS、USB composition、固件或持久 AT/Profile 修改。探针只可临时 claim
  已由只读 descriptor 明确识别的 EC20 MODEM bulk function；需要猜 interface 或抢占未知 owner 时停止。
- [x] 运行前、PPP 运行中、正常退出、父进程 `kill -9`、探针 `kill -9` 和 EC20 拔插后分别保存只读
  快照：USB composition/driver ownership、interfaces、Network Services、routes、DNS、相关进程和探针
  临时文件。任何宿主网络变化、公共 listener、native fallback、无法完全回收或旧 session 存活均失败。
- [x] 保持多个现有 PC/SC/eSIM reader 在线，验证独立 ATR/APDU/EID/ICCID 与 reader 热插拔不受 EC20
  PPP claim 影响。EC20 RF 数据测试前确认 USB 供电余量；出现反复重枚举、掉电或供电证据不足立即停止，
  不以循环重试掩盖。
- [x] M0.5 只有在目标 Mac 上取得 TCP、UDP、DNS、退出 fail-closed、多 reader 不受影响和零系统污染
  的完整证据后才能 PASS。此前不得修改 Agent、公共协议、GUI 或 companion 产品代码；探针失败则回到
  依赖/架构研究，不把失败路径集成进项目。

#### M0.5 实机证据与剩余门禁（2026-08-21）

- [x] 探针只存在于项目 Git 工作树外；目标 Mac 仅接收 arm64 预编译产物，未安装 Homebrew、开发包、
  launchd、System Extension 或虚拟网卡。固定源码为 libusb `1.0.30`（release archive SHA-256
  `fea36f34f9156400209595e300840767ab1a385ede1dc7ee893015aea9c6dbaf`）和 lwIP
  `STABLE-2_2_1_RELEASE`（tag archive SHA-256
  `ce0b7461c0ad9602c376f0bf07c5eb7253b48c7bf66f011c6bf3e2a96731c539`）。
- [x] 只读 descriptor 证明确切 composition：EC20 vendor interfaces 0–4 均无系统 owner，interface 2
  与 3 支持 AT，interface 3 是 PPP data function；interfaces 5–7 由 AppleUSBAudio 使用，探针未 claim。
  未创建串口 PTY、TUN/utun、Network Service 或宿主 socket。
- [x] 同一 lwIP 实例实机完成 PPP、运营商下发 DNS、`example.com` 解析、HTTP TCP 和向协商 DNS 的
  独立 UDP DNS 往返。首次向 `1.1.1.1:53` 的独立 UDP 被当前运营商路径丢弃，探针没有误判或循环重拨；
  改用 PPP 协商 DNS 后一次通过。拨号使用标准 `AT+CGDATA="PPP",1`，不写 PDP/Profile。
- [x] 正常退出与父宿主 `kill -9` 时，子进程有界关闭 PPP、挂断并 release；探针自身 `kill -9` 时，
  OS 立即释放 USB claim，宿主仍无蜂窝 interface、route、DNS、Network Service、listener 或 native
  fallback。下一 owner 检测到 data function 仍在 online-data 后，通过同一 function 的 V.250 guard-time
  `+++` + `ATH` 恢复 AT。独立 control function 上的 `ATH` 在此固件返回 `OK` 但不能退出 data function，
  因此不得把 control-only hangup 当成已恢复。
- [x] 首版恢复策略经证据复核确定为：有界 escape/hangup 并验证 command mode；失败就 release、撤销出口并
  报 `unhealthy/recovery_required`，禁止 native fallback、无限重试和默认整设备 USB reset。硬件 reset
  会连带音频/控制 function 与整机重枚举，只能作为以后显式授权且单独验收的可选能力。
- [x] 物理门禁完成：目标机同时枚举 2 个同型号 PC/SC reader；一个插普通香港 USIM，另一个插空白
  eUICC（EID `89086030202200000026000012391349`、Profile 列表为空）。两者保持独立 ATR/APDU/
  ICCID/EID，并在 PPP 与异常退出门禁期间无串卡。
- [x] 分别实拔插 EC20 和两个 reader：另一 reader 持续工作，服务端无幽灵 session，重插后复用各自
  endpoint；EC20 重插后重新枚举、私有 PPP/DNS/TCP/UDP 再次通过，宿主网络快照哈希保持不变。
- [x] 最新独立前置复核结论为 `PASS`。只允许 DNS/幂等 UDP 自测做有界重试；通用 UDP relay 不得
  透明重放。供电仍作为 doctor 风险提示，不能把一次稳定运行扩大宣称为任意 Hub 均有峰值余量。

### M1 — 统一宿主与读卡器

- [x] CLI 与 GUI 复用同一个 `ManagedAgentRuntime`、owner-only 配置、日志和本地控制合约。GUI
  默认从配置读取 Token；CLI 在配置缺失时支持 `--token`、`--token-stdin`、`MDD_AGENT_TOKEN`
  临时回退，且不得覆盖已保存配置；`config set token --stdin` 与 GUI 永久写入同一配置。GUI 与
  本地 CLI 每次启动检查并请求麦克风权限，授权后仅重检音频并通过当前 WSS 心跳动态更新能力，
  不重启其他设备。无桌面 SSH 无法强制 macOS 显示 TCC 时必须明确报出，不能误报已授权。
- [x] 实现上述安装级 lease；重复 CLI/GUI 或混合启动在访问配置与硬件前退出 `9`；`nohup` 宿主已
  实机验证在 SSH 退出后继续运行。
- [x] 公共 runtime 状态从单数 `_modem`/`modem` 改为按 `ModemInstanceIdentity` 索引的 contexts；
  本地控制协议发布 `modems[]`，迁移期只保留明确废弃的单 Modem compatibility view。即使 PoC 只插
  一台，也禁止把单数假设继续写入 Provider、WSS session、配置或 GUI。
- [x] 把现有 macOS 多 PC/SC/eSIM reader worker 与热插拔能力迁入统一宿主，保持已有双 reader 实机
  能力。两个同型号 reader、插卡/拔卡、分别拔插、交换 USB 口和 Agent 重启后不得串 ATR/APDU/EID/
  ICCID/线路数据；VPCD transport 槽位允许变化，业务配置按“eUICC EID → Profile ICCID”或普通
  USIM ICCID 恢复。本轮实测覆盖同型号双 reader、reader 普通卡、空 eUICC、EC20 普通卡；EC20
  插 eUICC 尚无本轮实物证据，不能据此标成已验。

### M2 — 私有蜂窝数据 PoC

- [x] 先做独立本机工具验证，不连接页面/服务端：资格门禁、raw USB PPP 建链、TCP、UDP、私有 DNS、
  重连以及父/子进程故障清理；未通过前不改现有 Agent 网络业务代码。
- [x] PoC 前后及运行中均证明 macOS 没有新增蜂窝 interface、Network Service、route 或 DNS；正常
  退出、父进程 `kill -9`、companion `kill -9` 与自动恢复后快照哈希均保持一致。
- [x] PoC 通过后接入 `CellularDialBackend` seam 和现有反向 WSS；未新增 macOS 专用业务 API，
  不得在 companion 内再实现 SOCKS/WSS。
- [x] 产品链路由服务端持久 desired state 自动拉起，在一次 PPP 失败后释放唯一 lwIP PPP 控制块并
  自动重试；反向 SOCKS 实测 HTTP 200，连续 90 秒保持 `connected/data_active/ready`。期间宿主
  interface、Network Service 与 DNS 哈希保持基线不变，未发生 native fallback。

### M3 — Modem 控制、短信与通话（不阻塞 M2 隔离 PoC）

- [x] 原始 USB transport 在不创建 PTY/全局 symlink 的情况下完成 AT 探测、IMEI/ICCID/IMSI、注册、
  信号、漫游、APN 与状态维护。
- [ ] 复用现有 SMS/Call 领域合约已验证列表与状态读取；本轮未重复发送收费短信、未拨号，也未重做
  来电接听，因此发送/接收/通话端到端保持待人工授权验收。任何 unknown 均不自动重试。
- [x] 通用 AT transport（含 macOS private raw-USB）复用现有 UICC、SMS bearer 与 voice registration
  maintainer 发布运行态就绪事实；服务端实机显示 Cellular/SMS/Call 均 `actual=on`，该门禁只读且
  不发送短信、不拨号，不能替代上一项收费端到端验收。
- [x] CoreAudio 按同一 physical USB identity 选择 Modem UAC，不按名称或默认音频设备猜测；当前
  EC20 的播放与采集端点匹配及非收费音频自检已通过。

### M4 — 产品化与发布

- [x] 已固定第三方版本、许可证、源码与校验，并生成无需目标机编译/安装 Python、Gammu、libusb、
  lwIP 开发环境的 arm64 CLI/App；M5 PASS 后已用最终源码重建，ad-hoc 深度签名校验通过。
- [ ] universal、Developer ID 签名、公证与 CI release 尚未完成。
- [x] CLI、GUI app、图标、菜单栏、日志、doctor、配置与卸载文档完成；GUI 关闭窗口隐藏，只有菜单
  “退出 MDD Agent”停止运行时。最终 arm64 包已在 M5 PASS 后重建并校验。
- [ ] runtime snapshot、配置、控制 session、数据 backend 与设备列表已由单数 `modem` 改为按
  `ModemInstanceIdentity` 索引的集合；至少 2 个 Modem 同时在线，分别插拔、换卡、断线和恢复时
  互不影响仍缺第二台物理 Modem 验证。首个 PoC 的单 Modem 限制不得泄漏进公共领域合约。
- [ ] 至少当前 EC20+多 PC/SC Mac 与另一品牌/协议 Modem 交叉验证；没有第二种硬件时只能标注已验
  型号，不宣称通用 4G/5G 数据已完成，也不能把“可实例化”写成“多 Modem 已验收”。

### M5 — 实现后独立复审

- [x] 另一独立评审任务只读审查实际 diff、第三方边界、打包产物与全部实机证据；多轮
  `NEEDS CHANGES` 的 fail-closed、宿主租约、并发状态与 PPP/APDU 所有权问题修正后返回 `PASS`；
  随后的通用 AT SMS/Call 就绪状态补丁也由同一任务追加只读复审并再次返回 `PASS`。
- [x] 复审 `PASS` 后才重建最终 arm64 CLI/App、部署最新源码到目标 Mac 并重新执行实机门禁；
  未提交里程碑，且 universal/多 Modem/另一品牌等未验证项继续保持未完成。

## 8. 必须自动化的验收矩阵

- CLI 前台、CLI `nohup`、GUI、CLI+GUI、两个 CLI、两个 GUI：只有一个设备 runtime，所有状态一致。
- Agent 正常退出、Agent `kill -9`、companion crash/hang/orphan、USB 拔插/硬 reset、睡眠唤醒、
  冷启动、Mac 重启与系统升级：父死亡后 companion 有界退出，lease/session generation 可恢复，
  稳定 ICCID/reader identity 可恢复，服务端旧 session 不能回写。
- 重放重复 arrival、无先行 arrival 的 removal、迟到 removal、同 USB path generation 变化，以及
  两个 Modem 同时插拔；reconcile 结果必须与最终枚举集合一致，且每个 physical identity 最多一个 context。
- Wi-Fi/Ethernet 与蜂窝同时在线时，MDD TCP/UDP/DNS 通过；普通 socket、显式接口绑定、浏览器、
  Tailscale、EasyTier、ZeroTier 均不能发现或使用蜂窝出口。
- 运行中和退出后检查 `ifconfig`、`networksetup -listallnetworkservices`、route table、DNS、系统网络
  流量计数；任何蜂窝宿主路径出现都视为失败。
- 单 Modem PoC 先覆盖多读卡器、同型号 reader、空 eUICC profile、普通 USIM、换卡、PIN、无 SIM、
  无音频和无数据能力；产品验收再加入至少 2 个并发 Modem，所有组合均不得串状态或制造幽灵设备。
- TCP 长连接、UDP 会话、DNS cache、断线半开、服务端不可达、Agent 默认网络仍在线时全部
  fail-closed；不得从默认网络泄漏。
- 私有路径测试必须证明没有调用宿主 `getaddrinfo()`，companion 缺失/失败时没有 native socket
  fallback。另一个进程主动 raw USB claim 的结果作为上述威胁边界证据记录，不冒充普通网络泄漏测试。
- 建立一套共享 contract-conformance fixtures，分别对 Windows SCM host 与 macOS AgentHost 执行相同
  CLI 命令、退出码、控制方法以及 `modems[]`/reader/SIM 状态 schema；只有 `host_mode`、`autostart`、
  `session_scope`、`isolation_backend`、`approval_state` 允许按平台不同，其余差异均视为回归。

## 9. 当前判定

- 安全上，PF/WireGuard/gVisor 包在宿主蜂窝接口外不是可接受边界；私有 link + 私有 IP stack 才能
  使进程死亡自然断路。
- 复杂度上，不建设完整网络平台；新增面被限制为一个无业务状态、无公共监听、只负责 PPP 与
  TCP/UDP dial 的 companion。若独立评审找到满足全部不变量的更小成品，应删除该 companion 计划并
  直接采用成品 Adapter。
- 当前已完成调研、前置评审、工作树外探针、统一宿主、私有数据 backend、当前 EC20 与同型号双
  PC/SC reader 实机门禁及 M5 独立复审。复审后源码已部署到目标 Mac 临时验证目录并再次证明：
  EC20 数据在线、两个 reader 独立在线、空白 eUICC 返回稳定 EID 且 `profiles=[]`、重复 CLI 固定
  `exit 9`、宿主 interface/DNS/硬件端口哈希不变。最终 arm64 CLI/App 已重建；多 Modem、另一品牌、
  universal 签名/公证及收费 SMS/Call 端到端仍保持未完成。

## 10. Windows / macOS 等价边界

以下必须完全等价：CLI/GUI 控制合约、退出码、单一 runtime/配置/状态源、status/devices/doctor/logs/
config/reconnect/self-test、Provider 能力语义、设备/SIM/reader/短信/通话/数据出口领域状态，以及
“GUI/CLI 不创建第二个硬件 owner”。

底层实现只能做到能力等价，不能伪装成相同机制：

| 领域 | Windows | macOS 首版 |
| --- | --- | --- |
| Host 生命周期 | SCM machine service，可开机启动与恢复 | 登录用户 `AgentHost`；无 launchd 时不自动启动 |
| 蜂窝控制 | MBN/系统 profile + 辅助 AT Provider | raw USB Provider + 私有 PPPoS |
| 流量隔离 | WFP 保护宿主蜂窝 interface | 蜂窝 IP 不进入宿主网络栈 |
| 凭据 | machine-scoped DPAPI/受保护安装 ACL | 当前用户 `0700/0600` 配置文件 |
| GUI/音频 | SCM 在 Session 0，GUI/音频必须用户会话 IPC | 首版 GUI host 与 CoreAudio 同属登录用户 |
| 安装授权 | SCM/WFP/驱动通常一次 UAC | 用户态首版无对应守卫；DriverKit 才需要 entitlement/system extension 批准 |

公共状态必须如实暴露平台差异，不让页面猜测：`host_mode`、`autostart`、`session_scope`、
`isolation_backend`、`approval_state`。macOS 的 start/stop/status 操作针对抽象 `AgentHost`；没有
launchd 时不得显示成“已安装系统服务”。
