# 当前恢复任务：唯一执行游标

## 2026-08-29：Go 分层运行时重构（当前主任務，第四十六批已验证、未接管生产）

用户已将方向从 Python 渐进修补改为 Go 分层重构；本节覆盖下方“状态事实收敛”中“不做全量
重写”的旧决策。生产仍保留当前现场作回退证据，新 Go 运行时在真实验收前不得接管付费呼叫、
短信、SIM 写操作或宿主网络。

已完成的研究与第一批实现：

- 盘点当前 Python/shell/PowerShell/前端运行时代码约 8.4 万行，其中 `control/app/main.py`
  约 13,900 行、`engine/swu_ike.py` 约 6,700 行。2026-08-28 的真实 giffgaff 现场再次证明
  `reg_unanswered` 能越权触发 Control 停止并替换整个 Engine，注册层与进程生命周期必须拆开。
- 调研并本地完整测试 `boa-z/vowifi-go` commit
  `1e9c6e6adbfcd9667695149d5ecb0f71cd062f07`，所有 Go package 通过。该库覆盖 SWu/EAP-AKA、
  IMS-AKA/SIP、短信和语音媒体，但维护者明确标注真实设备/运营商/生产未验证；无稳定 tag 且为
  AGPL-3.0，是否采用属于后续必须确认的许可证/依赖决策。`VoHive` 约 10.5 万 Go 行，只作架构与
  硬件实现参考，不整体搬入 MDD。
- 核实 gVisor netstack 和 WireGuard Go 的内存网络模式：SWu 解密后的 IP packet 可以直接进入
  用户态 TCP/IP 栈，由 `net.Conn`/`net.PacketConn` 承载 IMS DNS/SIP/RTP；目标方案不需要内层
  TUN、默认路由、策略路由、容器 IP 或用户确认浏览器访问 IP。
- 新增 `go-runtime` 的依赖无关核心：分层事实 reducer、权威 owner、单调 epoch/sequence、服务端
  ReceivedAt 时效、操作级 readiness、同进程指数退避（运营商 Retry-After 原值优先），以及独立
  的 10 秒浏览器心跳精确通话挂断守卫。恢复 action 类型不允许 process/container restart；
  注册、隧道或页面展示状态无法进入通话守卫输入。
- 验证：候选协议库 `go test ./...` 全通过；MDD `go-runtime` 的 `go test -race ./...`、
  `go vet ./...` 和 `git diff --check` 全通过。第一次测试曾发现默认恢复动作零值缺陷，已修复后
  重新全量通过，未隐瞒首次失败。
- 第二批新增只读 `mdd-shadow`：只接受已经保存的 `/api/snapshot` JSON 文件，没有 URL、token、
  通话、短信、硬件或恢复入口。legacy adapter 忽略 `Working` 等展示标签，只翻译 machine fact
  与设备明确能力；蜂窝数据、语音、短信已拆成三个独立层，数据正常不会推导语音/短信正常。
  Shadow 记录已见代际，切到新 Engine/card generation 后，迟到的已见旧代际不能覆盖当前事实。
- 第二批回归：`go test -race ./...`、`go vet ./...`、`git diff --check` 全通过；CLI 使用合成快照
  冒烟确认蜂窝通话为 ready 时，IMS `sip_rejected` 的 VoWiFi 通话仍为 blocked，展示标签没有
  覆盖机器事实。未读取生产、未部署、未拨号、未发短信。
- 第三批新增直接 Go event/record contract、服务端 epoch recorder、统一 operation catalog 与只读
  `mdd-replay`。每层只有 `mdd-core`/`mdd-agent`/`mdd-vowifi` 中一个 owner；Core 必须先显式授权
  精确的 line/role/producer/generation，旧 Agent 或旧 VoWiFi 进程不能靠新 sequence/generation 把
  自己重新声明为当前。Producer 不再自行声明全局 epoch，避免设备替换后双方 counter 都从 1
  开始造成冲突。machine condition/code 在持久化前校验，展示文本不能进入状态字段。
- 第三批回归再次通过 `go test -race ./...`、`go vet ./...`、`git diff --check`，并同时执行
  `mdd-replay` 与 `mdd-shadow` CLI 冒烟：直接事件只有蜂窝数据事实时，语音与短信仍为 blocked；
  legacy 快照的蜂窝语音和 VoWiFi/IMS 结果仍保持独立。未连接生产或任何 Agent。
- 第四批新增首个可运行只读 `mdd-core`：强制只监听 loopback，仅提供 health/线路列表/单线路
  typed facts API，没有写入、恢复、通话或短信路由。每次请求按当前时间重新投影，TTL 到期会变成
  unknown，不靠 timer 改写状态。真实编译进程冒烟已请求 API，并以 SIGINT 有界退出，最终 exit=0；
  修复了第一次 `go run` 被信号停止时 exit=1、可能被服务管理器误判为 crash 的问题。
- 持久化核对了 Go `os.File.Sync`、SQLite WAL/atomic commit 和 bbolt v1.5.0。自制 NDJSON append
  不能只靠 Sync 宣称断电原子；NDJSON 只作 replay/export。用户已确认采用纯 Go/MIT bbolt；其
  Linux ext4 fast-commit 风险必须保留为部署 preflight，不能误报断电安全。事务实现列为下一批。
- 第五批新增 provider-neutral 用户态 VoWiFi 网络边界：外层 ePDG packet dialer 与内层解密 IP
  packet session 分开；IMS 只能取得进程内 `DialContext`/`ListenPacket`/`LookupNetIP`，没有 TUN、
  interface、host route 或 namespace API。SIM AKA 只经过 Agent authenticator，不向 Core 暴露可复用
  SIM secret。ePDG → userspace stack → IMS 是一次有界尝试；任一阶段失败只逆序释放本次资源并返回
  精确 stage，不重试、不重启进程。`go test -race ./...`、`go vet ./...`、`git diff --check` 通过。
- 第六批新增统一 Agent runtime Controller，作为未来 Windows service、CLI、GUI/tray 的唯一行为
  owner。重复启动在 starting/running/stopping 均返回 conflict；意外退出只进入 failed、不自动重启；
  人工重启产生恰好一个新 generation；停止超时保持 stopping 并拒绝替代实例，直到旧 worker 实际
  退出，避免两个进程同时占有 modem/reader。Controller 不含服务管理、GUI、PC/SC 或 modem 逻辑，
  这些后续只能作为 adapter。竞态测试覆盖重复启动、异常退出、人工恢复和 hung stop。
- 第七批新增共享 Agent 本地 management API/Client：固定 literal loopback 端口的独占 bind 是跨
  进程 singleton，service host 与 GUI host 不能同时占有；status/start/stop 只调用同一 Controller。
  Bearer token 以固定长度 SHA-256 作 constant-time compare，401 只返回 machine code、不回显 token；
  Client 拒绝 DNS 名、非 loopback 和 HTTPS endpoint，避免 token 因 hosts/DNS 改写发往远端。集成测试
  通过真实 httptest HTTP 链验证一套 Client 的停止/启动/重复冲突，以及第二 listener 不能绑定。
- 第八批新增可取消 PC/SC attachment monitor 和独立 reader-session reconciler：使用已核实为最新
  revision 的 `github.com/ebfe/scard`，Windows 直接调用 WinSCard、macOS 调用系统 PCSC framework。
  已知 reader 通过 `SCardGetStatusChange` 等待、context 取消通过 `SCardCancel` 解除；无 reader 时
  有界重枚举以发现新设备。拔除只取消对应 session，插入只启动对应 session；同 ATR 重插或 PC/SC
  card-event generation 变化都会替换旧 session。相同型号 reader 由 PC/SC attachment name 临时区分，
  该名称不作为卡身份；EID/ICCID/profile 仍由 session 上层读取。session/monitor 故障只在同进程按共享
  指数退避重试，不重启 Agent。race/vet 测试覆盖热插拔、同 ATR 重插、双同型 reader、timeout 和取消。
- 用户已确认原生 VoWiFi 采用隔离的 AGPL `boa-z/vowifi-go` provider，固定已测试 upstream commit，
  不搬入 VoHive 控制面；许可证与对应源代码义务必须随 provider 交付保留。
- 第九批采用已联网核实为最新版的 bbolt v1.5.0，实现事务 event store。可信 Core 的 `Activate`
  在一个 write transaction 内替换 line/role producer binding、分配 layer epoch 并追加首条 record；
  producer 后续只能用 `Accept` 写入精确匹配的 durable binding。EventID 精确重试返回原 record，内容
  冲突会拒绝；已被替换的 generation 禁止重新激活。关闭重开后 binding/epoch/seen generation 均恢复，
  commit 顺序可通过同一 reducer 重放并导出 NDJSON。数据库强制 0600、Unix 首次创建后同步父目录、
  未知 schema fail closed，bbolt mmap slice 不逃出 transaction。24 concurrent writers、事务回滚、
  restart/replay/export/idempotency/schema/file-mode 的 race tests 与全量 Go vet 均通过。ext4
  `fast_commit` 限制仍是部署 preflight，未伪装成代码已消除。
- 第十批建立独立 AGPL module `providers/vowifi-go`，固定 pseudo-version
  `v0.0.0-20260709161034-1e9c6e6adbfc` / exact commit
  `1e9c6e6adbfcd9667695149d5ecb0f71cd062f07`，Core module 不 import。adapter 使用真实 upstream
  IKE manager 类型并强制 userspace dataplane，只接受 ready `PacketTunnelReadSession`；kernel、TUN-only、
  incomplete 与 canceled open 都以有界 context 关闭后拒绝。packet/DNS slice 均复制，SIM AKA 只由注入
  provider 提供。fake SIM/tunnel lifecycle、真实 constructor compile、race/vet/module verify 全通过，未
  访问 APDU/网络、未发短信、未拨号。该批当时只有 SWu packet adapter；后续第十一、十二批已补
  in-process IP stack 与 IMS registration binding，但 service/IPC、SMS/voice 仍未完成，不得宣称原生
  VoWiFi 可用。完整上游 AGPL license/source 已纳入隔离模块，正式分发/部署仍须保留对应源码交付。
- 第十一批完成纯用户态 IP stack。联网核对当前依赖后排除已停止维护的 `google/netstack`、耦合整个
  Tailscale 控制面的集成层和较旧的 noisysockets fork；采用最新已核实 WireGuard-Go
  `v0.0.0-20260522210424-ecfc5a8d5446` 的 MIT `tun/netstack` 薄封装（底层固定可编译 gVisor）。独立
  Go 1.26.3 probe 与正式 module 均确认 `Device.File()==nil`，不会创建 OS TUN。新增双向 SWu packet
  pump、TCP dial/listen、UDP dial/listen、DNS lookup、精确 pump direction error 和单 stack 有界关闭；
  没有 process/container restart。两套 linked fake SWu session 已真实完成 TCP echo、UDP 往返、DNS A
  query，race/vet/module verify 全通过。首轮仅有测试文件 unused import 编译失败，删除后重跑全绿，
  未把首次失败隐藏；Linux/amd64 `CGO_ENABLED=0` 交叉编译确认生成静态 ELF。未访问宿主网络、APDU、
  短信或通话。
- 第十二批完成用户态 IMS 注册接线。联网确认 `boa-z/vowifi-go` HEAD 仍是固定的 exact commit；其
  registrar 有 transport factory，但 SIP flow/DNS 最终仍硬编码宿主 `net.Dialer`。隔离 provider 因此
  纳入完整上游 AGPL 源码快照，仅增加可选标准 `DialContext` seam 并从 registrar 传到 REGISTER、
  request、persistent flow 和 prepared P-CSCF DNS；nil 时上游默认行为不变。MDD `internal/ims` 只注入
  SWu in-memory stack，拒绝来源不明的 custom transport/resolver、LocalAddr 和 security installer，
  不回落宿主网络。fake P-CSCF 已经通过 SWu 内存 DNS 解析 FQDN，真实接收 REGISTER/200，并在 Close
  时接收第二个 `REGISTER Expires: 0`。provider race/vet/module verify、上游 fmt/tidy/vet/smoke/full
  tests 均通过。上游 compat selftest 因其脚本使用 Bash 4 `mapfile`、本机仅 Bash 3.2 而未执行完成，
  已如实记录，未误报 PASS。首次编译因新增 runtimehost 间接依赖缺少主模块 go.sum 而失败，按上游
  锁定版本 tidy 后通过；DNS 增强测试首次只在 listener 关闭时把 `io.EOF` 当错误，修正等价关闭断言
  后通过，均未隐藏。未访问 APDU/运营商网络、未发短信、未拨号、未部署。
- 第十三批完成无 Security-Agree 情形的用户态 IMS dialog 信令。新增的 outbound agent 只接受
  machine-confirmed registration、contact identity 与 voice transport，不配置 media，也不把 Registered
  推导为 call ready。fake P-CSCF 在 linked SWu userspace stack 上按序实际收到 REGISTER、INVITE、ACK、
  BYE 和注销 REGISTER；BYE 200 仅证明物理信令挂断，不冒充双向语音。provider race/vet/module verify
  全通过，未拨付费电话。联网核对 3GPP TS 33.203 后确认 IMS Security-Agree 使用 ESP transport mode；
  upstream 仅有 Linux XFRM，gVisor 仍未实现 ESP header。MIT `n0madic/go-ipsec` 虽有纯 Go userspace ESP，
  但实现位于 internal 且面向 IKEv2 VPN 的整包 IP/tunnel path，不能直接复用为 IMS transport SA，故未
  引入第二套 IKE 控制面，也未伪装跨平台 security 支持；当前继续 fail closed。
- 第十四批完成独立用户态 IMS 媒体边界。先核对现有浏览器契约为 8 kHz/单声道/20 ms/320-byte
  s16le，再复用当前依赖图中的 Pion RTP/RTCP 与最新稳定 BSD-3-Clause `zaf/g711` v1.4.0；已检查
  Pion RTP 最新 v1.10.5，其更新只涉及本任务无关的视频/扩展头，因此保留 pinned upstream 已验证的
  v1.10.2。Bridge 先在 SWu 内存栈保留本地 RTP/RTCP 端口供 INVITE SDP 使用，随后原子应用 answer
  的 literal-IP 远端，不走宿主 DNS/路由。100–2000 ms 有界 PCM 队列的 stale/overflow 只计数和丢
  媒体，不拥有挂断；20 ms pacer、PCMU/PCMA、talkspurt marker、短缺静音、固定 peer 校验、序号
  wrap/loss 与 RTCP Sender Report 已实现。linked fake SWu 双栈实测双向非静音 PCM/RTP、RTCP、延迟
  SDP endpoint、父取消、队列压力和 Close 后不再发 RTP；provider 全量 race/vet/module verify、媒体
  与 usernet 各 10 次重复线测及 Linux/amd64 静态编译通过。重复门曾抓到测试失败路径先关底层
  netstack、RTCP writer 仍运行而触发上游 `send on closed channel`；usernet 现只跟踪本批真实使用的
  UDP packet socket，在 device 前逆序关闭。曾尝试扩成通用 TCP/UDP wrapper，却隐藏 connected UDP
  的 `net.PacketConn` 能力，导致 Go DNS resolver 误走 stream 并超时；该过度方案已撤销，SIP/DNS
  保持原类型后真实 IMS DNS/注册测试恢复。可选远端改造首次编译还曾因局部 `err` 未声明失败，以上
  问题修复后全量重跑通过，均未隐藏。当批尚未把 Bridge 绑定进 SIP dialog；该项已由下一批完成，
  service IPC 仍未实现，也不宣称真实通话健康。
- 第十五批把 Bridge 最小绑定到 outbound SIP dialog。联网核对 RFC 3264/3605 和现有 upstream
  offer/answer 后，不改其 REGISTER/INVITE/ACK/BYE 状态机，也不启用宿主 UDP relay：先在 SWu
  userspace stack 保留 RTP/RTCP 端口并生成 20 ms PCMU/PCMA offer，收到 2xx answer 后只接受共同
  codec、双向 audio、literal-IP endpoint；无显式 RTCP 时按规范使用 RTP+1。拨号 timeout context 与
  媒体 lifetime 分离；answer 无法使用时用独立 5 秒 context 发送 BYE 后停媒体；正常 End 无论 BYE
  成败都停本地媒体，但原样返回失败且允许下一次 BYE 重试，绝不把停流冒充停止计费。linked fake
  P-CSCF/RTP peer 已实测 REGISTER→INVITE/ACK→双向非静音 PCM/RTP→BYE→停流→注销；另测不可用
  answer 自动 BYE，以及第一次 BYE 503、第二次 200。重复 race 门最初暴露 fake UDP server 把合法
  SIP retransmission 错算成新阶段，导致提前退出和注销 timeout；改为 transaction 去重但仍逐包响应，
  新旧 dialog 测试共用同一夹具后十轮通过。仍未部署、未拨号、未触碰生产，不宣称运营商通话健康。
- 第十六批完成 provider-neutral `mdd-vowifi` service IPC 契约与 transport。联网核对最新
  Connect-Go v1.20.0、gRPC-Go v1.81.1、HashiCorp go-plugin v1.6.0；前两者为当前低频控制面引入
  protobuf/codegen/HTTP2 过重，后者由 host 启停 plugin、与状态不能拥有进程生命周期冲突，因此
  复用已验证 Agent 模式：Go 标准库 literal-loopback HTTP + 严格 versioned JSON，无新依赖。
  `go-runtime/vowifiipc` 仅含 lifecycle、typed snapshot、call/end、message operation；协议类型中没有
  SWu packet、RTP/RTCP、PCM、SIM secret。snapshot 必须带 line/provider/process generation/sequence/
  observed_at，伪造或不完整 ready 会 fail closed；mutating request 必须有 operation ID，backend 契约
  要求付费动作前持久化幂等键，崩溃后的未知结果不得盲目重放。token 至少 32 byte 并经 SHA-256
  constant-time compare；client URL 与 server 实际 RemoteAddr 双重要求 literal loopback，redirect
  禁止，unknown/trailing JSON、超大请求/响应、错误 result identity 均拒绝。真实 child process 已完成
  status→start→call→busy conflict→end→message→stop TCP 往返；十轮 race、全 go-runtime test/vet 与
  跨平台编译门在提交前执行。当前只是 IPC transport/contract，尚无真实 AGPL backend/service binary，
  未部署、未通话、未短信。
- 第十七批完成 Agent 主动 WSS 与 PC/SC AKA 硬依赖。联网核对当前 `coder/websocket` v1.8.15、
  ETSI TS 102 221、3GPP TS 31.102 和 WinSCard transaction/transmit 语义后，对外只实现同一 HTTPS/WSS
  listener 上的高层 `AuthenticateAKA`，没有远程裸 APDU、写卡、媒体或通用消息总线。连接精确绑定
  Agent process generation，AKA 再绑定 card session generation 与 ICCID；未知/尾随 JSON、响应身份
  不符、重复 Agent、过量并发均 fail closed；单次请求超时后的迟到/重复结果只丢弃，不拆掉健康
  Agent 连接。10 秒 ping/5 秒 pong 等待只更新连接健康并关闭死连接，
  不重启进程或容器。Agent PC/SC session 在每次鉴权内独占 transaction，扫描 EF_DIR 选择 USIM/ISIM，
  只读 ICCID 并执行 PIN/AUTHENTICATE；拔卡先撤销 generation 后等待已有操作。只有明确 EF_ICCID
  不存在才保留为空白 eUICC attachment，其余传输/异常状态进入既有有界指数退避，避免半就绪卡死。
  同一错误 PIN hash 在单进程只尝试一次，修改 PIN 后允许一次新尝试。真实 WebSocket→fake PC/SC
  全链、移除/空白卡/身份冲突/传输分类/事务释放/PIN containment、十轮 race 与全模块 test/vet/verify
  均通过；私有 Linux runner C 另以原生 `libpcsclite` 完整通过全模块 race（此前缺 Go、容器代理和
  缺开发包的环境失败均保留原始日志，未误报）。未访问真实 SIM、未部署。EID/profile topology、
  跨重启 PIN 尝试持久化和 PC/SC 原生阻塞
  上限仍属于后续 Agent host 工作，不能把当前 fake-card 证据称为实际 VoWiFi 可用。
- 第十八批完成 provider→Core 的本机 AKA broker IPC。provider 不读取远端 Agent 连接表，只向
  literal-loopback HTTP 提交精确 Agent/process/card generation；Core 复用第十七批唯一公开 WSS
  将请求转发给 Agent。客户端拒绝 DNS/非 loopback/redirect，服务端再按实际 RemoteAddr 校验，
  32-byte 以上 bearer 固定长度哈希比较；严格 JSON、16 KiB 双向上限和有界 timeout 均已实现。
  Agent typed failure、离线、generation conflict、timeout 与 broker failure 保持独立 machine code，
  失败结果也必须通过 operation/session 身份校验。真实 HTTP→Core broker→WSS→fake Agent 全链、
  远端来源/错误 token/未知字段与十轮 race、全模块 test/vet/verify 均通过。该本机 IPC 不新增公开
  端口，未接真实 Agent/SIM、未部署。
- 第十九批完成真实 `mdd-vowifi` Go service executable/backend。provider 现在把精确 Agent/进程/卡
  世代的 AKA broker 接入固定上游 SIM provider，再依次建立 SWu userspace packet session、内存
  gVisor IP stack 和 IMS registrar；任一失败只形成该层 typed failure，由 Core 使用新 operation ID
  按全局退避重试，不重启进程或容器。上游 registration maintenance 新增无副作用 `Snapshot` seam，
  refresh 失败不会让首次 Registered 永久冒充当前状态；stack pump 失败也只降级这一线路。
  Start/Stop 在副作用前后写入 0600 bbolt 幂等记录，崩溃留下的 pending 不盲目重放；进程收到退出
  信号会先停止 IPC，再有界注销 IMS/关闭 stack。严格 0600 JSON config 只允许 literal-loopback IPC，
  provider→Core 和 Core→provider 都不是公开端口；浏览器/API/Agent 仍设计为同一 HTTPS/WSS listener
  的独立连接。真实构建进程已完成 health/status、0600 数据库与 SIGINT exit=0 冒烟；provider 全量
  race/vet/verify、完整 pinned upstream test/vet、go-runtime race/vet/verify 全过。付费 call/message
  仍明确返回 not_ready，因为浏览器 PCM WSS 和 messaging operation 尚未接入；未接真实 SIM、未访问
  运营商、未部署、未拨号、未发短信。
- 第二十批完成同一公网 WSS 的媒体透明层和 provider 无收费 PCM canary。联网核对
  `coder/websocket` v1.8.15 的 `NetConn`/Reader/Writer 与 RFC 6455 后，确认 byte-stream
  `NetConn + io.Copy` 会丢失现有文本控制/320-byte binary PCM 消息边界，因此 Core 使用逐消息双向
  relay：保留消息类型/边界、64 KiB 上限、禁用压缩、自然背压、同源校验和有界 close；provider
  target 与 bearer 只能发往 literal-loopback，Core 不解析或持久化 PCM。`mdd-vowifi` 同一个本机
  listener 新增 `/v1/media/{session}`，再次校验 loopback/bearer、单 session 单连接，兼容现有
  browser.media v1 hello/challenge/evidence；付费动作前先回环两帧非静音 320-byte PCM，并要求浏览器
  capture/playback/played 证据均递增到 2 才 ready。真实 WebSocket 测试已贯通 browser→Core relay→
  provider 的文本、双帧 PCM 和 ready evidence；跨源、非 loopback、未授权、重复 session、超限消息
  均拒绝。该批截至提交时仅是无收费 transport canary，尚未绑定 `StartMediaCall`。
- 第二十一批完成 provider 内部的真实 call/media 绑定和独立 10 秒守卫。再次联网核对固定
  `vowifi-go` commit 与仓库 HEAD 相同，并按 RFC 3261、RFC 6455 和 `coder/websocket` 当前行为复审。
  只有同 call_id 的 browser session 完成非静音 canary、仍 connected，且 runtime 当前 voice layer
  ready，才在副作用前持久化请求指纹并调用 `StartMediaCall`；IMS 已接通但浏览器绑定失败时立即 BYE。
  live PCM 只在 provider browser session 与 userspace RTP bridge 之间传递；resume ticket 每次轮换，
  断线 10 秒内恢复同一 call owner，不新拨号。公开 `callsafety.Guard` 只接收 call_id/phase/connected/
  last_seen，注册、隧道、进程、容器和线路健康不可能进入挂断判断；超过 10 秒才对该精确 call 发 BYE。
  显式 Stop 和进程退出先 BYE，再注销 IMS/关闭 stack 和 WebSocket。真实 WebSocket 与 service race
  测试覆盖 canary→live PCM→断线→resume、start/end 幂等、短暂重连、超时挂断及 BYE-before-close。
  测试曾暴露 ready 后每帧发送 status（约 50 条/秒），已改为每连接只发一次并重复测试通过。尚未
  接公开 Core authorizer、未做进程级全链、未部署、未接真实 SIM、未拨号、未发短信。
- 第二十二批完成进程级 fake Core/Agent/P-CSCF/RTP 全链。生产 `run` 仍只构建真实
  `UpstreamFactory`；新增 `runWithFactory` 窄测试 seam，fake factory 和所有假 peer 只存在 `_test.go`，
  没有运行时 fake 开关或测试 API。父测试进程提供同一公网 WebSocket relay 和带 token 的 fake Agent
  AKA broker，子进程运行真实 provider HTTP/IPC、bbolt、browsermedia/service，并创建内存 SWu 双栈、
  fake P-CSCF/RTP。实际贯通 Agent/进程/卡世代校验→runtime Start→canary→durable StartCall→REGISTER/
  INVITE/ACK→双向非静音 PCM/RTP→EndCall/BYE→runtime Stop→SIGINT 正常退出。race subprocess 连续三轮
  及 provider 全量 race/vet/verify 通过；测试过程中曾发现夹具非法 card_id 和并发日志 Buffer race，
  均只在夹具中修正后重跑通过。仍未部署、未接真实 SIM/运营商、未拨号、未发短信。
- 第二十三批把真实 Core authorizer 与 provider directory 挂到同一 HTTP/WSS listener。浏览器原生
  WebSocket 无法添加自定义认证头，因此沿用当前 HttpOnly 登录 cookie 的抽象 verifier；公开路径只
  携带 32-byte 随机媒体 session ID，不把 session/token 放 query 或日志。租约精确绑定 subject、line、
  call、provider generation 和期限；每次连接先重验登录，再解析 current provider，换代后旧租约立即
  返回 conflict。provider directory 只保存 literal-loopback WS origin/token/代际，不读注册、隧道、
  页面状态或 PCM；迟到 Remove 不能删 replacement，已被替换 generation 不能重新成为 current。
  Core `/healthz`/API 与 `/api/browser-media/{sessionID}/ws` 已在同一真实 httptest listener 完成带
  cookie/Origin 的 WebSocket 往返，并验证无登录 401、旧代际 409；聚焦 race 连续十轮、完整
  go-runtime race/vet/module verify、隔离 provider race/vet/module verify，以及两个 Linux/amd64 静态
  executable 构建均通过。首轮聚焦测试因测试断言漏 import 编译失败，补齐后整批重跑通过，未隐藏。
  仍未迁移登录存储或启动 live Core，未部署、未接真实 SIM/运营商、未拨号、未发短信。
- 第二十四批完成现有管理员认证数据的 Go 只读兼容层。按 Python `hashlib.scrypt` 独立生成的固定
  向量确认 `auth.json` 的 N=32768/r=8/p=1/32-byte 哈希可直接验证；只新增官方维护的 BSD-3-Clause
  `golang.org/x/crypto/scrypt` v0.55.0，没有复制或转换真实密码。session 仍为内存态、12 小时、
  服务重启失效，token 只以 SHA-256 key 保存在服务端；保留当前 cookie/header/CLI token、CSRF、
  五次失败后 60 秒限流及 login/status/logout JSON 契约。媒体 verifier 明确只接受 HttpOnly cookie，
  header-only CLI session 不能签发一个浏览器无法在 WSS 握手复现的租约。Cookie 使用 Path=/、
  HttpOnly、SameSite=Lax，live TLS 配置必须启用 Secure。Core 新增通用 management middleware：
  `/v1/lines` 需要 session，`healthz` 公开，Agent 与媒体仍由各自精确握手授权；logout 恢复旧契约的
  session+CSRF 校验。真实同一 httptest listener 已依次完成 login→cookie→认证 lines API→媒体租约→
  WSS 往返，并证明未认证 API/WS 均拒绝、旧 provider 代际仍 409。五轮聚焦 race、完整 go-runtime
  race/vet/module verify 与 Linux/amd64 静态构建通过。最初依赖命令在已位于 `go-runtime` 的 workdir
  又执行一次 `cd go-runtime`，原样报 no such file 后命令仍在正确目录完成；没有据此跳过任何门。
  当前 `cmd/mdd-core` 尚未加载 auth/TLS/provider registration，故仍不能称 live executable 已完成。
- 第二十五批完成 provider→Core 的本机动态路由登记。复审现成方案后未采用由 host 启停子进程的
  `go-plugin`，也未为单机目录引入 Consul；复用 AKA broker 的 literal-loopback HTTP + 32-byte bearer
  模式。provider 启动本机 IPC 后立即登记 line/process generation/loopback WS origin/media token，
  每 10 秒幂等刷新，Core 路由租约 30 秒；Core 重启后无需重启 provider，最多一个刷新周期恢复。
  心跳只表示本机路由可解析，不携带或推导 IMS/通话健康，不触发进程/container 恢复。provider 退出
  时先停刷新并作 generation-aware remove；Core 不可达时注销为 best-effort，旧入口最多存留至 TTL，
  不因此制造服务重启循环。迟到 remove 不能删除 replacement，已替换 generation 不能靠迟到心跳
  重新成为 current；provider 突然退出则目录自然过期，Core 在此前拨本机失败只返回 unavailable。
  client/server 均拒绝远端/DNS URL、错误 bearer、redirect、未知/尾随/超大 JSON；wire fields 固定为
  snake_case。真实 provider 子进程测试已贯通登记→Agent AKA→runtime→媒体/通话→BYE→Stop→SIGINT→
  注销，连续三轮 race；完整 go-runtime/provider race/vet/module verify 和 Linux/amd64 静态构建通过。
  首轮 registration 负例因 `httptest.NewRequest` 默认远端是 192.0.2.1 而先得到正确的 loopback 403，
  修正夹具后重跑；provider 首轮编译要求 tidy，核对只把已直接使用的 websocket 提为 direct 并统一
  x/sys v0.47.0 后全量通过。仍未组装 live Core、未部署、未接运营商、未拨号、未发短信。
- 第二十六批完成 live `mdd-core` 单 Go executable 组装。单个严格 0600 JSON 只保存入口与文件路径：
  公网 listen/TLS identity、现有 `auth.json`、0600 bbolt event store、本机 IPC listen/token 和事实
  TTL；未知字段、尾随 JSON、相对路径、宽松权限、过大文件和短 token 均在监听前拒绝。公网只开
  一个 HTTPS listener，同时承载 login/management HTTP、Agent WSS 和 browser-media WSS；provider
  registration 与 Agent AKA broker 共用另一个 literal-loopback HTTP listener，仍以 32-byte bearer
  和实际 RemoteAddr 双重限制，不向远端公开。`auth.json` 的现有 `agent_token` 只在内存交给 Agent
  resolver，不写入 event store。新增 cookie+CSRF 媒体租约 HTTP 入口，把当前 provider generation
  与精确 line/call/session subject 绑定后才允许浏览器在同一公网 WSS 连接，CLI header 不能签发一
  个浏览器无法复现的租约；撤销幂等，12 小时上限只允许长通话/短时重连，本身不拨号、不持有恢复
  或挂断判断。收到 SIGINT/SIGTERM 会有界关闭两个 HTTP server；没有添加子进程/container supervisor。
  真实 child process 使用自签测试证书的精确信任根（未禁用 TLS 校验）贯通 login→Agent WSS→本机
  AKA broker→provider registration→lease→同一公网 browser WSS→loopback provider 消息往返→正常
  退出；全 go-runtime race、五轮聚焦 race、vet/module verify 与 Linux/amd64 CGO=0 静态单文件构建
  通过。首个编译命令因 workdir 已在 `go-runtime` 却又给文件加同名前缀而立即 `lstat` 失败；修正命令
  后执行。新增租约单测第一次引用不存在的夹具名而编译失败，改用正式接口内联 verifier 后整批重跑
  通过。仍未部署、未接真实 SIM/运营商、未拨号、未发短信；上述 fake provider 回环不冒充语音健康。
- 第二十七批接通 provider outbound SMS typed operation。联网与远端 HEAD 核对确认 pinned
  `vowifi-go` 已是当前 commit，现成 `runtimehost/messaging` 已实现分段、3GPP SMS/CPIM、SIP MESSAGE、
  digest、redirect 与状态报告请求，因此直接复用，未自写 TPDU/RPDU/SIP 协议，也未升级依赖。
  `mdd-vowifi` runtime 只使用当前 IMS registration 提供的 `SMSTransport`；未注册或 transport 缺失
  返回独立 messaging not-ready，不重启 runtime/container。Backend 在任何网络副作用前把 operation、
  message、recipient、body 的完整指纹作为 0600 bbolt 幂等 reservation；精确重放返回既有结果，
  同 operation 改任何字段返回 conflict，明确失败也持久化并不自动重发。若发送已完成但结果落盘失败，
  reservation 保持 pending，后续只返回 result-unknown，避免收费短信重复。fake transport 实测 IPC
  business ID 保持不变、上游获得正确 device/IMSI/peer/text、成功和失败都只调用一次；五轮聚焦 race、
  provider 全量 race、vet/module verify 与 Linux/amd64 静态构建通过。未部署、未发真实短信；inbound
  SIP MESSAGE/投影与 delivery report 持久化尚未接 Core，不能把 outbound fake transport 称为完整 SMS。
- 第二十八批组装首个统一 `mdd-agent` Go executable（PC/SC-only）。联网核对
  `kardianos/service` v1.3.0 与官方 `x/sys/windows/svc` 后，确认前者可在后续复用 Windows SCM、
  systemd/launchd，但本批不为 CLI host 强加 service 依赖；PC/SC `ebfe/scard` 和 `vowifi-go` 的
  pinned commit 均与远端 HEAD 一致，无需升级。一份严格 0600 JSON 保存 Agent ID、Core WSS/token、
  明确 SHA-256 certificate pin、loopback control/token、ICCID→PIN 与有界扫描/退避参数；未知字段、
  尾随 JSON、相对/宽松配置、短 token、非法 PIN、非 WSS/带 query URL 均在打开硬件前拒绝。
  `modem_enabled` 是持久化开关且本版默认 false，true 会明确报 PC/SC-only unsupported；没有删除 modem
  方向代码，也不会误开 4G/5G 模块。`run` 先绑定固定 literal-loopback control port（跨进程 singleton），
  再同步有界启动唯一 Controller；`status/start/stop` 只是同一 authenticated API 的 CLI client，重复
  host 立即冲突，退出信号不可能与迟到 auto-start 竞态。PC/SC monitor 初始扫描完成即本地 runtime ready；
  Core 暂时离线只在同进程按全局 capped exponential reconnect，不退出、不重启进程/container，也不阻断
  热插拔监控。每次手动 runtime start 生成新 process generation。Agent WSS 新增 `hello_ack`，只有 Core
  真正接受唯一 Agent generation 后才报告连接；复审曾发现登记到 ack 之间 AKA 可抢先的首帧竞态，现
  在 connection lock 内发布并完成 ack，AKA 才可发送。自签 TLS 使用完整证书 SHA-256 pin 的 constant-time
  校验，未使用裸 `-k`/CERT_NONE；pin 不匹配在发 Agent token 前失败。真实 child process 已验证 host→
  running→CLI stop→stopped→CLI start→running、重复 host 冲突和 SIGINT 正常退出；另测 Core 离线仍本地
  ready、真实 outbound WS hello ack、pin match/mismatch。十轮聚焦 race、全 go-runtime/provider race、
  vet/module verify 全过；macOS arm64 和 Windows amd64 可执行文件构建成功。Linux 原生门未伪装通过：
  runner C 剩约 1.1GB/98% 且无 Go，A/B 无 Go 和 pcsclite 开发包，D 无 Go/pkg-config；三次 transfer
  先后因工作根为空和远端尾斜杠被包装器安全检查拒绝，按其默认工作根规范化后传输成功，但代码未在
  缺工具链的 runner 上运行。未部署、未访问真实 PC/SC/SIM、未改系统 service、未启 modem。
- 第二十九批增加 Windows SCM 外壳，没有复制 Agent runtime。联网复核后采用
  `github.com/kardianos/service` v1.3.0；安装项只保存当前 executable 与绝对配置路径，SCM 的
  Start 回调构建既有 Worker 并异步进入同一个 `runHost`，Stop 回调只取消该 context 并有界等待。
  因此前台 `run` 与 Windows service 继续由同一固定 literal-loopback listener 实现跨进程 singleton，
  `status/start/stop` 仍只操作同一 Controller。`service-install/uninstall/start/stop/status` 返回机器可读
  状态；未安装是正常 `not_installed`，不伪装成故障。线路、网络、注册或 Core 离线仍只走进程内有界
  恢复，绝不控制 SCM；仅 host 自身意外退出时主动把 SCM 状态收敛为 stopped，避免“服务运行中但
  runtime 已死”的假状态。本批没有设置 Windows failure-restart 循环，也没有实现 macOS launchd。
  新增 lifecycle 测试覆盖重复 start、协作 stop、意外退出和 stop deadline；全 go-runtime race、vet、
  module verify、Windows amd64 executable/test 交叉构建均通过。未安装/启动服务，未访问真实硬件。
- 第三十批增加同源码的 Windows/macOS GUI/tray 薄外壳。强制联网比较后没有采用稳定版缺少 tray 的
  Wails v2、仍快速变动的 Wails v3，也没有采用仅 11 commits 且无完整窗口的新纯 Go tray 库；选用
  官方当前 tag `fyne.io/fyne/v2` v2.8.1，其 `desktop.App` 直接提供 tray window、tray menu 和关闭拦截。
  Fyne 只在 `gui` build tag 下链接，默认 Core/Agent service executable 不包含 GUI runtime。GUI 仍读取
  同一绝对 Agent JSON、调用同一 authenticated loopback API；Windows 的安装/启动/停止/卸载调用同一
  SCM adapter，运行中禁止直接卸载。macOS 不新增 launchd：GUI 以同一 `runHost` 持有 Agent，只有真正
  绑定 singleton listener 后才显示窗口，CLI host/第二个 GUI 会明确冲突。关闭窗口只隐藏到 tray；
  Windows 显式退出 GUI 不停止服务，macOS 显式退出会有界停止其唯一 Agent host。两秒采样仅在状态
  内容变化时刷新控件，不制造空闲重绘。真实 macOS arm64 GUI binary、GUI-tag race、全 runtime race、
  default/gui vet/module verify 均通过；Windows service binary 与 GUI+Windows API 的 `ci` headless
  交叉编译通过，但本机无 MinGW，尚未生成/运行真实 Windows Fyne backend，不能把该项写成 GUI 验收。
  本批未运行 GUI、未安装服务、未连接真实硬件、未部署。
- 第三十一批在既有 Agent WSS 上增加应用健康与 PC/SC 拓扑事实，没有新增端口、连接或恢复状态机。
  参考 Kubernetes Lease 的轻量 renew 模式与 RFC 6455 transport ping 分层：生产默认每 10 秒上报一次，
  新连接和拓扑 revision 变化时携带完整拓扑，未变化时只发送 sequence+revision 心跳；服务端使用自身
  接收时间，不信任 Agent 时钟。传输 Ping/Pong 继续只证明 WSS 响应，application health 单独证明 Agent
  采样循环仍工作。拓扑明确区分 local reader attachment name、单次插入 session generation 和可读取的
  durable ICCID；绝不把 reader 顺序/名称冒充 SIM identity，空身份保留 discovering 或
  identity_unavailable。PC/SC 条件为 starting/ready/recovering 并保留原始错误；监控超过三倍扫描周期
  （最少 1 秒）未更新时自动报告 `PC/SC observation is stale` 并清空旧附件，避免卡片永久假在线。
  Core 对每个连接强制首次 full topology、sequence 单调和 revision/hash 一致，返回深拷贝；新的受管理员
  auth 保护 `/v1/agents` 与 `/v1/agents/{agentID}` 只展示当前 WSS connection、服务端 last_seen/
  last_report 和最新拓扑，断线即从 current 列表移除。全 go-runtime race、default/gui vet、GUI race、
  module verify、真实 Core child process 的 WSS→authenticated management API、macOS GUI、Windows Agent
  和 Linux static Core 构建均通过。未接真实读卡器、未部署。
- 第三十二批让 Agent CLI 与托盘直接消费上述同一 typed topology，没有新增硬件扫描器、缓存或配置
  真相。`Worker.Topology` 同时供 outbound Agent WSS 和本机 API 使用；停止后撤销当前 manager 并清空
  附件，stopped/failed runtime 的本机 API 明确返回 `topology_unavailable`，不会把旧卡或 `starting`
  冒充在线。新增 `mdd-agent topology`；GUI 明细与摘要显示同一 PC/SC condition，Windows service 与
  macOS GUI host 仍使用原 singleton/controller。按 RFC 6455 复审后确认公网目标为一个 HTTPS/WSS
  listener：每个浏览器/Agent 各自一条连接，低频控制/状态/拓扑按 typed message 复用；实时音频使用同
  listener 的独立 WSS，避免单一有序流的拥塞阻塞控制心跳，不增加 RTP 公网端口或用户 IP 确认。
  聚焦及全模块 race、default/gui vet、module verify、macOS GUI、Windows Agent/GUI API 和 Linux static
  Core 构建均通过。未触碰已有脏 WebUI、未接真实读卡器、未部署。
- 第三十三批把浏览器低频状态接到 Core 同一公网 `/ws`。连接只接受现有 HttpOnly 管理会话 Cookie，
  保留 `coder/websocket` 默认同源检查并禁用压缩；每个浏览器独立收到 versioned `browser.snapshot`，
  其中线路按发送时刻重新投影、Agent 来自当前 WSS connection/topology。浏览器不能发送应用消息，
  会话在每个 10 秒周期重验，logout/过期按既有 4401 关闭；两浏览器并发互不抢占。付费 mutation 仍走
  现有 CSRF/idempotency HTTP，PCM 仍走同 TLS listener 的独立 WSS，避免媒体背压拖住状态。前端 `/ws`
  删除了 URL session token；旧 Python endpoint 本已支持同一 Cookie，故兼容而且不再把凭据留在历史、
  代理或访问日志。诊断页可直接投影 Go Core Agent 的 WSS/PCSC condition、reader 和当前 card identity；
  不把这些状态冒充通话/短信健康。真实 Core child process 使用受信测试 CA 完成 TLS login→Agent WSS→
  `/ws` snapshot→media WSS；无认证、跨源、TTL 自然过期、双浏览器、会话撤销均有 race 测试。全 Go
  race/vet/module verify、18 个 WebUI 脚本、外置盘 Vite production build 和 Linux static Core 构建通过。
  工作区既有 VoWiFi requestable 与 dist 未提交改动仍保留且未混入；未部署、未接硬件、未拨号。
- 第三十四批补齐 Agent 的只读 eUICC 身份拓扑。联网核对 `estkme-group/lpac` v2.3.0、其
  LGPL `libeuicc`/独占 PC/SC 驱动和最新稳定 MIT `github.com/damonto/euicc-go` v1.1.2 后，采用
  后者的自定义 SmartCardChannel，不再启动第二个 lpac/PCSC owner，也不引入 C 动态库。Agent 在每次
  插卡既有 transaction 内，通过同一 Card handle 打开临时 ISD-R logical channel，只执行 EID 和
  Profile list 两项 ES10 读取；随后关闭 logical channel，卡的生命周期仍归原 session。普通 USIM、
  非 eUICC 或 Profile 查询失败不阻断 ICCID/AKA session。拓扑新增独立 EID、Profile ICCID/state 和
  `profiles_available`；因此 EID+空数组表示已确认空白 eUICC，EID+false 表示列表读取失败，不把两者
  混同。EID 不能代替当前 Profile ICCID 发 AKA。上游稳定版对畸形可选 Profile 字段可能 panic，边界已
  把它隔离成只读探测失败，不允许崩掉 Agent。真实 BER-TLV/APDU fake-card 覆盖空卡、双 Profile 排序、
  enabled/disabled、畸形响应和同 Card ownership；全 go-runtime race/vet、macOS CLI/GUI、Windows
  amd64 CLI/GUI 交叉构建及 Linux/amd64 静态 Core 均通过。提交后另以同源码一次性、未安装的测试
  binary 在两台私有 macOS 主机做共享事务 shadow：普通 USIM 明确返回非 eUICC；空白 eUICC 读取
  EID 且 `profiles_available=true/profiles=[]`；另外两张 eUICC 分别读取 3、5 个 Profile，并准确保留
  单一 enabled 与其余 disabled 状态。两台既有 Agent 全程保持原 PID/ready/双 reader 在线，临时
  binary 和远端目录均删除。第一次传输因本机 rsync 3.4 的 `--protect-args` 与远端 macOS rsync 2.6.9
  不兼容而未产生文件，随后用 SHA-256 核对的单文件传输完成，失败未隐藏。新 Go Agent/Core 仍未部署。
- 第三十五批把 CLI、Windows service 与 macOS GUI 收敛到同一个 owner-only JSON 配置。默认路径来自
  系统用户配置目录，也可由 `MDD_AGENT_CONFIG` 或 `-config` 明确覆盖；`config init/show/set` 与 GUI
  读取同一文件。token 只能从 stdin 持久化，展示固定脱敏；新目录 0700、文件 0600，拒绝 symlink 和
  group/world-writable 父目录，使用当前最新版 Apache-2.0 `moby/sys/atomicwriter` 原子替换并同步目录。
  默认仍是 `modem_enabled=false` 的 PC/SC-only 模式。全模块 race/vet、真实 CLI 权限/脱敏冒烟和
  Windows amd64 headless 构建通过。
- 第三十六批新增 macOS 候选发布入口，只在调用者指定的外置盘目录生成独立 headless CLI 与标准
  `MDD Agent.app`；两者来自同一源码、同一配置、同一 singleton，不能同时占有 PC/SC。脚本固定当前
  Fyne tools v1.7.2/runtime v2.8.1，默认仅作 ad-hoc 开发签名；Developer ID 使用 hardened runtime，
  notarization 保持独立显式发布动作。前两次包装分别因缺少 `--source-dir` 和错误假定 bundle executable
  名称而在 staging 内失败、没有发布文件；修正后 codesign 与逐文件 SHA-256 均通过。
- 第三十七批在一台私有 Mac 做了原位、可回退的真实 PC/SC-only shadow。首次冷启动时两个 reader 都
  枚举成功但身份长期停在 discovering；新增 generation-bound `identity_detail` 后抓到 WinSCard/PCSC
  返回 `Card is unresponsive`。只读 APDU probe 证明卡本身可完成 MF/EF_ICCID 全链，随后与仓库成熟
  Agent 对比定位为新 connector 只尝试 `ProtocolAny`；按既有顺序最小恢复 T=0→T=1→Any 后，两张卡
  均在 5 秒内 identified，其中空白 eUICC 正确返回 EID 与零 profile。没有新增硬件状态机，session
  失败仍只在同进程走既有指数退避；Core WSS 断线现在保留具体错误而不吞掉。
- 同一真实 shadow 又验证了一个公网 TLS listener：Agent management WSS 与浏览器 `/ws` 状态 WSS
  同时通过，浏览器收到 1 个 Agent、2 个已识别 reader；停止 Core 时本地硬件持续 ready/identified，
  原配置重启 Core 后 Agent 在退避窗口内自动重连，无需重启 Agent。macOS 15 把 SSH 子进程脱离会话后
  的本地网络访问以 `no route to host` 阻断；验证因此保持 SSH owner，会话内全链通过。产品不为该系统
  权限另造网络兜底：正式 headless 由真正 daemon 承载，GUI 由用户会话授权。测试结束后影子 Core 与
  Go Agent 均停止，旧 Python Agent 已恢复原有 parent/child owner 模式、两张 reader 和生产 WSS bridge 均在线。
  全 go-runtime race/vet/module verify、macOS CLI/GUI、Windows amd64 CLI/GUI 和 Linux amd64 static
  Core 构建通过；Linux Agent 因 PC/SC CGO 依赖不冒充单静态 binary。最终从代码提交 `f147235`
  重新生成了 `source_tree=clean` 的 macOS arm64 候选，逐文件 SHA-256 与 CLI/app codesign strict
  校验全部通过；预修复候选不再作为交付物。
- 第三十八批完成 Go VoWiFi 的纯用户态 IMS Security-Agree/ESP transport。联网再次确认固定的
  `boa-z/vowifi-go` upstream HEAD 未变，并核对 3GPP/ETSI TS 33.203、gVisor 网络边界以及 MIT
  `n0madic/go-ipsec`/`veepin`；后两者均是完整 IKE/VPN 控制面，直接引入会重复现有 SWu IKE 且扩大
  权限与状态面，因此只复用当前 upstream 已测试 ESP codec 并补齐 3GPP 所需 null encryption 与
  HMAC-MD5-96。新的 packet transformer 只在既有 gVisor↔SWu packet pump 内保护命中实际连接地址、
  port-c/port-s 的 UDP SIP，支持 null/AES-CBC 与 SHA1/MD5 96-bit ICV、64 包 replay window；未增加
  TUN、宿主 route/raw socket、公开端口、进程或 container 重启。完整 linked-stack 测试已真实走完
  plaintext REGISTER challenge、ESP 保护的 AKA REGISTER/200 与保护的注销，并验证篡改、重放及
  匹配 plaintext 被拒绝。provider 与完整 pinned upstream 的 `go test -race ./...`、`go vet ./...`、
  `go mod verify`、十轮聚焦回归和 Linux/amd64 CGO-disabled 静态构建均通过。未部署、未连接真实
  SIM/P-CSCF、未拨号或发短信；TCP/TLS Security-Agree 与 IPv6 extension-header selector 继续
  fail closed，不能把 fake P-CSCF 结果称为运营商可用。
- 第三十九批把 Go Core 的 VoWiFi 启停、拨号、挂断和出站短信操作接到既有单一公开 HTTPS/WSS
  listener；没有新增公开端口，也没有把控制消息和 PCM 强塞进同一物理 WebSocket。联网核对确认
  Go 标准 `net/http` 已原生支持 method+wildcard 路由、当前 `coder/websocket` v1.8.15 已是最新、
  pinned `vowifi-go` HEAD 未变；因此未增加 router/RPC/multiplex 依赖。公开 mutation 沿用管理员
  cookie/header 与实际 `X-MDD-CSRF-Token`，严格解析原有 typed IPC request 和 operation ID，再通过
  无代理的 literal-loopback HTTP 调用当前 provider；token、loopback URL 和 generation 不返回浏览器。
  provider directory 以每线路读写租约线性化操作与换代：同线路 replacement 等待有界操作结束，
  其他线路和同代 10 秒 heartbeat 不被阻塞，避免切代瞬间把收费动作发给旧 provider。返回的
  line/process generation 必须匹配路由租约；无 provider、上游 typed failure、timeout、transport 和
  identity mismatch 保留为不同机器错误，不触发进程/container 重启。真实 child-process 测试用精确
  测试 CA（未关闭 TLS 验证）贯通 login→无 CSRF 403→带 CSRF public runtime start→loopback provider，
  并在同一个公开 TLS 端口继续通过 Agent WSS、browser state WSS 和 browser media WSS。五类操作的
  typed fixture、同线路 replacement/跨线路并发、聚焦 race 均通过；未部署、未发短信、未拨号。
  审计同时纠正了 README 中过时的“outbound SMS 未实现”：第二十七批早已接通 outbound SMS；真正
  尚缺的是 provider snapshot 的 Core 持久化/浏览器事实同步及 inbound SMS/delivery report。
- 第四十批完成 provider typed snapshot → Core 持久事实 → browser state WSS。动态路由登记新增稳定
  `provider_id`，控制结果和状态快照都必须同时匹配 line/provider/process generation；同线路换代期间
  使用既有租约线性化，迟到旧代际不能写状态。Provider 首次登记后立即上报完整 runtime/tunnel/IMS/
  IMS voice/messaging 五层快照，之后与 10 秒登记周期同拍；周期失败不退出或重启 Provider。Core 的
  literal-loopback `/v1/provider/facts` 使用同一 bearer、实际来源校验和严格 JSON，把有变化的层作为
  event 原子追加，同时只覆盖一个 durable checkpoint；值不变时不增长事件日志，只刷新该完整快照
  明确覆盖层的新鲜度。Checkpoint 不会续命同一 Provider 的 browser media 等未覆盖事实，路由登记
  heartbeat 仍不携带或推导健康。Core 重启从 bbolt 恢复事件和最新 checkpoint；旧 sequence、被替换
  generation、身份不符、非 loopback、错误 token 和未知字段均拒绝。真实 Core 子进程使用测试 CA
  贯通本地快照→bbolt→同一公开 TLS listener 的 browser WSS，并读到 fresh IMS voice fact；真实
  Provider 子进程证明启动态 IMS 快照在周期内到达 fake Core。两个模块全量 `go test -race ./...`、
  `go vet ./...`、`go mod verify` 通过，聚焦 Core/状态/持久化连续十轮、Provider 进程连续三轮通过；
  Linux amd64 Core/Provider 均构建为静态 ELF，Windows amd64 Core 构建为单 PE 文件。审计曾发现
  checkpoint 会误续命未覆盖媒体层并在提交前增加 coverage fence，也发现首次路由登记成功但事实
  握手失败会残留短期路由，现已仅对首次失败作 generation-aware remove；测试夹具另有一次 Go 切片不可
  比较的编译错误，改用深比较后整批重跑全绿。未部署、未接真实 SIM/运营商、未拨号或发短信。
- 第四十一批从提交 `ad6dc75` 在一台私有 Mac 构建并运行真实 arm64 `mdd-core`、PC/SC-only
  `mdd-agent` 和 `mdd-vowifi` 三个单文件 shadow。使用独立外置盘目录、0600 配置/数据库和精确测试
  CA 验证，没有 `-k`/CERT_NONE；本批开始前确认四个端口无旧 owner，系统当时也没有枚举到 PC/SC
  reader。Provider 始终保持 stopped，未执行 runtime start、SIM AKA、网络、通话或短信。真实管理
  HTTPS 与 browser WSS 都读到同一 `shadow-provider` generation 的五个 fresh inactive 事实；因此同时
  证明 route registration 存在不会被投影为 runtime/IMS/voice/SMS ready。保持 Agent/Provider 运行、
  只重启 Core 后，双方自动重连/重登记，0.6 秒内 API 恢复同 generation 的五个持久事实与一个 Agent；
  新浏览器 WSS 会话也再次通过。随后分别重启 Provider 与 Agent：各自 generation 均变化，Core 只
  保留一个当前实例，没有旧 generation 幽灵状态。最终本地 Provider 状态为 runtime/tunnel/IMS/
  voice/messaging 全 stopped、`active_call=null`；按 Provider→Agent→Core 顺序退出后四个端口全部释放，
  两个 bbolt 文件保持 0600。首次 HTTP 链误用了旧 shadow 的登录响应 JSON而得到 400；长期管理员
  凭据又与旧 shadow 临时认证文件不匹配而得到 401，随后只在隔离目录按既有 scrypt 契约重建测试
  auth 并完整重跑，未修改或复制生产认证。由于现场没有 reader，本批不能宣称热插拔、EID/ICCID、
  AKA 或实际 VoWiFi 已验证；该真实硬件门仍待 reader 插入后补跑。
- 第四十二批完成 inbound SMS 与 delivery report 的 Go 主链，并保持单一公网入口。Core 在既有
  HTTPS/WSS listener 上用原 browser state WSS 投影最近消息，同时提供同 listener 的鉴权只读查询；
  Provider→Core 业务事件只走既有 literal-loopback 控制面，不增加公网端口。消息事件与可覆盖的
  readiness facts 分库：Provider 先在原 0600 bbolt 写 durable outbox，Core 写入独立 0600 bbolt 后才
  回 204，随后 Provider 删除 outbox；进程换代可收养已提交未删除事件，EventID 内容冲突仍拒绝。
  Core 持久保存 Call-ID／In-Reply-To／RP-MR 对出站 multipart 的映射，delivery report 先到或 Provider
  重启后仍可在读取时补齐关联。入站 MESSAGE 必须先完成 Core 可重放事件入队才回 SIP 200；失败回
  500，不把已收取或已送达从 Registered/transport-ready 推导出来。
- 同批修正真实 IMS Security-Agree 边界：不再另绑一个与受保护 port-c 冲突的 Contact listener，
  REGISTER、出站请求和入站 MESSAGE 复用同一条 userspace `WireSIPFlow`。初始 UDP Contact/source
  固定为隔离 netstack 内的 5060；Security-Server 选择 port-c 后，在发 authenticated REGISTER 前同时
  重绑 flow 并把 Contact 改为该协商端口（测试为 5062）。空闲读取、出站 transaction 期间到达的
  request 和 final-response drain 都会分流给同一 inbound handler；socket 读取失败只释放该连接，
  交给既有 registration maintenance 原位重连，不创建第二套恢复状态机或重启进程/容器。
- 实证：linked SWu 双栈实际建立同一 UDP flow，P-CSCF 发 SIP MESSAGE，Provider 解析文本、写 durable
  event 并回 200；race 重复 20 次通过。真实 Security-Agree fixture 检查 protected REGISTER 的源端口
  与 Contact 都为协商 port-c，Core 子进程则从同一 browser WSS 读到经本地入口持久化的消息。
  `go-runtime`、完整 pinned upstream、Provider 三模块全量 `go test -race ./...`、`go vet ./...`、
  `go mod verify` 全通过；聚焦 Security-Agree/inbound 路径各重复十至二十轮通过。Linux amd64 Core/
  Provider 为静态 ELF，Windows amd64 Core/Agent/Provider 均构建为单 PE console executable。首次全量
  Provider 测试因新增断言漏 import `fmt` 编译失败；修正后重跑。高压重复测试还真实捕获合法 OPTIONS
  retransmit 可能先于 MESSAGE 200 到达，夹具现按 SIP transaction 去重并逐次响应，未将其误报产品
  丢消息。未部署、未连真实运营商、未发短信，因此不能宣称 carrier inbound/delivery 已验收。
- 第四十三批移除 Provider 配置中的瞬时 Agent ID、Agent process generation 与 card session
  generation，只保留稳定 UICC ICCID。每次 AKA 由 Core 从现有 Agent typed topology 找出一个当前
  `identified` attachment，再把精确 Agent process/card session generation 填入既有高层 AKA 请求；
  拔插同卡或把卡移到另一 Agent 不再要求改配置或重启 Provider。没有匹配明确返回可重试
  `card_offline`；多个当前 attachment 报 `card_identity_ambiguous`，不按 reader 名称或枚举顺序猜。
  解析与执行之间如果刚好发生拔卡，现有 Agent session generation fence 仍会拒绝旧会话；没有新增
  APDU 类型、恢复循环、进程/container restart 或通话挂断输入。
- 本批实现前强制检索了现成远程 PC/SC/WebSocket 项目；它们主要转发裸 APDU 或虚拟 reader，不能
  复用为 MDD 的高层 AKA/typed topology 边界。ETSI TS 102 221 当前 Release 18 仍定义 EF_ICCID
  `2FE2` 为 ICC Identification；因此沿用现有 ICCID 身份，不把 reader 名称、ATR 或 eUICC EID 当成
  当前 Profile 的 AKA 身份。另核对 systemd template unit、Go `os/exec`/`WaitDelay` 与当前 bbolt，
  确认下一批线路配置和部署 adapter 不应先造第二套自研 supervisor 状态机。
- 新增真实双 Agent WSS 测试覆盖初次解析、拔卡变 `card_offline`、同卡重插后自动使用新 session，
  以及两个 Agent 同时报同一 ICCID 时 fail closed。Core 子进程测试先上报真实 typed topology 再走
  loopback broker；Provider 子进程仍完成 AKA→SWu→IMS→浏览器媒体全链。`go-runtime`、Provider 与
  完整 pinned upstream 全模块 `go test -race ./...`、`go vet ./...`、`go mod verify` 已通过；动态
  解析聚焦 race 重复二十轮通过。macOS arm64 Core/Agent/Provider、Windows amd64 Core/Agent/Provider
  均构建为单文件，Linux amd64 Core/Provider 均为静态 ELF。第一次聚焦编译因局部 type/variable
  同名失败，修正后重跑；
  第一次全量 race 又发现 Core 进程测试仍构造旧 Broker 字段，更新真实契约后整批重跑通过，失败均
  未隐藏。未部署、未接真实卡/运营商、未拨号或发短信。
- 第四十四批建立 Core 唯一的 durable line catalog。0600 bbolt 只保存期望线路、稳定 ICCID、SIM、
  IMS 与网络配置；不保存观察状态、Agent/进程/卡会话代际、PIN、端口、Asterisk 密钥、容器或运行
  marker。CardID 在事务内唯一，快照带 schema/revision，并经既有管理员认证的同一 HTTPS listener
  提供只读 API，同时进入已有 browser state WSS；没有新增公网端口、写接口、恢复循环或 supervisor。
- 同批新增一次性 `mdd-core import-legacy -config CORE.json -source config.yaml`。导入只接受绝对路径、
  非软链接、大小有界且权限收紧的旧 YAML，完整解析并验证所有 active line 后，才在新 catalog 为空
  时单事务写入；任一线路无效则零写入，第二次导入明确拒绝。旧源保持只读，receipt 只记录源 SHA、
  数量和时间，不把路径或旧秘密写入新库。实现前核对当前受维护 YAML v3 官方模块，采用
  `go.yaml.in/yaml/v3` v3.0.5；v4 仍为候选版，因此没有为追新引入不稳定依赖。
- 实证：`go-runtime` 全量 `go test -race ./...`、`go vet ./...`、`go mod verify` 与 diff check 通过；
  catalog/import/Core 聚焦 race 连续十轮通过。真实 Core 测试从同一 browser WSS 读到持久 catalog；
  macOS arm64、Windows amd64 均构建为单文件，Linux amd64 为静态单文件 ELF。审计修正了数字字段
  可能静默删除非法字母的问题，现仅规范空格/连字符，其余非法值会拒绝；持久记录读取时也重新校验。
  未读取或改写生产旧配置，未部署、未接真实 SIM/运营商、未拨号或发短信。
- 第四十五批移除静态 Provider 端口块。Provider IPC 现在允许 `127.0.0.1:0`，实际 bind 后继续通过
  既有 Core loopback registration 上报真实地址；Core/browser/Agent 的公网契约未增加端口。共享
  `providerconfig` 成为 Core 配置生成器与隔离 AGPL Provider 唯一 JSON schema/校验实现，删除 Provider
  命令中的重复结构和验证逻辑，避免两边字段漂移。媒体仍是同一 TLS listener 下的独立 WSS 连接，
  不和控制心跳共用一个 TCP 流，从而保留单入口部署而避免 PCM 队头阻塞控制面。
- 新增 `mdd-core render-provider-configs`：从 durable catalog 为每条 enabled line 生成确定性的 0600
  Provider 配置和无秘密 manifest；输出目录必须全新，完整内存校验后才创建，失败只清理由本次命令
  新建的目录，不覆盖旧产物。文件/instance/device 名由 line ID 哈希稳定生成；每线 IPC token 用 Core
  本地 secret 与 line ID 经 HMAC-SHA256 派生，不复用 master token；state path 明确落入调用方给出的
  持久目录。disabled line 不生成可启动实例。
- 部署层只加入一个 `mdd-vowifi@.service` systemd template：Provider 异常退出最多五次/五分钟、间隔
  十秒，业务注册失败仍由 Provider 原有指数退避处理，状态变化不会触发进程或容器重启。实现前核对
  Go `net.Listen` 的 port 0 语义、systemd template `%i` 与凭据机制；真实 runner C 是 systemd 219，
  第一版采用新版 `LoadCredential/StateDirectory` 被验证拒绝，因此收敛为单一 219-compatible template，
  配置由部署层安装为 `mdd:mdd` 0600，token 不进环境变量/unit。替换命令路径为 runner 的已知可执行
  文件后，真实 `systemd-analyze verify` exit 0；runner 上一项无关的既有 VNC unit warning 保留。
- `go-runtime` 与 Provider 全模块 `go test -race ./...`、`go vet ./...`、`go mod verify` 通过；配置生成/
  动态端口/实际登记聚焦 race 各十轮通过。Core 与 Provider 均构建为 macOS arm64、Windows amd64
  单文件，Linux amd64 为静态单文件 ELF。未安装 unit、未切换配置目录、未部署、未拨号或发短信。
- 第四十六批加入最小 desired-config 写契约，不加入自动 apply。Catalog list/item GET 现在返回全局
  revision 的强 `ETag`；PUT `/v1/catalog/lines/{lineID}` 必须同时通过现有管理员会话、原有
  `X-MDD-CSRF-Token` 和精确 `If-Match: "revision"`。bbolt 在同一个 serializable write transaction
  内比较 revision、检查 CardID 唯一性、写线路并递增 revision；旧写者返回 412 和当前 ETag，不能
  覆盖先到写入。URL/body ID 不一致、未知/尾随 JSON、超限请求、重复 CardID 均有独立拒绝路径。
- 该 API 只改 desired catalog：不生成配置、不切换目录、不调用 systemctl、不启停/重启 Provider，
  也不接受 runtime fact、注册状态、热插拔或恢复结果作为输入。首版不提供破坏性 delete；暂时以
  enabled=false 保留配置和审计/回退依据。实现按 RFC 9110 If-Match/412 lost-update 语义，并继续使用
  已固定 bbolt v1.5 的单 writer serializable transaction，没有引入新依赖或第二个状态机。
- 真实 Core HTTP 集成测试确认：正确 cookie 但缺 CSRF 返回 403；正确 CSRF/If-Match 更新并从同一
  browser WSS 读到 revision 2；旧 revision 返回 412 且数据库仍是第一写者。store/HTTP/Core 聚焦
  race 连续二十轮、`go-runtime` 全模块 race/vet/module verify 通过；Core 再次构建为 macOS arm64、
  Windows amd64 单文件和 Linux amd64 静态 ELF。未部署、未 apply、未接真实卡/运营商、未拨号短信。

目标架构和分批验收记录在本节。当前未部署、未拨号、未发短信、未改变任何生产
容器。旧 EC20/APDU 和 Control `reg_unanswered` 的未提交修改仍保留在工作树，尚未混入本批提交。

`next_action`：下一批实现显式 apply adapter，但仍不接生产：输入必须是 catalog revision 与由该 revision
生成的完整 manifest；先 preflight 二进制、目录权限、旧/新实例差异和当前 active call=0，再原子切换
`providers-current`，只 start 新增 enabled instance、stop 已禁用 instance，配置改变的同线实例必须在
无通话时显式重启。apply receipt 持久记录旧目录、新目录、revision 与 systemd 结果，失败回切旧链接；
普通事实、注册失败、热插拔、恢复退避和页面刷新不能触发 apply。先在私有 Linux runner 的隔离目录/
假 unit 做 dry-run 和故障注入，不直接接管生产或删除旧目录；WebUI 后续只调用 catalog/apply 契约，
不另造配置状态机。真实 carrier inbound SMS/delivery report 在已有 SIM/P-CSCF shadow 条件具备时再做
一次不收费接收验收，不以 linked fixture 冒充。私有 Mac 热插拔/EID/ICCID/AKA shadow 门在 reader
再次可用时补跑，且不得同时运行旧/新两个 hardware owner。现有 WebUI 的 VoWiFi requestable/dist
未提交改动属于此前独立修复，本批不替它作出处置；fake canary 不能冒充运营商双向音频。

## 2026-08-28：EC20 蜂窝语音展示与 VoWiFi 控件修复（已部署、真实网页已验收）

- 根因一：设备/概览/通话页把 VoWiFi WSS 状态当成整条线路的“浏览器语音”状态，导致两块
  EC20 虽然 Agent 已上报 `call.actual=on`，仍被显示为“语音不可用”，通话页也默认选择
  VoWiFi。提交 `66efae9` 将蜂窝语音与 VoWiFi 语音分开投影；EC20 现在显示“蜂窝语音自检
  已通过”，进入通话页默认选择“蜂窝通信模块”。
- 根因二：实时 status WebSocket 的 Engine `STOPPED` 样本会覆盖 `/api/devices` 已确认的
  `vowifi.available=false`，把硬件不支持误显示成线路故障。提交 `d613c43` 保留权威的 unavailable
  能力事实，设备页和诊断页现在稳定显示 `VoWiFi / IMS 不支持`。
- VoWiFi 不能真正开启的当前事实不是前端锁死：两台 Windows Agent 在 Windows MBN 持有
  EC20 SIM 时均上报 `sim_apdu=false`，服务端因此不能取得构建 VoWiFi 所需的 SIM APDU。
  现在“期望开启但不支持”的开关允许用户关闭；关闭后服务端投影为 `actual=off`。不伪造开启
  成功。若要让这两块 EC20 真正启用 VoWiFi，需要后续单独实现 Windows Agent 的 SIM 所有权/
  APDU 能力模式，不能混入本次展示修复。
- 生产 Control image：
  `sha256:a49435bbc6d1d959315e263634a7f47daf1f70a787523e52a222086c88c3af91`；
  deploy record=`codex-20260828-ec20-live-status-d613c43`，Control restart=0；所有 Engine 未替换，
  活动通道/通话均为 0，未拨号、未发短信。
- 真实网页已逐页点击概览、设备、IMEI 池、通话、短信、eSIM、网络出口、通知、系统设置和诊断。
  两块 EC20 在概览/设备页均为蜂窝语音可用；通话页默认蜂窝模块；短信页为蜂窝短信可用；
  诊断页 VoWiFi 明确为“不支持”。其余页面未出现页面级错误。自动化测试仅作回归补充，不作为
  本条验收结论。

`next_action`：用户刷新生产网页验证显示与 VoWiFi 关闭动作；EC20 真正 VoWiFi 开启能力另立
Agent 能力批次，不能再把它显示成蜂窝通话故障。

## 2026-08-28：状态事实收敛与端到端诊断（已定方向，待实施）

**决策：不做全量重写，做渐进式状态投影重构。** 现有的 Engine replacement、挂断、收费
SMS/通话 lease、PC/SC 事务锁和 fail-closed fence 都是安全边界，不能在一次重构中删除或重写。
真正需要替换的是“每个模块各自给 UI 一个状态字符串”的展示/诊断层：它现在同时读取卡路由、
Engine、USIM、PJSIP、admission、媒体、历史日志，彼此覆盖，导致 `Registered`、`allow` 或容器
运行被误显示为“正常”。

第一批目标：

1. 新增按 `container_id + engine_run_id + VPCD session_generation` 绑定的线路事实快照；卡路由、
   隧道、IMS、admission、活动通话、媒体和恢复事务全部携带来源、时间和 generation。页面状态只由
   此快照投影，明确区分 `ready`、`degraded`、`blocked`、`unknown`，不再把展示文本当业务判断。
2. 把 fence 收敛为“禁止哪些动作、由谁拥有、如何恢复”的事务状态，不再兼任通话健康展示；任何
   跨代 fence 必须能显示精确旧/新 generation、自动恢复资格和人工排障入口。
3. 设置页新增“验证与排障”：被动拓扑/证书 pin/出口/IMS 采样；无收费浏览器↔Control↔Asterisk
   双向 PCM canary；有明确目标、预算和独立挂断路径才允许的收费呼叫稳定测试；以及只读证据和
   有界人工恢复。健康轮询不得自动拨号或发短信。

调研：Asterisk 官方把单元、功能和系统测试分层；SIPp 可做场景/RTP echo/统计，但它不能替代本
项目的浏览器 WSS 与真实运营商链路。因此不把 SIPp 嵌入生产；借用其“固定场景、单次预算、媒体
收发统计、明确退出码”的模型构建本地验证 runner。

`next_action`：先实现事实快照与只读/无收费验证 endpoint，再替换设备、通话和短信页面的状态
来源；收费呼叫测试最后接入，复用已存在的授权与物理挂断记录，不能用自动健康探测代替。

### 实施进度（commit `cdbf394`，未部署）

- 已新增纯投影 `control/app/line_facts.py`：同一快照绑定 Engine `container_id`/
  `engine_run_id`/启动时间和远程 VPCD session generation；卡路由、PIN、隧道、IMS、动作
  边界、活动通道、浏览器媒体分别呈现 `ready/degraded/blocked/unknown`。`Registered` 只会让
  IMS 一项为 ready，不能覆盖其他异常；被动采样中途换代会明确标为 unknown，绝不混用两代数据。
- 新增 `GET /api/instances/{iid}/facts`（无 I/O）与
  `POST /api/instances/{iid}/verification/passive`（有界、只读 Engine/PJSIP/通道采样；不发
  REGISTER/SMS/呼叫）；成功的无收费浏览器 WSS PCM canary 只在相同 Engine 世代中显示为有效。
- 设置页已加入“验证与排障”：事实快照、无收费被动采样、浏览器 WSS 双向 PCM、现有国家出口
  SOCKS5 UDP 端到端探测、以及跳转到原有真实通话页的人工收费稳定测试入口。没有自动外呼或
  自动短信。回归：`148 passed, 13 subtests passed`，WebUI build 通过。
- **尚未替换**设备/通话/短信旧页面的 status 消费者；旧 `status.py` 仍仅作为历史兼容和轮询
  原始证据，不能再宣称它是完整健康结论。下一批先用 facts 替换展示投影，再逐项审计
  `_line_admission_blocked` 的每个持久化来源，删除“历史残留被当作正在切换”的错误阻断，保留
  真实进行中的跨进程切换与活动通话安全边界。不得把状态快照本身接入任何新业务拦截。

### 第二批（commit 待创建，未部署）

- 设备/实例快照和实时 status WebSocket 已附带 facts；读取 Runtime event cache，不会因打开网页为
  每个 Engine 额外 Docker inspect。设备、VoWiFi 与浏览器语音展示优先使用事实 summary/code。
  前端不再因 `REGISTERING/STOPPED/NO_CARD` 这类**展示样本**本身禁用已可用的 native WSS 外呼；
  真正的媒体 prepare/Engine generation/admission 仍是服务端最终准入。
- `usim_recovery_blocks_paid_submission` 已改成以当前 `engine-run-id` 校验所有可验证 recovery
  owner：完整且明确属于旧 generation 的残留显示 `usim_recovery_stale_generation`、不阻断新
  REGISTER/通话/SMS；同代、冲突、缺失或不完整 artefact 仍 fail-closed。Engine 启动和维护交易仍用
  原始 durable fence，未被放宽。
- Settings 的人工 IMS REGISTER 现在服务端先作同代零活动通道核验；通话稳定测试需用户输入号码
  并确认收费，接通后按 10–300 秒绝对时钟以该 WSS 会话挂断，随后无收费被动采样确认零通道。
  健康轮询没有、也不得拥有自动呼叫/SMS 能力。

`next_action`：独立复审本批的 WebSocket facts 投影、稳定测试的“超时/结束/零通道”路径与 stale
owner 语义；通过后一次部署到生产，刷新网页做无收费 facts/PCM/出口验证。收费稳定测试由用户
在新 UI 中自行明确触发，不作为部署验收步骤。

### 生产部署（2026-08-28，已部署，网页验收待管理员配置）

- 本批 `ef9ec7b`、`e93c4cf`、`61cc8a4` 已完整同步并以**新 Control image**部署到生产；运行
  image=`sha256:61bc13718384f85a4f55e47a3d169aa3660fe9d6aa257ca60a78d7c7e1dbda09`，容器
  restart=0。容器内 `main.py`/`engine.py`/WebUI index SHA256 分别等于本地冻结的
  `cf453f…`/`77225…`/`0d7b…`。先前误用 `MDD_REUSE_CONTROL_IMAGE=1` 的 reload 只复用了旧
  image，已识别为未部署、随后用完整 build 修正，不能把第一次操作算成功。
- Engine image 未替换（两条仍为 `sha256:e68b…`）；部署前后两线 Asterisk 都是 `0 active
  channels / 0 active calls`。但 `reload --no-engines` 重启宿主 orchestrator 后，Engine 1 的
  `restart_count` 从此前记录 0 变成 1；Engine 7 保持 0。此副作用已记录，不能再将这种 reload
  描述为“不影响 Engine”。修复 `ea9303f` 已单独同步生产 `install.sh`：今后
  `reload --no-engines` 会保留 host orchestrator（不再重启它）；该脚本修复未再执行 reload，
  因此不会制造第二次 Engine 干扰。
- TLS SPKI pin 的 HTTPS `/api/auth/status` 成功；facts endpoint 在未认证状态返回 401，说明路由
  已由新 Control 提供。当前 `/data/auth.json` 只有 agent token、`auth/status.configured=false`，
  所以无法在不重置管理员认证的前提下自动登入网页/API 验收。不得擅自 setup/覆盖管理员密码。
- 私有生产记录：`/opt/mdd-gateway/data/deploy-records/codex-20260828-line-facts-e93c4cf/manifest.txt`
  （SHA256 `900f651c…`）。无自动拨号、无自动短信。

`next_action`：用户完成现有管理员 setup/login 后，刷新设置页的“验证与排障”，先执行 facts、
无收费被动采样、浏览器 PCM 和出口 UDP；收费稳定测试必须由用户手动输入号码并确认。代码后续
整改项：把 `run_orchestrator` 从 Control-only reload 中分离，防止无 Engine rebuild 的 Control 更新
仍重启在线 Engine。

## 2026-08-28：giffgaff 线路恢复与重连状态同步已完成

本批没有继续排查 Free FR。giffgaff（iid1）“VoWiFi 正常但不能呼叫/短信”的现场根因已拆成
两层并分别修复：

1. 旧代际的 `usim-auth-recovery` 已在 AMI 重连窗口内进入 `exhausted`，但 Asterisk 的
   `MDDRearmOnly` 成功响应漏回 `ActionID`，Control 把成功误判成超时；同时旧 campaign/fence
   在 Engine 重启后阻止新代际首次 REGISTER，形成“新代际无法产生 AUTH_OK、调和器又要求
   AUTH_OK 才清理”的死锁。
2. Agent 重连时同一张卡从 VPCD 槽 15 改分配到槽 10，旧 `instance.json`/`pin_reader`/
   `ami_reader` 仍指向 15，导致 Engine 读到离线槽；页面的 Registered/VoWiFi 展示因此不能
   代表实际可用链路。

最小修复提交：

- `db34dd3`：AMI `MDDRearmOnly` 回应保留请求 `ActionID`，并允许同一 Engine/card campaign
  在明确的 late `AUTH_OK` 证据下完成一次 timer-only 恢复。
- `c36e658`：跨 Engine 世代时，只有当前 SWu/P-CSCF 已就绪、Asterisk 明确
  `Unregistered`、且零通道，才归档旧 exhausted campaign，并提交一次非付费 REGISTER；同代、
  状态未知或通道非零仍 fail-closed。
- `8aacb10`：Agent/PCSC 重连时，单读卡器线路的 `reader_index` 与已显式保存的
  `pin_reader`/`ami_reader` 一起更新，避免只更新其中一个造成后续状态不同步；没有显式字段的
  线路不会因每次扫描而重复写配置。

生产记录（私有部署记录保留完整旧文件、容器 inspect、模块和通话证据）：

- Engine 候选/默认 digest：`sha256:e68b55d77e3f1d339cf66ddbe61dc97cbbe54a9c994629b52122a22c473dbd68`，
  iid1 当前容器 restart=0；Control 当前镜像 digest：
  `sha256:90e2ed5b6977da559247246bb434ee23b33ed879ae902da63f7f67b4e315b547`。
- iid1 当前 VPCD/Engine 快照统一为在线槽 10；`SWu=CONNECTED`、`USIM=AUTH_OK`、
  `PJSIP=Registered`、admission `allow`、活动通道为 0；四个 USIM 恢复残留文件已归档并从
  `run/` 清除。宿主 `pcscd` 已确认 active。
- AMI timer-only 原始回读包含 `ActionID` 且 `SentRegister: false`，证明没有重发 REGISTER。
- 不收费媒体 canary 通过：浏览器协议→Control→Asterisk WSS 双向 PCM，证据就绪。
- 依长期授权实际呼叫一次并已挂断：呼叫记录为 iid1 最新外呼、`status=answered`，Engine
  通道最终 `0 active channels / 0 active calls`，PJSIP 仍 Registered。验证脚本的 50 秒判断
  只在无消息时触发，实际持续约 74 秒后走独立 API 挂断；这是测试工具计时缺陷，不再重拨，
  详细输出在私有部署记录中。没有代发短信。

本地复审：`159 passed, 1 skipped`（卡片重连、USIM fence、AMI ActionID 相关集合）；全量
测试仍有历史环境缺少 `Crypto` 的既有失败，未把它伪装成通过。下一步只需用户刷新网页后验证
giffgaff 的页面拨号/短信入口；若仍失败，读取当前 Engine 世代的新证据，不沿用旧 503 或旧
fence 结论，也不重拨已授权号码。

## 2026-08-28 00:40：远程 VPCD `Card was removed` 修复已部署

中断前 Copilot 任务中已提交的 `0f8cc68ddb89a1cf034732c9fb30323322764399` 已完成一次整批
复审并部署。该提交只改 `engine/ami_usim.py` 对远程 VPCD T0/T1 `Card was removed` 文本的
精确分类，复用已有 `pcsc_card_reset` 有界恢复路径；相关恢复回归 171 项通过。

- 生产部署记录：`codex-20260828-vpcd-card-removed-0f8cc68`。
- 候选/默认 Engine digest：`sha256:b3d98f1b7121269a5539a85a6f01f95104590c6436d4a05f929aae6f8040eba7`。
- 候选 runtime fingerprint：`feaf634354d70ee0a025d67484df8d6157a84bf1940788cda6991828139686a5`。
- 两条受影响线路均已替换并提交；容器 `restart_count=0`，隧道 `CONNECTED`、USIM
  `AUTH_OK`、PJSIP `Registered`，活动通道为 0；默认镜像 revision 已绑定 `0f8cc68`。
- 部署前旧 Engine digest 和源文件均已在部署记录中保留，旧镜像保留为 `pre-0f8cc68`；未改
  Control、WebUI、通话挂断/计费逻辑，也未执行收费呼叫。

`next_action`：Free FR（iid7）需要用户在刷新页面后做一次新代际呼叫，才能取得此前承诺的
request/session/channel 阶段证据；该号码不在长期自动通话授权范围内，不由后台代拨。若仍失败，
只读采集新 Engine debug 并定位具体阶段，不凭旧 cause 38 重启或猜测运营商。giffgaff（iid1）
的本次部署只证明恢复路径和注册健康，尚未替代用户的人耳通话验收。

## 2026-08-28 01:32：外部号码被提前取消；calling 阶段租约修复已部署

用户随后用外部号码 `+33744930030` 重试。生产记录显示浏览器/Engine 媒体链路已建立，Engine
发出 `call_out`，但 14 秒后返回 `CANCEL/cause=0`；SQLite id 90 为 `status=cancelled`。
目标设备留下未接来电，证明 INVITE 已到达对端，失败点在本机提前取消，不是“未送达”。

根因是 Control 的 `_renew_softphone_call_lease` 在外呼仍处于 `calling`（对端尚未接通）时，
要求下行 PCM 也保持新鲜。等待振铃阶段本来可能没有下行语音，于是它停止续租 Asterisk 的
10 秒绝对安全租约，最终触发本机取消。此前约 14–18 秒的失败时长与该窗口一致。

最小修复提交 `87e15ea`：只有 `calling` 阶段允许在无下行 PCM 时继续续租；浏览器/Engine
精确身份、WSS 存活和 AMI 续租失败处理不变。进入 `active` 后仍要求原有双向媒体证据，并在
固定 10 秒宽限后终止，通话中断/停止计费路径未放宽。新增回归测试覆盖该阶段，相关测试
`277 passed, 20 subtests passed`；全量回归唯一失败仍是本机既有缺失 `Crypto` 依赖。

生产部署记录：`codex-20260828-native-ringing-87e15ea`。只重载 Control（`--no-engines`），
两条 Engine digest、容器 ID、启动时间和 restart_count 均未改变；新 Control 源文件哈希与
提交一致，旧 Control 镜像保留为 `pre-87e15ea`。部署后两条线路仍为隧道 `CONNECTED`、
USIM `AUTH_OK`、PJSIP `Registered`、活动通道 0。下一次外部号码测试应可等待完整的配置
`ring_timeout`；若接通，再验证 active 阶段的实际双向语音和物理挂断。

### 2026-08-28 01:25：Free FR 两次立即失败的现场证据

用户点击的两次呼叫均为 iid7 → `+33744910222`（该 Free 线路自身 MSISDN），不是外部测试号码。
两次均先收到 `POST /api/instances/7/browser-media/outbound/prepare` 200、浏览器媒体 WSS 与
Engine 媒体 WSS 成功建立，随后 Engine 在约 1 秒内发出终态；SQLite 呼叫记录为 id 88/89，
`status=failed`、`duration=1s`、`engine_run_id=a5b762a0-ca7b-427f-9113-73650b8abb3a`，
Engine `core show channels count` 显示已处理 2 次且当前为 0 通道。Control 日志的顺序是
`call_result` 先到、`native_media_closed` 后到，因此不是网页先关闭导致失败。

两次 Engine 事件的原始结果为 `CHANUNAVAIL`、Q.850 cause `22`；Asterisk 官方映射中 cause 22
是 `NUMBER_CHANGED`（SIP/PJSIP 对应 410），与拨打自身号码时的快速拒绝一致。当前只记录事实，
不把 `Registered` 当作通话健康，也不为此修改代码或重启线路。下一次应使用外部号码；若外部号码
仍立即失败，再开启一次有界 PJSIP 信令诊断，定位实际 SIP 响应，不沿用这两次自呼叫的 cause。

## 2026-08-27（晚，第三次）已部署：exhausted USIM-recovery fence 自动调和

Gap 1-4（`4d38625`）+ 独立复审后补的 TOCTOU 竞态测试（`2a185b4`）已部署到生产，
record=`codex-20260827-usim-exhausted-reconcile`。只替换 Control 侧 3 个源文件
（`engine.py`/`main.py`/`vpcd_slots.py`），`install.sh reload --mode docker --no-engines`，
**没有碰 Engine**（两个 Engine 容器 StartedAt/restart_count 均未变）。

部署前：独立复审 PASS（1 个 P1——TOCTOU 竞态测试覆盖不足，已补测试通过；2 个 P2——
`MaintenanceReservation` 暂无生产调用方，已加注释说明；文件系统故障注入测试留作以后）。

部署后核实：容器内 3 个文件哈希与 commit `2a185b4` 完全一致；`restart_count=0`；
运行 2 分钟、4+ 个轮询周期（30 秒一次）无 traceback/exception；iid1
`state=allow healthy=true`（新轮询确认无 exhausted fence 可清理，正确地什么都没做）；
iid7 同样不受影响；未做任何付费通话验证、未做 Engine replacement。

已知遗留：`/opt/mdd-gateway` 不是 git 仓库，用文件级 hash 校验源码一致性代替
`org.opencontainers.image.revision` 标签（这次是纯文件替换，没有走完整 `source.tar.gz`
流程，所以镜像标签缺少 revision 字段——下次如需更完整的可追溯性，建议走完整打包流程）。

`next_action`：观察一段时间确认没有误触发；case "同一世代仍卡住需要人工/显式换代"
（`vpcd_slots.MaintenanceReservation` 已就绪但没有调用方）仍待后续补一个操作入口。

## 2026-08-27（晚）iid1 exhausted fence 维护换代：Gap 1 已实现，未部署，未触碰生产

按下方"iid1 历史 exhausted fence"一节已确认的方向（复用 EngineReplacement，同镜像/仅
iid1/不 promote default）继续往前推进，逐个 gap 补齐，尚未合并成可部署批次，**生产环境
未做任何改动**：

- Gap 1（Control 拥有的 VPCD maintenance reservation）：已在 `control/app/vpcd_slots.py`
  新增 `MaintenanceReservation` 数据类型及 `begin_/validate_/clear_maintenance_reservation`，
  与既有 `RecoveryReservation` 互斥、持久化在独立文件 `*.maintenance-reservations.json`，
  `claim()` 已让它和 recovery reservation 一样阻塞槽位复用。新增 8 项单测
  `tests/test_vpcd_maintenance_reservation.py`，全部通过；全量回归 2144 passed（本机缺
  `Crypto` 依赖的 1 项是既有环境缺口，与本次改动无关，不是新增失败）。
- **实现过程中先后发现、又修正了一个设计误判**：一开始照抄 `RecoveryReservation` 用
  `current_identity`/`session_generation` 做校验，但 `VpcdSlotRegistry.current_identity`
  被设计为永不跨进程重启存活（`_migrate_record` 每次 reload 都清空它；`_active` 只在
  内存）。用户指出：ICCID 这类持久身份本来就该用数据库/`last_known_identity`（会持久化）
  恢复，不需要依赖那个"活的"字段。据此已改为按 `eid`/`iccid`/`imsi` 这些**持久**身份字段
  做 `durable_identity_digest`，`begin_/validate_maintenance_reservation` 现在同时兼容
  同进程活跃校验和跨进程重启后重新加载两种场景（新增测试覆盖两种路径），
  `host/mdd_engine_replacement.py` **不需要**额外的 Control 内部 API 就能独立校验。
  "这次具体故障事件是否还是同一个"这一层校验，继续交给 `engine.py` 里已有的
  `expected_recovery_identity`（`engine_run_id`/`auth_seq_baseline`/`campaign_epoch`），
  是分开的、互补的两层校验，不重叠。
- Gap 2（begin/stop containment 需要 exact exhausted proof）、Gap 3（旧四件套归档后
  fsync 清理）、Gap 4（target postflight 复验卡/route/ALLOW）：**尚未实现**。
- Gap 2（begin/stop containment 需要 exact exhausted proof）：`usim_recovery_containment_boundary`
  新增可选 `required_phase` 参数，传 `"exhausted"` 时额外要求记录当前 phase 精确匹配；
  不传时行为与之前完全一致（向后兼容）。
- Gap 3（旧四件套归档清理）：新增 `archive_and_clear_exhausted_usim_recovery()`，只在记录
  精确处于 `exhausted` 且 `engine_run_id`/`auth_seq_baseline`/`campaign_epoch` 与调用方
  持有的证据完全匹配时才生效；四个文件的原始字节+SHA256 摘要归档到
  `orchestrator/usim-recovery-exhausted-archive/{iid}-{txid}.json`（txid 幂等重放）后
  逐个 unlink+fsync 目录。与既有 `recovered` 态的 `_clear_usim_recovery_fence_unlocked`
  是两条独立路径，互不影响。新增 19 项测试全过，全量回归 2157 passed（仍只有那 1 个
  与本次改动无关的既有 `Crypto` 依赖缺口）。

**2026-08-27（晚，第二次）核对生产实际状态：iid1 目前健康，没有正在进行的故障。**
`admission-authority-status.json` 显示 `state: allow, healthy: true, restart_count: 0`；
四件套文件已不在 run 目录；`deploy-records/codex-20260827-iid1-stale-run-recovery/`
里完整保留了旧的三个 stale 文件（对应本文件此前记录的那次临时 stop/start，容器
`StartedAt` 与该记录时间戳吻合）。也就是说这次具体故障已经被那次临时操作实际解决，
本节继续实现的是**可复用的软件能力**，不是在修一个正在发生的故障。

**架构决策（用户拍板，2026-08-27 晚）**：不做"必须显式发起 EngineReplacement 换代"
（方案 A，原计划），改做**自动调和**（方案 B）——原因是唯一能决定"清理是否安全"的逻辑
事实，其实只有"记录的 `engine_run_id` 是否还等于当前真实在跑的 `engine_run_id`"，跟是否
真的做过一次换代无关。Docker 自动重启已经会产生新世代，不需要再造一次。

- **Gap 4 已按方案 B 实现（不是 postflight 校验，而是自动调和）**：
  `engine.reconcile_stale_exhausted_usim_recovery(iid, *, txid, ...)`：只在
  ①记录精确处于 `exhausted`、②记录的 `engine_run_id` 与 `run/engine-run-id` 文件里的
  当前值**不同**、③当前世代独立证明 `usim_status.state=="AUTH_OK"`（且 engine_run_id
  对应当前）、`registration_state()=="Registered"`、`active_channel_count()==0` 时，
  才调用 Gap 3 的 `archive_and_clear_exhausted_usim_recovery` 归档清理；`engine_run_id`
  没变、当前世代不健康、或读不到当前世代，都原样不动、不报错。`registration_state`/
  `active_channel_count` 走依赖注入，测试不用碰 Docker。18 项测试全过。
- **接入点**：新增独立的 `control/app/main.py:usim_exhausted_fence_reconciler()` 后台轮询
  （30 秒一次，`MDD_USIM_EXHAUSTED_RECONCILE_SCAN` 可调），作为**完全独立**的 lifespan
  task，**没有改动**已有的 `usim_auth_recovery_reconciler`/`_reconcile_usim_auth_recovery`
  （那条是驱动"进行中的恢复重试计时器"的复杂逻辑，已经过评审，不应该在这次改动里被牵连）。
- **通知**：按用户要求，触发清理或者卡住时都用现有 `notify_push.dispatch(...,
  EV_HOST_ALERT, ...)`（"网关主机异常"分类）通知用户，每线路按 `HOST_ALERT_REPEAT_SECONDS`
  限流，不会刷屏。5 项测试覆盖：成功归档通知、被挡住通知、正常情况不通知、限流生效。
- 全量回归 2169 passed（同一个既有 `Crypto` 依赖缺口，与本次改动无关）。
- `next_action`：这批 Gap 1/2/3/4 加起来已经是一个完整、可独立工作的自动化能力，
  **建议作为一个整批做一次代码复审**（不是再拆更小的补丁），复审通过后就可以部署
  ——不需要等 iid1 再出故障，因为这个能力是纯粹增量式的旁路轮询，不改变任何现有
  正常路径的行为，部署风险主要在于"新轮询本身是否会误触发"，复审应重点核对这一点。

## 2026-08-27 当前纠偏：317 已回退；国家出口 resolver 修复待部署

### 2026-08-27 用户纠偏：giffgaff 是全程破音，恢复音频修复

- 用户明确纠正：giffgaff 的体验是从接通到结束全程结巴/破音；WSS burst/gap 只是采样到的
  帧到达证据，不能把用户故障描述缩成“偶发突发破音”。
- 317 后无法呼叫已证明由 sing-box 国家出口配置失效造成，不是 native rebuffer 被通话证伪。
  因此在出口恢复后，提交 `c95a603` 正式撤销此前 revert，恢复 native/canary 60–200ms
  underflow rebuffer；cellular 路径仍不启用，已由用户确认正常的 EC20 不受影响。
- 组合版本已作为 Control `c95a6035675c3de951504d210c43de084383ed06` 运行，record=
  `codex-20260827-uk-audio-e2e-probe`，镜像/容器标签和入口 `index-DPbrFkGA.js` 均已核对；
  restart=0、两 Engine 零通道。官方 reload 仍因同一个 iid1 exhausted fence 在最终 authority 门
  非零退出，故不称 finalized，也未实拨。
- 同一版本包含 `8d2113a`：Cloudflare/Google 两个 E2E 请求均发出，任意一个合法 answer
  即通过并返回实际目标，只有两者都失败才失败。相关 Python 108 PASS+16sub，17个WebUI脚本、
  组合build、15项dist sums PASS。运行候选实际 FR/GB/HK 均 HTTP200并返回成功目标；
  `next_action`：处理 iid1 既有 exhausted fence，最后一次真实 giffgaff 双向语音/挂断验收。

#### iid1 历史 exhausted fence：保持 fail-closed，不能直接清理

- 现场进一步证明旧流程已在 exhausted 时清空 persistent VPCD recovery reservation，却遗留
  recovery/fence/dispatch debris；当前 VPCD session generation 也已改变，虽仍是同一 ICCID。
  因此不能原位补写 recovered、timer rearm、手删 fence、普通 restart 或 docker restart。
- 双预审一致认为最小安全方向是现有全局 EngineReplacement wrapper 的“同镜像、仅 iid1、
  不 promote default”换代，但现有代码尚有四个真实阻断：Control-owned current VPCD maintenance
  reservation；begin 与 stop containment 两处都需 exact exhausted proof；旧四件套须先完整归档再由
  maintenance target-start 清理并 fsync；target postflight 再验当前卡/route/ALLOW。当前没有可直接
  执行的更小现成路径。
- 本轮曾实现的原位 late-result 草案未通过上述 reservation 边界复审，已完整撤销；相关原测试
  54 PASS，工作树干净，未部署、未改生产 fence/Engine、未拨号。
- `next_action` 需要作为一个明确的 Engine maintenance-reservation 批次实现并整批复审；这会扩大
  Engine replacement 协议与 VPCD claim 接入范围，不能伪装成几行 fence 清理继续执行。

- 用户实测 317 native dejitter 后 UK/FR 都无法完成呼叫；生产 Control 已回退到
  `864c84f7b850defb8440bbd6a58f5cc9d8b6c711`，入口 `index-Q0USWVih.js`，restart=0，
  deploy txid=`codex-20260827-d2-media-audio-864c84f`。Git 以 `35f49e7` 正式 revert 317；
  317 不再是候选，不重放其部署或测试结论。
- 同期主阻断已由现场错误确定：host sing-box 升到 1.13.19 后，旧生成配置缺少
  `route.default_domain_resolver`，FR/GB/HK 三个实际国家出口全部 `ready=false`。节点库的临时
  UDP 测试与“已应用国家出口”不是同一契约，节点通过不能证明 VoWiFi 所用出口运行。
- Host 修复已提交为 `30246fadfa728fb38ba0c6b102e4c3f88ef3c012` 并有记录发布到生产，
  record=`codex-20260827-egress-resolver`。发布前旧文件 SHA 与冻结基线吻合；备份保留，发布后
  FR/GB/HK 三个实际出口均 `ready=true`，日志出现 UK/FR ePDG UDP 500/4500 经相应
  shadowsocks 出口，UK/FR Engine 均零通道且分别 Registered。Registered 仅是恢复注册证据。
- 该修复给有国家出口的配置增加唯一 local `dns-bootstrap`，并让
  `route.default_domain_resolver` 指向它；各国 `dns-CC`、`detour: exit-CC` 和规则不变。
  生产 1.13.19 对同一现场 desired 生成的候选配置 `sing-box check` 已 PASS，旧配置错误已保留。
- 同批把 UDP probe 改为 Cloudflare `1.1.1.1` 与 Google `8.8.8.8` 两个独立目标都发出，
  任意一个合法 answer 即证明当前已应用出口 UDP 可用，只有两者都失败才报错；
  校验 SOCKS 目标/53、DNS transaction/question/QR，并在总 deadline 内忽略迟到重复包。
  页面把 `ready` 改称“出口已启动”，节点临时测试与已应用出口端到端测试分开命名。
- Control/WebUI 候选已提交 `76789b85ee8b7b4328ad8fe723d2fc50b106aa14` 并运行，
  record=`codex-20260827-applied-egress-probe`，镜像/运行容器/源码标签均绑定该提交，restart=0、
  unless-stopped，实际服务入口 `index-C7p9Q8SP.js`。相关 Python 107 PASS + 16 subtests；
  17 个 WebUI 脚本、Vite build、13项dist sums及双复审 PASS，P0/P1/P2=0。
- 已部署版本的真实已应用出口双 DNS API 探测：FR 268ms、GB 294ms、HK 283ms，均 HTTP 200。
  下一候选保留两个请求但改为任一合法 answer 即通过，并返回实际成功目标；仍不是旧节点库假绿，
  也不等于 IMS/拨号健康。
- 官方 reload 最后以非零退出并保留 fail-closed：iid1 存在此前 `usim-auth-recovery` exhausted
  本地 fence，authority 对 iid1 为 `deny/allow_not_proven`，iid7 为 allow。候选 Control 实际稳定
  运行、三出口 ready、两 Engine 零通道；该失败不能伪装成 finalized。fence 不是本批 probe 产生，
  不手删、不以 Registered 绕过。`next_action`：单独评审 iid1 exhausted fence 的产品恢复语义，
  然后在安全闭合后做一次已授权 UK 实际语音验收；FR 无已授权目标，由用户验证或另行授权。

更新：2026-08-27。媒体容错 `57ada662fbc45d08ae12b075b7b88ceba69a7b1e` 已部署；勿重放部署。
不要把 Registered、能力旗标、模拟 PASS 或镜像哈希当成通话健康；也不要重放已完成部署。

## 2026-08-27 追加验收：香港已好、英国dejitter已部署、法国待当前代际复测

- 用户人耳确认两台香港EC20已无破音，本批cellular Control去二次pacing可关闭。部署后软件源/汇
  iid5完成48秒双向语音但物理idle 51.044秒，严格保留FAIL/overrun；iid6双向语音/48.013秒idle
  PASS。最终两台fresh authoritative idle、paid0/server channels0，不重拨这些marker。
- giffgaff用户实测从头到尾结巴破音。新诊断实拨PASS（45秒、RTP双向增长、非回声语音、精确
  清理），但2021个下行WSS帧中985次<5ms burst、244次>30ms gap，P95=52.54ms、max=2466.56ms；
  证明运营商/RTP在流动，浏览器Worklet empty→silence把burst/gap变成逐字glitch。提交3170373
  仅给native/canary启用60–200ms有界underflow rebuffer，1000ms设置对应200ms；cellular仍即时，
  silence/eviction不计媒体证据。17个WebUI脚本/build/dist哈希与双复审PASS。
- native dejitter已用Control runtime overlay部署，record=`codex-20260827-native-dejitter-3170373`；
  source/revision/deploy-txid与bundle哈希正确，pinned HTTPS=200，Control restart0/unless-stopped，
  paid0/zero channels；Engine1/7容器ID不变且StartedAt早于Control切换，未做Engine replacement。
- Free FR用户三次旧代际呼出均约17–18秒 `CHANUNAVAIL/cause38`，从未call_active。固定sysmocom
  源码表明38保留在PJSIP request/task/channel创建失败、INVITE尚未形成；tunnel CONNECTED、接口
  zero drop/error、当前P-CSCF OPTIONS 200。三次失败属于旧Engine run；该线随后10:09自动进入
  新run并Registered，尚无新run呼叫结果。当前新run仅开启`chan_pjsip.c` debug 5，30分钟后自动
  关闭，不记录SIP正文。
- `next_action`：用户用刷新后的页面分别听一次giffgaff超过25秒，并在30分钟窗口内拨一次Free FR。
  giffgaff若仍结巴，保留固定200ms结果后再评审自适应NetEq，不扩大当前批；Free结果出现后只读
  当前Engine debug，定位request task/session/channel哪一步，不凭旧cause38重启或猜运营商。

## 2026-08-27 冻结候选：浏览器媒体重接、D2 状态机与 EC20 长通话音频

状态更新：候选已冻结为 `864c84f7b850defb8440bbd6a58f5cc9d8b6c711` 并完成有记录部署，
deploy record=`codex-20260827-d2-media-audio-864c84f`。Control/source cutover、iid1+7
Engine replacement 和 finalizer 均成功；新 Control/两个运行 Engine restart=0、unless-stopped，
旧 Control stopped/no-restart，逐线 admission ALLOW、零通道/零通话/零付费、无活动事务；
用户设置仍为1000ms。不要重放本次 cutover、wrapper、finalizer 或实拨 marker。

- 设置中的 `cellular_audio_buffer_ms` 仍按每通新会话快照贯穿浏览器发送/播放和 Control
  双向队列；默认 500、范围 100–2000，生产已有 1000 不会被覆盖。
- Native/蜂窝浏览器媒体仅对异常关闭码 1006 允许原 owner/session 在断线时刻起固定 10 秒
  内重接；ticket 轮换、connection epoch CAS、deadline 不滑动，且必须重新得到真实双向媒体
  证据。正常关页、鉴权/协议/代际错误及 Asterisk/Agent 后端腿断开仍立即走唯一停费清理；
  不重复 Dial/Answer/commit。
- D2 已闭合 Asterisk timer/transport/FullyBooted 绕过、request-owned permit、durable dispatch
  receipt、dispatch→P-CSCF 跨 send 锁、AUTH/rearm/recovered/exhausted 顺序、所有 normal start
  fence、Engine replacement paid/channel containment、failover campaign generation，以及 VPCD
  current/last-known 分离和每槽持久 route reservation。Reservation 只有 Engine 先持久
  `recovered/exhausted` 后才可精确清除；坏文件、late AUTH、route/card/config 漂移全部 fail-closed。
- 两台 EC20 约 20 秒后持续破音的新实测对应一个确定的公共反模式：浏览器 AudioWorklet 与
  modem/miniaudio 已由各自硬件时钟驱动，Control 又双向固定 20ms pacing。候选只移除 cellular
  Control 第三个时钟，保留 queue/age/oldest-eviction/send-timeout/evidence/唯一 release；native
  Asterisk pump、Agent 和浏览器不改。尚未实拨，不能称破音已根治；若 50 秒仍异常，再评审
  adaptive jitter/resample。部署后软件源/汇实拨结果：iid5在48.001秒deadline后结束，真实上行
  36.64秒、下行43.64秒、helper回调1075次、语音完整且双向非静音，物理idle在51.044秒，因
  超50秒验收上限原结果保持FAIL/overrun，但cleanup已确认且unknown=false；iid6在45.005秒释放，
  上行34.06秒、下行39.2秒、helper回调1025次、真实双向语音，48.013秒物理idle，PASS。
  最终两台均fresh authoritative idle，服务端零租约/零通道。该证据证明未在20秒后中断或停止
  PCM增长，但不是用户浏览器扬声器的人耳音质验收；仍需用户页面听一次确认“持续破音”消失。
- 最终本地门：Python `2133 passed + 141 subtests`；本机缺 Crypto 的 1 项在 Engine validation
  image 内 PASS；17 个 WebUI 脚本、Vite build 和 dist sums PASS。私有 runner A 的验证镜像
  build=0（仅 runner 验证副本预展开 PCSC、关闭声音包并跳过受阻的 mitshell Git 包；产品
  Dockerfile未改），现有 admission E2E 与新增 registration-fence E2E 均 PASS，后者覆盖连续
  fenced timer 碰撞、exact 单 REGISTER、replay 零第二包、P-CSCF 竞争零 send 和 timer-only rearm。
- 最终独立复审均 PASS，当前范围 P0/P1/P2=0。Agent软件/设备配置未改；两台EC20各执行上述
  一次已授权收费验证并已停止。`next_action`：用户只需从实际网页/扬声器听一次超过25秒的EC20
  通话，确认主观破音是否消失；若仍破音，保留本批结果并仅进入延期的adaptive jitter/resample
  评审，不重做D2、部署或当前实拨。

## 当前正在交付：通话缓冲一致性与短暂卡顿恢复

用户确认三条真实故障：giffgaff不能呼出、4054接通后卡顿且数秒结束、4541报stall类错误。
已有用户通话日志保留；浏览器release请求不能直接归因为用户主动挂断。测试每次允许50秒，
所有收费目标只读私有授权文件，结束必须核对实际通话停止；本批测试进展见本节末尾。

- 预审后已改：蜂窝页面不再因单个degraded状态立即release；保持真正关闭/错误/所有权清理。
- 同一设置`cellular_audio_buffer_ms`驱动新蜂窝/VoWiFi会话：浏览器发送、播放，服务端双向蜂窝/
  原生上行队列与Asterisk拥塞判断。默认500，范围100–2000；保留用户现场1000。
  已有通话保持创建时快照，不在通话中重配；这不是系统TCP/驱动缓冲或总延迟配置。
- 已发challenge保留原5秒有效期（最多8条），过期回复不计新证据、不立即断线；音频满队列
  原子丢最旧帧，不阻塞后续心跳，不刷新语音进展。帧龄在真正发送锁取得后仍检查。
- 正常蜂窝发送允许原10秒失联窗口，清理通知/关闭仍0.5秒；原生媒体短暂不就绪不提前挂断，
  但不续租，期限固定于最后成功续租。身份/代际/关闭立即清理；Agent原12秒本地租约未改，
  不能声称物理停止精确10秒。STATUS短暂漏答可恢复；拥塞按已有FLUSH_MEDIA清队列，等真实状态。
- 按[Asterisk官方WebSocket文档](https://docs.asterisk.org/Configuration/Channel-Drivers/WebSocket/)
  和[浏览器bufferedAmount语义](https://developer.mozilla.org/en-US/docs/Web/API/WebSocket/bufferedAmount)
  核对现有实现；本地回移版FLUSH不自动解除paused/full，按实际代码处理，不升级依赖。
- 保存提示15秒、可手动关闭。17个前端脚本通过；最新后端377项＋37子测试通过。
  首次满队列回归1失败/46通过已保留：测试将新帧排在满预算尾部后误期望必达；修为先确认
  旧队列处理完再验证新音频恢复。没有放松过期丢弃规则。额外独立10秒/STATUS回归仍在完成。
- 最后同批补齐：蜂窝同lease续租的ModemTimeout仅在固定10秒剩余期限内重试，明确拒绝立即清理；
  页面状态GET暂败不再提前断开仍活跃的媒体WS，401/403仍立即清理。没有自动重拨。
  扩展13文件534项＋39子测试通过；独立18项虚拟时钟测试通过，含6秒恢复、10秒失联、
  身份变化立即关闭及FLUSH不伪造进展。独立测试首次18项失败是fixture非法身份，原输出保留。
  新前端入口`index-CTA2zyK1.js`；本批未部署的中间DoSY产物已移除，历史已部署资产保留。
- 最终合并运行14文件552项＋39子测试、17个前端脚本通过；独立整批复审PASS，无遗留阻断。
- 新bundle已构建，11项校验；98个镜像运行文件与冻结Git逐一核对通过。复用既有构建/部署
  流程完成：core1600→Engine事务`engine-replace-1787765111-44cf38375e72`两线verified/committed
  →finalize complete；16个生产文件/新UI哈希一致，三容器restart=0，正常重启策略恢复。
  13文件配置/一致SQLite快照已离机，旧Control保留。预检曾遇旧法国Engine自然重建、收尾曾遇
  额外线路自动探测未能读取通道数，失败均保留；等待自然恢复/停止后通过，没有豁免检查。
  Control OCI `8a1300b2ea5963ae78f4d8d10453494a312c1a5c1a07b7066e925c4eb31e279f`；
  Engine OCI `59c1c18f58f51337cb6c3d0f85c60fac73725521b2f671acfc1bf7fc0194c1b6`。
- 4054已实拨一次：准备响应1000ms，实际收到43.32秒下行、完整发送44750字节测试语音，
  真实helper采集/播放各增长1075次；没有数秒自动结束。48.0065秒发起挂断，但51.1511秒才
  确认设备空闲，超50秒测试限，原结果仍FAIL，不假报通过。独立复查fresh authoritative idle、
  terminal_samples16、audio=false、media=null、全部付费租约0；当前没有活动测试通话。
  不重拨4054。后续测试改45秒收尾、50秒硬界，原失败记录不改写。
- 4541也已实拨一次，准备响应1000ms；控制WSS断开并换SID，测试在1.452秒主动进入收尾，
  未观察到active。13.475秒后已有fresh物理idle，但旧SID清理判定始终不匹配新SID，90秒
  诊断确认窗结束后原结果FAIL/unknown，不能改成PASS。随后独立确认同设备新SID的fresh
  authoritative idle、terminal_samples116、audio=false、无media字段，以及服务端全部付费租约0。
  两次通话均已停止；giffgaff本批未拨打。不要重放本批5/6标记或跳过未知结果去继续。
- Windows只读证据排除了服务重启、旧/混装包及普通45秒心跳超时：同一进程，7/7安装文件
  哈希匹配批准包；只有modem控制WSS失去远端后重连。没有足够证据归因VPN、CSFB或服务器。

### Control连续性批次已部署并完成实拨验收

双预审通过Control-only最小恢复：仅已提交call、原浏览器/PCM仍活、原Agent/Modem身份一致，
才可在原lastHealthy+10秒内等待控制连接重连。新SID须经原lease真实续租及前后Attachment
CAS确认，不自动等价旧owner，不重拨或重开音频。挂断/终态查询复用身份门，禁止误挂同ICCID
的新设备或用其idle清旧租约；不升级Agent、不改DB表结构或前端。
同批修复已实证的恢复任务异常：真实Attachment没有online属性，改用is_online()；旧代码
3RED/1PASS，修后相关153项＋19子测试通过。最终16文件606项＋45子测试通过，独立复审
291项＋25子测试通过，无本批P0/P1阻断。关闭RAM owner被持久恢复循环绕过身份门的问题
已同批闭合；异设备不再收到其hangup或供给终态证据。上述已作为
`7b5d603910cc8c92606dd1e6d117d0c0b3c6185a`部署。
首次core在第二次DENY检查未证实时停止，失败记录保留；双复审的一次性续接助手只执行原未运行
尾段，随后原Engine事务两线verified/committed、finalize/postflight complete，没有重建事务或清fence。
Control OCI`35144d4dd9dfe8b8ac75cf8ab225cb8c59cef94c03d39b82fa60d4fba61b50cd`；
Engine OCI`611a82f10dc8d22334d141fa0f97fcc93692ea9cbb86ac6d45075cdbf0e763fd`。

新批4541实拨PASS：1000ms、active、双向语音，45.010秒挂断、47.662秒物理idle；本次没有SID变化，
不能冒称实机重连注入验收。英国giffgaff首次实拨PASS：1000ms、非回声双向语音和同一通话RTP
双向增长，45.006秒挂断、45.726秒零通道。最终三容器restart0、两Engine零通道/零通话、付费租约0。
旧5/6失败记录保持原样；新批两个结果使用独立一次性标记，不重置旧标记。
边界与延期项见postponed-tasks.md；尤其不能把控制WS恢复说成整条媒体WS断线可续接。

  浏览器工具已有明确访问策略阻断，不绕过；API/WSS实机测试不冒充页面或用户声卡验收。

## 上一批已部署：首次语音能力请求失败的有界恢复

当时运行源码 `a8b9faa18cb8192bb11bea358cd28d10872fca0b`，已被上方57替代；不重放此部署。

现场只读API确认UK1/FR7当前softphone原生入/出媒体入口可用；HK5/6的VoWiFi停止是用户配置，
蜂窝能力独立。不能由此宣称浏览器页面或运营商通话健康。
当前代码反例：初次GET失败后100条状态消息＋WS重连仍只有1次能力请求，prov=null一直禁拨；
未完成GET又因没有超时阻挡fresh trailing。不是把它认定为此前截图的唯一根因。

- 已预审、实施：复用现有KeyedTrailingRequests，softphone GET用已有AbortController 8秒超时；
  可选1/3/8秒最多三次重试，仅网络/408/5xx；401/403/404/429不重试。耗尽后明确失败＋手动Retry。
  普通snapshot不重置预算，WSopen仅补无prov且无call的线路；清理/旧epoch/单inflight＋单timer隔离。
  没有收费动作自动重试，也不改现有通话所有者或挂断协议；默认无重试的其它调用保持原语义。
- 16个WebUI脚本通过，含真实API abort、取消/重加/旧timer/fresh trailing回放。
  新构建入口index-Cfd9esKs.js，9个dist校验通过，旧D2/D96保留。整批复审PASS，已正式部署。
  独立生产WS回调反例反转通过：失败耗尽后重连恢复缺失能力，已有通话owner未触碰。
- 已核对[React官方版本](https://react.dev/versions)与
  [Effect清理/竞态指南](https://react.dev/reference/react/useEffect)，以及
  [SWR有界重试接口](https://swr.vercel.app/docs/api)：当前18.3.1、最新19.2.7；
  升级React不会替手写fetch加入恢复。只借鉴重试/取消语义，不引入新库或升级依赖。
- TODO.md旧入口错误地指向历史长任务板，已改为本文件，避免压缩后重放历史任务。
- 部署记录 `data/deploy-records/codex-20260826-provision-recovery`：core1600未改；
  Engine事务 `engine-replace-1787758028-81aba5b8b99c` 两线verified/committed，finalize complete。
  Control OCI `5599ac6a460d33f23e353159f6678a8c9a9c503a79a897ee7785b3eb911f1bcc`；
  Engine OCI `f90435757fdf490ca7ed70fefcfc92a2a2ef3bcae5eeefdcd9c27d5e75ed384f`。
  独立实机复核10个源文件／新入口与HTML哈希、默认镜像及restart策略正确；三容器restart=0，
  无维护事务／未结束付费租约，两个Engine零通道／零通话。旧版本和13文件配置／SQLite快照已离机保留。
- 打包器曾误将两个前端测试纳入运行清单并被严格拒绝；仅排除这两个已审测试，完整Git快照仍含测试，
  未知额外文件仍拒绝。部署助手18项通过，源包严格10成员；镜像96运行文件与冻结Git核对通过。
- 部署后UK1/FR7 softphone GET 200、原生入出媒体能力可用；HK5/6 VoWiFi停止是原配置，
  两台蜂窝Agent在线且信令／音频能力及就绪标志为true。本批没有收费拨号，不能把这些旗标当通话验收。
- 浏览器真实页面、麦克风／扬声器及多端呼入仍未验；新资源已部署不代表旧活动标签已刷新。
  不重放本批构建／部署或已完成的收费实验。

## 工作区保留：源码快照与本地入口

6棵旧树已保存并逐树恢复验证：1741个路径（1733个当前文件、8项删除），含309个未跟踪文件。
保存HEAD、原index、staged/working binary patch、当前文件包和共享Git bundle；
新目录恢复后的内容、执行位、删除与原件一致，原树HEAD/index/diff/纳入文件哈希未变。
外置盘快照另有私有跨卷副本，46个归档文件逐字节核对一致；manifest SHA
`e69863fc989e7d061337c9492ada3c3385663635ae73fc36510df6aa7b92e5ae`。

这是**源码快照，不是完整运行现场备份**：ignored中的配置、SQLite、镜像、Agent二进制和缓存
明确列为未备份。原树未移动、删除、重置或锁定，不能据此删除原件或重放旧运行事务。
私有索引目录为 `mdd-worktree-preservation-20260826.oPwb6x`，恢复边界和包路径均在其记录中。

原默认目录没有当前任务板，容易从旧基线继续；已补仅本地的AGENTS.md指向本工作树和本任务板。
该文件由仓库共享info/exclude忽略，不进入Git；这个忽略项作用于本仓库全部worktree的根AGENTS.md，
不影响已跟踪文件。本轮只新增原默认目录的入口，未修改全局配置或其它树的源码。
按[官方AGENTS.md规则](https://learn.chatgpt.com/docs/agent-configuration/agents-md)选择项目级入口；
没有为验证它另启新会话，也不声称已改变当前会话启动时读取的指令。

## 上一批已关闭：ab84 缓冲配置与 4054 Agent

唯一工作树／分支仍是下述 forward-runtime；本批当时运行源码
`ab84baaaf01c96b344189276b1a4fd8297336cf1`，现已被顶部a8b9faa替代，功能保留。
后续任务板提交仅记录结果，不需要再构建或重放部署。

- 系统设置 → 通话与 VoWiFi：蜂窝音频排队上限默认500ms，严格整数100–2000ms；
  只在新媒体会话分配时读取，已有会话不变。1500ms有配置持久化／媒体阻塞测试。
  这是本地排队余量，不是网络RTT或端到端延迟上限；过期帧丢弃后继续接收新音频。
  六帧队列、真实发送I/O超时、媒体新鲜度和停止计费保护均未放宽。
- 4054(iid5)原Agent缺少call_contract，已用现有获批1.3.13包升级；身份／配置／秘密不变，
  独立持久备份保留，服务Running/Auto。4541(iid6)未重复升级。
- 批量回归654项＋65子测试通过，独立201项＋17子测试、16个WebUI脚本通过。
  部署助手15项通过；镜像95运行文件与冻结Git逐一对应，8个dist文件校验通过。
- Control OCI `4b4d2bd205bf8f7a2b9f32c7d30f33c819c600fd15ca81dd0d532e0d7c8b78d8`；
  Engine OCI `3cc4f1566f35e881d7034da6f62f474581f684444e4a52273758c893bc954c7c`。
  新入口 `index-D2Lghu8n.js`（SHA `aacb9a0be880bd8e5ca4b920a8ed537ec66bb1bee56fd607fc12c166646e4562`）；
  旧D96保留，HTML只引用D2。旧版本和配置／SQLite快照已保留并离机校验。
- 正式记录 `data/deploy-records/codex-20260826-mobile-pcm`：core1600成功，事务
  `engine-replace-1787756261-eda60805aac0`两线路verified/committed，finalize complete。
  实机再次核对9个源文件和5个容器运行文件，三容器restart=0／unless-stopped，
  无维护事务／未结束付费租约，两个Engine零通道／零通话。
- 4054和4541分别一次无收费prepare→WSS PCM→cancel通过，总6.734s／7.411s。
  实际双向转发171/200帧及170/174帧，两台真实helper采集／播放均100次回调；
  新会话读到500ms，2001被API拒绝。独立复查两台fresh authoritative idle、audio=false、media=null。
  没有commit/answer/dial；这不是收费实拨、浏览器麦克风／扬声器或长通话音质验收。

私有完整收据：`mdd-mobile-pcm-deploy`、`mdd-mobile-ab84baa.b8306N/BUILD.md`。
下一步回到已有主流程验收；不要重新升级4054、重放已消费的收费测试或重建本批产物。

## E6 历史源码与现场（已被上方 ab84 替代）

工作树：`/Volumes/micron512g/tmp-project/codex-audit-tmp/mdd-forward-runtime-20260824`
分支：`codex/forward-runtime-20260824`。用户原始工作树未动，没有 push。
运行源码：`e6e9379f014d621ad2107f5bd2fd0c06a8d9d6e1`；后续任务板提交仅是记录，不需要重新构建。

| 组件 | 运行产物 | 容器 |
| --- | --- | --- |
| Control | OCI `a048d01063f46bbaa22ff030b1f21de21d52000f98c47437e3142e6d529e1763` | `14708309b453adadfe2ab342289b3ff661d299ddb89abaa154385c0ff2c93d55` |
| Engine | OCI `e778b32c2f9a88b2a85207114797bcd00663f83be24e90a887d35646357b77eb` | UK1 当前 `fa082781e79489285ec0ba087aa5325438268807cd51981423fafa6785a64101`；FR7 `eeed6ca300be96ddf7b1021e3dfe664e96f83b9bdbd55f969d7fdb2d3580511e` |
| WebUI | 沿用入口修复 `index-D96mFEH8.js`，7文件清单 SHA `3986e11d…` | 未因文档或 PCM 修复改包 |

三容器回读 restart=0、unless-stopped。Control 的实际 call_media.py SHA 为
`0e0df5ab0fec9f14cb5ed30adbd526154989628e345e5934609ea7bbbce0fe43`。
UK 初始 E6 容器 `f79848b9…` 于12:51 UTC因两次注册无响应被现有有界恢复自动换代；
不是新的部署，镜像未变。不能用新容器restart=0掩盖这次换代。

## 本批修复与部署闭合

- 716入口修复已关闭：正常契约是state=OK/label=Working；前端不再拿显示文字误禁拨。
  UDP测试的包导入错误已修，真实4节点测试通过；不是用户密码或UDP配置错误。
- E6保留一次Asterisk原生注册重试，期限锚定首次事件；未知样本不能不断刷新期限。
  重键采用认证后的双入站SA过渡、worker安装回执、同MID有限DELETE重传及旧SA退休。
- D2不再借Engine配置伪造当前卡身份；真实空闲PC/SC读取持既有锁、LEAVE与代际CAS。
  实际API已确认UK槽10／FR槽1 online、identity_current=true，身份代际等于连接代际。
- HK真实拨号曾在signalling阶段报 `cellular PCM jitter queue overflow`。
  合法成批回调会在consumer尚未执行时填满6帧队列；已改为有界入队背压，保留原收到时间
  和跨块残余年龄，6帧／0.5秒I/O／0.2秒年龄上限未扩大，真正阻塞仍停止并清理。

最终40文件回归1143项／125子测试通过，16.19秒；PCM独立复审96项通过；部署辅助包14项通过。
正式记录：`/opt/mdd-gateway/data/deploy-records/codex-20260826-reliability`。
原core1600 → 原EngineReplacement(scope1+7) → finalize均完成；事务
`engine-replace-1787744974-20e08cc8314f` committed，批准计划SHA `381b052e…`。
旧Control、旧源文件、完整create-spec、数据库均留存；13文件配置／SQLite快照已离机校验。
额外线路4曾自然启动、未就绪后自然停止，预检／收尾均曾据此拒绝；没有手工停它或放宽检查。

## 实际通话证据：分清证据层级

| 测试 | 实际结果 | 挂断／限制 |
| --- | --- | --- |
| 英国新指定号码（E6切换前） | 实际answered；完整发送2.796875秒TTS，收到4.68秒非静音、非暖机标记下行；同call单次RTP Rx138/Tx193 | active后约5.056秒主动挂断，0通道／0通话，精确记录已结束。第二RTP采样尾部与挂断重叠，原脚本ok=false保留，不能误报业务拨号失败，也不能宣称测得两次增量。 |
| 香港EC20、iid6（E6部署后） | 实际active；完整44750字节TTS上行，4.18秒非静音下行；真实Agent采集／播放计数均增长；脚本PASS | 总14.515秒，确认fresh authoritative idle、media=null、终态采样≥2、无未结束租约。 |
| 英国新指定号码（E6后、自然换钥及SIP重连后） | 一次新验收case；实际active，完整44750字节TTS，6.92秒下行／AC-RMS1746；同一AMR通话RTP Rx196→316、Tx274→386，脚本PASS | 总16.376秒，8秒active上限；主动owner挂断，精确记录44有end_ts，同代0通道／0通话；13:32:13 UTC独立复核零残留。 |

这是正常鉴权API／WSS与真实运营商／设备的测试，不是浏览器页面点击或用户麦克风／扬声器验收，
也未证明对端人耳听见或识别语音内容。原始录音、控制帧、失败与独立复核均在私有记录。
E6后另有英国无收费WSS/Asterisk回声通过并清理，未为补统计重复收费拨号。
新增换钥后实拨的独立复核确认暖机标记残留0、逐帧原样TTS回声0；但原RTP统计有RxLost16、
TxLost0→11，不能称无丢包、音质或长通话已通过。接通／双向媒体／精确终止仅按本次有限窗口验收。

HK前次队列失败、媒体瞬态失败、一次客户端CSRF头拼写错误造成的403都保留，不能改写PASS。
403发生在创建通话前，0提交／无call_id；已修的是测试请求头，未把它归咎产品。

## 凭据与测试授权

管理员凭据及最新指定测试号码在Git外的私有目录，目录0700、文件0600；全局AGENTS有使用规则。
用户已长期授权这两类指定短时测试，不再重复索要；号码替换以私有授权文件最新值为准，
停用号码不再作为测试配置。每次必须控制次数、独立看门狗、实际语音数据及物理终止证据。
正常登入的CSRF头是 `X-MDD-CSRF-Token`，不得臆造；凭据不进Git、镜像或公开记录。

## E6 有界观察及上游对齐：已完成，不重放

2026-08-26 UTC：英国12:22:46、法国12:24:07安装新CHILD SA；分别12:22:46、12:24:08
收到经认证的旧SA DELETE ACK。法国随后仍回答对端DPD。收尾读回三容器仍为原代际、
restart=0，两个Asterisk均0通道／0通话；无新增IKE初始化、未见旧NoResponse→Control重建链。
这只证明本次空闲周期没有重建，**不证明长通话媒体连续性**。

英国12:23:14与12:28:50各有 `PJSIP transport 'volte_ims' failed.`。固定上游源码复审确认，
断开回调清VoLTE子状态但可保留通用Registered；随后几条Missing Security-Server是候选检查，
不能单独当成整体注册失败。本次没有新2xx原文，也没有足够前后计时证据，故不宣称两次重连
已完全验证，不凭告警猜测新rekey缺陷。未改代码／配置、重启或追加收费通话。
一次被动SSH观察exit255中断，已补取同代际完整日志；没有将观察工具中断当成服务端重启。

后续实证（UTC）：12:46:35 TCP断开并启动原生REGISTER；12:48:43本地408／NoResponse，
30秒后原位重试，12:51:21再次本地超时。**不是运营商返回408**。Control到12:51:24.994
才请求空闲换代，新UK同镜像容器12:51:42启动；Control／FR未换代。

新代抓包又见13:20:22 TCP断开，13:20:23恢复连接／认证，而首次CHILD换钥13:21:51才发生。
本次TCP断开早于该换钥约89秒，不能把它归因于这次换钥。CREATE_CHILD请求／响应和DELETE
请求／响应分别约0.30秒完成，无重传；后续新SPI确有双向数据及内层交付，随后实际通话也通过。
被动采集12:58–13:33，1353包、内核丢包0，按既定timeout结束（124），两项临时unit已清理，
原始状态／日志／pcap均离机保留；pcap SHA `bbd8d4a0093431b8c0f1452a85794f63e637c437fc1115626bb228892d54b83c`。
13:31后的包包含上述实际通话；内层过滤不含RTP UDP，不能用全程内外包数差臆断解密丢包。
新入站SPI序号1–471中捕获421个不同序号、存在50个缺口；这是服务端抓包前的缺口线索，
并非已证明发送总量或定位某一个代理故障。不能将RTP丢包抹掉，也不能把这点直接归因到国家出口。

按用户要求对齐现成实现：官方sysmocom/20.7.0与sysmocom/2.14最新分支头就是Docker既有
`d231cb2c…`／`20537ab1…`；TCP立即重注册、旧绑定清理、端口切换哈希等修复已包含。
pagecat main `e3719840…`（8/12）无缺失根修，仍有单SPI／无安装ACK等旧实现，不能覆盖E6。
strongSwan的重叠SA／延迟退役原则与E6一致；两版Python DPD均缺同MID待确认重传，
故不盲目将现有默认0改20。此次没有依赖升级或新的生产代码部署。

Registered与TCP脱节已实证。一个4文件未完成状态／新提交检查草稿已在Git外封存，
仅本轮未提交增量已撤回，未混入运行版本；不得压缩后自动重放。草稿及评审／原始失败在私有索引。
原始基线的独立聚焦测试仍有2项SMS fixture失败（local_modem_sms表缺失）；生产该表实查存在，
不能报告成生产数据库缺表。两Mac PC/SC-only及各两读卡器当前在线、同代身份的交付也已核对，无遗漏。

## 剩余边界与后续入口

1. 指定号码短通话、换钥后实拨、当前有界抓包及上游差异核实均完成；不再为补统计重拨、
   重建或重做该批研究。先用上述证据处理真正主流程缺口，不再把TCP断开直接归因于CHILD换钥。
   英国前一代完整无响应的来源仍未闭合；原生重试及安全换代已实测，不等于长通话全验。
2. 浏览器工具明确URL安全策略拒绝页面操作；禁止换浏览器、CDP、代理等绕过。
   实际逐页点击、当前用户标签所载JS、麦克风／扬声器及真实多端呼入仍未验。
   服务器新资源正确不证明旧活动标签已刷新；不得把缓存当作已确认根因。
3. 已知P2：PC/SC原生调用没有硬超时；Agent12秒租约加故障挂断预算不等于最坏10秒停止计费。
   本次实际挂断成功不等于所有异常时限成熟；不能用强杀／提前释放锁伪装安全超时。
4. 首次softphone GET失败恢复已在顶部批次关闭；此前“后端未就绪”截图的
   精确根因不能由该反例替代，不重复实施已关闭的状态映射修复。
5. 初始IKE_AUTH完整MAC、独立HTTPS通知hook证书校验、完整macOS私有4G/5G、Linux统一Agent、
   旧研究树封存与流程整理仍按后续清单处理。Windows版本以各机收据为准，4054升级已闭合；
   不把混合协议设备概括为全部可用。
6. 旧树源码保存与恢复检查已完成，详见上方“工作区保留”。ignored运行数据未备份、
   旧树用途和未合入修改仍未全部判定，因此尚未整体封存或删除；当前执行入口唯一指向canonical。

当前私有索引：`/Users/fanli/.codex/private/mdd-reliability-20260826/RECOVERY_INDEX.md`。
实际通话：`/Users/fanli/.codex/private/mdd-authorized-calls-20260826`。
`archive/TODO_ACTIVE_RECOVERY_20260824.md`（2026-08-27 归档，原 `TODO_ACTIVE_RECOVERY.md`）
及历史提交只作历史，不执行其旧“下一步”。
Runner D持久代理／传输、E3/E4媒体、30c/716/E6部署已有记录，禁止因压缩再次重做。
