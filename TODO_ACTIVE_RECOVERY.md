# 当前恢复任务板 — VoWiFi、来电与 macOS Agent

> 本文件是当前目标的持久任务状态，不是设计随笔。每次继续工作（尤其会话压缩后）必须先读本文件，
> 再核对工作树和现网；禁止仅凭旧对话重新研究或重复修改。状态只能按证据推进：
> `待评审 → 已预审 → 实施中 → 已测试 → 已复审 → 已部署 → 已实机验收`。

最后更新：2026-08-25 08:18（Asia/Singapore）

## 最新恢复检查点（2026-08-25；后续继续时先读本节）

```text
checkpoint_id: PCSC-D1-ABSENT-ENGINE-QUARANTINE-TESTED-20260825T0818+08
goal_status: paused_by_user（不得由 Agent 自行 resume）
canonical_worktree: /Volumes/micron512g/tmp-project/codex-audit-tmp/mdd-forward-runtime-20260824
canonical_head_before_d1: c3343e1
production: root@10.44.0.23
paid_call_or_sms_test: DENY（未获逐次明确授权时禁止）
phase: D1_ABSENT_ENGINE_START_QUARANTINE_TESTED_PENDING_POST_REVIEW
next_action: 缺席 Engine 启动隔离已在本地实施并通过受影响回归，下一步只允许交原“实施后复审”会话独立复审当前精确 commit。不得构建、传输或部署。生产仍 BLOCKED：旧 Control 回滚版不识别新 quarantine，且未获正常枚举 APDU 授权；两项未单独预审闭合前不得传输、加载、切换 Control/Agent，不得读卡、AT、REGISTER、拨号或短信。

当前批次按以下顺序推进，不得因新消息覆盖旧项：

A. Control 请求风暴和来电快照误提示：已部署并完成两分钟生产观察，预审/实施后复审均 PASS。根因已
   实证为前端 raw instances/devices 对象更新触发 softphone/cellular-sims
   自激轮询；Control CPU 达约 100%，进而造成 Agent receipt timeout、VPCD 断线和 Engine 重建。
   当前差异加入每线路单 in-flight + 单 trailing fresh 合并、取消/代际隔离、remote-modem 语义变更
   刷新、softphone provisioning 缓存 runtime，以及 incoming snapshot 连续第 4 次失败才显示一次告警。
   已通过全部 10 个 WebUI 行为脚本、Vite production build、Python 聚焦 100 tests / 8 subtests。
   A 批部署时 Control image=`sha256:f9465689...`、container=`676abf43dd59`；观察期 CPU 从此前约 100%
   降至 6.73%–10.74%，3 分钟仅 2 次 `/api/engine/event`、0 次 `/softphone` 和 `/cellular-sims`，
   无 receipt timeout/VPCD bridge disconnect/traceback，Engine 1/7 generation 均未改变。首次 reload
   误用了历史 latest image，核验发现后未误报成功；已用原运行镜像制作精确 overlay、重标正确 tag
   并二次 reload 纠正，前后证据保存在生产私有 deploy-record 中。

B. 法国/英国“连续 6 次，问题不在出口”推送：B0 止误报已预审、实现、171 tests / 17 subtests、
   最终复审 PASS 并部署。两条线路均在 A 项风暴
   触发的 VPCD/Engine 重建后以 stable_for=0、SWu CONNECTED、IKE retransmits=2 累计 REPORT；现有
   文案把证据不足错误写成运营商/IMS 结论。必须按 Control/Agent/reader/VPCD/Engine generation
   连续性隔离或复位计数；B0 对 UNCLEAR 样本完全只读，零健康窗口不再累计或通知，证据缺失显示
   unknown，peer 通知使用本次 plan 节点，标题改为“线路持续异常”，并删除运营商/IMS/出口确定性
   归因。Telegram Bot API 已读回验证外部显示名为“MDD 远程 SIM”。B1 的 generation 连续性、真实
   PACE 节奏与硬件有界恢复状态机仍待 D 批，B0 不冒充完整状态机。
   B0 部署时生产 image=`sha256:f560d3be...`、Control=`bac3eef3e004`；TLS pin/HTTPS 200、容器与宿主源码
   hash、Engine 1/7 原代际、0 active call/channel 均已验证。install.sh 最终因 line 1 已存在的
   `local_fence_usim_auth_recovery/allow_not_proven` 在有界等待内不健康而非零退出，未绕过；实际 Control
   部署完成但全局 admission 继续 fail-closed，记录目录为
   `/opt/mdd-gateway/data/deploy-records/codex-20260825T0140+0800-false-line-attribution-b0`。
   英国 line 1 当前证据：SWu CONNECTED；01:32:16 是本开发会话直接执行 Asterisk CLI 的一次诊断性
   REGISTER，01:32:31 是现有恢复器一次显式提交；随后 `usim_status` 从 outage auth_seq=2 出现
   latest AUTH_ERROR/auth_seq=6，证明 Asterisk 内部计时器仍进入了后续 AKA，但 auth_seq 不能推导精确
   REGISTER 或网络发包次数。recovery attempts=3 仅是两次门禁探测+一次提交，record
   phase=submitted/fence 仍在；法国 line 7 已 AUTH_OK 且 fence 删除，但 record 也仍卡在 submitted。
   2026-08-25 05:01–05:11 发生一次独立传输故障：macOS Agent 同一长运行进程、同一 run_id 的 health 和两个
   VPCD 通道同时 TLS timeout/`UNEXPECTED_EOF`；无 Agent 物理拔卡事件。Control 在 VPCD session 消失后把两张
   卡合成为 removed，于 05:02 销毁 line 1/7 旧 Engine，05:11 同 Agent 重连、身份重新识别后创建新 Engine；
   line 1/7 随后均 `AUTH_OK/Registered`、0 channels、RestartCount=0，旧 recovery/fence 和 failover 计数被清除。
   旧 line 1 owner/container/run 事实已经失效，任何旧 D0 操作均禁止。该事件证明传输不可达不能等价为物理拔卡。
   06:08 部署前只读复核：line 1/7 均 `SWu CONNECTED`/`USIM AUTH_OK`/PJSIP `Registered`、
   `0 active channels/0 active calls`，两线 failover 当前项已清除；Windows 与远程 Mac Agent 为
   fresh/online，旧 Mac 记录 offline。line 1 近一代 Engine 曾出现 `Missing Security-Server`
   并在 06:05 同容器 restart 一次，随后恢复 Registered；这是当前证据，不得把已清空的告警误报成现在仍故障，也不得由此推导运营商定责。

C. Windows 10.44.1.1 EC20 语音不可用：已完成预审、实现、测试、事务部署、实机验收和实施后复审，
   最终 PASS 并关闭。原始 package 信任链根因是 builder 的 `{name,bytes,sha256}` 与 runtime 的
   `{name,size,sha256}` 不一致，且 Windows digest 未进入服务端 allowlist；正式构建后又实证并修复
   `ManagedAgentRuntime.start()` 把已加载配置替换成校验器的脱敏返回值、从而丢失 token 的确定性根因。
   最终代码 commit=`8f3d84f`；package digest=`72a115d205ac19c59d9668cc4a3499a28bf36a45a3b595b2076ace714c1e5674`，
   CLI sha256=`f8d681e8a2ff2ed6b6787ba919f053d45fca966a86a3daab3fc5842f313ecf6e`，
   GUI sha256=`3fab6fc5e2bcaea6d2b6aea543862d43167e1f3deb2846399dd4dbceadd92c39`。
   代码门禁最终 `328 passed, 8 subtests passed`；token 修复聚焦 2、相关 Agent 206、受影响集 242 均 PASS。
   服务端 release 已持久归档，allowlist 8→9；部署记录为
   `/opt/mdd-gateway/data/deploy-records/codex-20260825T0337+0800-windows-c-tokenfix`。当前 Control
   image=`sha256:f560d3be...`、container=`05ad3346e6ca...`、restart_count=0；旧 Control 以 stopped/no-restart
   容器保留，Engine 1/7 未重启且均 0 call/channel；direct download 已原子切换到新包，旧包完整保存在
   deploy-record，stage 无残留。Windows 第三次事务安装成功：MddAgent Running/Auto、PID 22584；连续两轮
   runtime online、doctor/self-test healthy；EC20 IMEI=`864819055504383`、ICCID=`89852312388530153089`、
   COM34 connected，call/audio/SMS/cellular capabilities 均 true，安装文件 hash 与 release 完全一致。
   服务端同 session 的 12 秒双快照持续前进，并接受 call contract v2、audio telemetry v2 与新 digest；
   call_signalling/call_audio/call_ready/call_audio_ready 均 true，contract error 为空；Windows 到服务端 health、
   VPCD、modem 三条 TCP 均 established。持久 `agent-health.json.seen_at` 只在语义变化时落盘，不得用作
   live heartbeat；本次以 receipt WSS 长连接、server live registry 和 modem 快照交叉证明。GUI 在唯一 RDP
   session 2 的 Limited 任务中显示“服务：running / 运行时：online”、PID 22584 和正确 digest；关闭窗口后
   驻留托盘且无错误，清理测试 GUI 后服务 PID 不变。截图只保存在外置盘临时证据目录，Windows 测试脚本、
   结果、截图、计划任务和 GUI 进程均已清理。最终复审明确 C 可关闭；未拨号、未短信、未执行 APDU，也不
   声称完成真实付费通话。192.168.15.211 当前未插 EC20，不能假称已完成同机对比。

D. 硬件/Agent 运行期状态机：C 已关闭；D0 试验实现已被实施后复审拒绝并私有归档。D1
   “传输中断不等于物理拔卡”已经修订预审 PASS、实施、测试和最终实施后复审 PASS，当前只等同批部署，未部署前不得宣称生产已修复。
   D1 将 remote VPCD name 消失或 `present=false` 先收敛为 unknown，仅在 active/open/fresh native-v2 health、同
   Agent run、同 health session、同 VPCD session generation、当前 identity CAS 和服务端 PC/SC 观测同时一致并稳定后才进入 exact stop。stop failure/shutdown 不 completed、不 confirm、不清身份；精确 missing 才是无 Engine 的安全终态。空 binary ATR 作为合法 VPCD frame 转发，不再误当断线。真实读卡在任何 config/draft/Hub/autostart 副作用前完成 generation CAS；空白 eUICC placeholder 在第一次 CAS 前净化。native reader 原物理移除路径不变。
   D1 本地最终影响集为 `262 passed, 13 subtests passed`，WebUI agent-health/line-presentation/build PASS，`git diff --check` PASS；最终复审 PASS。
   D2 仍要处理以下已实证的
   三个阻断是：Asterisk 自身 3600 秒 REGISTER timer 可绕过 Control exhausted；VPCD 新
   session_generation 会继承旧 ICCID/matched，不能证明同一张卡；AUTH_OK 先写再删 fence，而现有
   reconciler 无 fence 即返回，submitted 结果不能线性落盘 RECOVERED。修订方案必须同时做到仅对
   remote-VPCD local-auth fence 抑制 timer/FullyBooted 自动注册但保留显式受控一次恢复；稳定卡身份
   与 slot/session/ready_at 路由身份分离并要求当前会话强身份；durable recovery record 驱动结果消费、
   总实际 REGISTER 提交预算封闭、未知结果 EXHAUSTED。修订评审的必要门禁为：Asterisk
   `handle_client_registration` 及 `schedule_registration` 在 fence 下同时 fail-closed；Control 一次性 nonce/permit
   与 Asterisk serializer 内原子消费，send 前先落 durable dispatch receipt（只能声称 dispatch 0/1，不声称
   网络收包）；recovery schema 需 `auth_seq_baseline`/nonce/receipt 关联，AUTH_SYNC 不得终止；pending/
   submitted/exhausted 必须阻断所有 Engine start/rebuild，recovered 才可归档释放；VPCD 复用每次 claim 的
   token 作 session_generation，通过 begin_observation/observe_card(expected_generation) CAS 拒绝旧扫描晚到；
   failover 分 `sample_generation` 与可跨受控重建的 `campaign_epoch`，UNCLEAR 必须零写。仍需复用
   现有 maintainer，采用稳定阈值、单次恢复、分级冷却、成功复位和可观测 next_probe/
   last_repair/cooldown，不得无限重启、无限 fallback 或自动重放付费动作。新增硬门禁：TLS/Agent/VPCD 断线只能
   进入 `transport_unreachable/card_presence_unknown`，不得删除槽位或 Engine；物理移除必须来自当前 session、
   单调事件序号和匹配 agent/reader/card 身份；重连建立新 session epoch，稳定窗口内核对同卡后恢复原槽位；多个
   reader 独立维护状态，单通道异常不得牵连同 Agent 其他卡。D0 两个复审阻断为 marker→`restart=no` 崩溃窗口，
   以及 containment 未与蜂窝 call/SMS admission 线性化，瞬时 paid-work=0 不能证明之后仍为零。

E. 用户确认媒体入口 IP：架构预审已完成，结论为删除人工确认。该 IP 仅供浏览器直连 WebRTC RTP
   的 SDP/ICE 主机候选，不是 IMS/出口/用户访问接口；误确认不能证明 UDP/RTP 可达。第一批改为
   浏览器会话逐候选自动 Echo + 双向 RTP 证据、短期 route lease、网络/Engine/WSS 变化自动失效，
   并补 WSS ping/pong、通话中 RTP 续租和入/出站统一 10 秒精确挂断兜底。WSS PCM 只在真实环境
   证明直连失败后作为第二批回退，不在当前止血批次过度设计。

F. macOS 全能 Agent、CLI/GUI、EC20 私有数据面、多 reader、权限和部署：保留在
   TODO_MACOS_AGENT.md，当前按用户决定延后部署；不得误报已完成或用当前服务端止血改动污染。

G. 旧研究工作树封存：待 A/B/C 主流程稳定后进行。必须先制作私有、自包含、可校验 git bundle
   与 NUL-safe 文件清单，保留 refs/reflog/index/status，并经过 realpath 白名单与前后竞态校验；
   永不触碰用户主工作树 /Volumes/micron512g/tmp-project/mdd-gateway。
```

### 当前批次评审/实施流水（2026-08-25）

| ID | 范围 | 状态 | 证据/边界 |
|---|---|---|---|
| `WINDOWS-C-PRE-1..3` | Windows package/service/CLI/GUI 闭环实施前评审 | `NEEDS_CHANGES×2 → PASS` | 先补 strict artifact trust/persistence、真实 reparse 删除边界、installer exact schema/runtime digest；再补系统树后代/输出内 junction、同盘私有 staging 原子发布；最后消除默认 `agent/dist/mdd-agent-windows-amd64` 与保护规则冲突。评审明确 PASS 后才实施。 |
| `WINDOWS-C-IMP-1` | strict manifest、release store、安全覆盖、installer runtime digest、GBK | `已测试，实施后复审 PASS` | 修改 9 个代码/测试文件及本任务板；聚焦 `204 passed`，最终扩大受影响范围 `328 passed, 8 subtests passed`；py_compile、`bash -n install.sh`、`git diff --check` PASS。Windows PS5 两脚本 parse PASS；真实 builder 路径门禁 system/8.3/device/repo/default/SUBST/junction 全 PASS，临时目录和 SUBST 已清理。实施后复审发现的 repo trust anchor、仓库后代覆盖、runtime env/_MEIPASS 冒充、架构实证、final-path/reparse、fsync failure/reuse、frozen 定期重复 hash 均已整改；独立复审最终 PASS。 |
| `WINDOWS-C-TOKEN-FIX` | frozen runtime 启动丢 token | `已修复、已测试、已复审` | 正式包实机启动暴露 `ManagedAgentRuntime.start()` 误用脱敏校验返回值；commit `8f3d84f` 保留 loaded config、仅调用校验器判错。聚焦 2、相关 206、受影响 242 tests 全 PASS。 |
| `WINDOWS-C-DEPLOY-1` | 服务端 release/allowlist/direct download + 10.44.1.1 事务安装 | `已部署、已实机验收` | package digest `72a115d...` 持久归档；allowlist 8→9；Control 同一 B0 image、restart_count=0，旧容器 stopped/no-restart 保留；Windows MddAgent Running/Auto PID22584，连续两轮 runtime/doctor/self-test/EC20/hash 全 PASS；server live contract/WSS/VPCD/modem 证据闭合。未拨号、未短信、未 APDU。 |
| `WINDOWS-C-FINAL-POST` | GUI/托盘交互验收与最终复审 | `PASS，C CLOSED` | 唯一 RDP session2、Limited task；界面显示 service running/runtime online/PID22584/正确 digest，WM_CLOSE 后驻留并隐藏、tray error absent、服务 PID 不变；证据 PNG sha256=`630e9d14...` 只存外置盘临时目录，Windows 测试产物/任务/GUI 均清零。实施后复审确认剩余 PC/SC/恢复预算归 D，自动媒体/浏览器音频归 E。 |
| `PCSC-D-PRE-1` | remote-VPCD/USIM 恢复状态机 | `NEEDS_CHANGES，禁止实施` | timer 绕过、当前会话卡身份不足、AUTH_OK/fence 结果消费不闭合；line1 有两次显式 REGISTER 提交及后续 timer-driven AKA 证据，但无 dispatch receipt，不得声称精确网络发包总数；下一版方案复审 PASS 前不得自动恢复。 |
| `PCSC-D-PRE-2` | D0 containment + 完整 D 修订 | `NEEDS_CHANGES，D0 单独复审中` | direct stop 可被 hotplug/health/manual start 复活；现有 admission gate 不覆盖 REGISTER，不得误报可阻 Asterisk timer。完整 D 须补 nonce/dispatch receipt/result correlation/lifecycle fence/VPCD CAS/sample+campaign 两层。 |
| `PCSC-D0-PRE-4` | line1 REGISTER 紧急 containment 修订预审 | `PASS，但 owner 后续失效` | 预审认可严格 marker、精确 owner、retained stop 和有界 deadline；这只是方案 PASS，不覆盖后来代码中出现的 marker→policy 崩溃窗和蜂窝 paid admission 并发。 |
| `PCSC-D0-IMP-POST-1` | D0 试验实现实施后复审 | `NEEDS_CHANGES，整批拒绝` | 57 tests / 12 subtests PASS 不能解除两个 P1：marker 发布与 restart=no 分成两次调用；containment 未进入统一蜂窝 call/SMS admission/maintenance lock。现网旧 owner 已失效；禁止提交/部署。试验 binary patch、两个 untracked 文件、status 和 SHA256 私有归档于外置盘 `mdd-rejected-d0-20260825T0519+0800`。 |
| `PCSC-D-TRANSPORT-INCIDENT-1` | macOS 双 reader 传输中断取证 | `CONFIRMED` | 05:01–05:11 同一 Agent 进程/run_id 的 health + 两个 VPCD TLS 通道同时失败，无物理拔卡；Control 错将 session loss 合成为 card removed 并替换 line1/7 Engine。重连后两线均 AUTH_OK/Registered。正式 D 必须先分离 transport 与 physical。 |
| `PCSC-D1-PRE` | transport/card presence containment + health-v2 evidence join | `PASS` | 原 sideband `reader.removed` 方案被拒绝；审核通过复用 Agent health、共享 Agent run id、VPCD generation CAS、稳定证据 join 和 native 路径不变的同批 D1a+D1b 方案。D1a 禁止单独部署。 |
| `PCSC-D1-IMP-POST` | D1a+D1b 实施与竞态收敛 | `NEEDS_CHANGES×2 → PASS` | 首审修复 probe CAS 副作用顺序、stop TOCTOU、in-flight/completed、通用 auto-start gate、health session key、UI unknown 和真实双 reader 流程；二审再将 blank-eUICC placeholder 净化前移到第一次 CAS。审查者随后对已存在的 try 前初始化产生一次误报，最终复核确认误报并 PASS。独立复审 `262 passed, 13 subtests passed`；未部署、未计费操作。 |
| `PCSC-D1-AGENT-BUILD-POST` | Windows/macOS Agent 正式产物构建与复审 | `PASS，未部署` | Windows 包来自精确源码归档 commit `1875154`，manifest digest=`41304c7fdd9542b7565525cd66d23971bbde636c074819ace9b27e7a7f3581a4`；当前共享 verifier 仍完整接受其严格 v1。macOS 干净单并发 release 构建 PASS，manifest v2 digest=`136d6a2aae4ea1ca27f2440f2ae6f307968d1f2d6d3c0c4fd490330b86359cec`，94 files/28 internal symlinks，CLI sha=`423aae06...`、cellular helper=`cca48307...`、audio helper=`58f9bc7b...`；arm64、Developer ID、统一 TeamIdentifier、Hardened Runtime、deep signature、最小 audio-input entitlement、CLI/GUI 内含共享 `package_manifest` 均实证 PASS。相关 Python `217 passed`，真实 C AT test PASS，py_compile/bash -n/diff-check PASS；增量实施后复审 PASS。当前未公证，`spctl` 原样为 `rejected / source=Unnotarized Developer ID`，只可作为受控部署候选。未安装、未部署、未触发 PC/SC/硬件/付费动作。 |
| `PCSC-D1-CONTROL-BUILD-POST` | Control 精确源码离线构建、审计与复审 | `PASS，未部署` | 从 exact HEAD `82e9c22f2fd2a8a450a9eeb5f13b9cc5c44ba7e0` 仅归档 `control/ host/ webui/ VERSION`，source tar sha256=`c6441793b92c0ccc3af4758c3b65b6097fc07f0cf19f1437d093c8af1c9d60df`。私有 Linux runner 的 buildx 构建出 Linux/amd64 managed image ID=`sha256:ee6238bd26c5fbe9fe6a3cc9afceea42db526a30ff3041f3f38b3011868016d1`、version label=`82e9c22`；压缩产物大小=`262459989`、sha256=`e386704a1e0bcdb2aa7af5e7e353f5d78ad0be20710d2d4b3ea8abc23d63fb56`，`gzip -t` PASS。首次 scp 截断被独立 size/hash 复核发现并拒绝，改用可续传 rsync 后本地与远端稳定值一致；无效产物已清理。未启动的临时 container 仅用于 rootfs 审计，三份关键 Python 文件与源码逐字节一致，WebUI 中英文 unknown 状态存在；容器已删除、runner running container=0。实施后复审 PASS，可进入生产 0-paid/predeploy 门禁；不授权直接替换。 |
| `PCSC-D1-ABSENT-ENGINE-QUARANTINE-PRE` | 缺席 Engine 启动隔离 | `PASS，仅离线实施` | 生产只读拓扑发现 line9 enabled/desired 但 Engine9 absent；新 Control/card identity current 可触发 create/REGISTER。原预审会话经多轮 `NEEDS_CHANGES` 收紧后最终 PASS：唯一 pure contract；stable orchestrator line lock；Host global EX→line EX acquire/release；normal global SH→line SH opaque permit 从 PIN/APDU 前贯穿 create；maintenance 保持既有 global EX→engine-maintenance→line SH；hard delete 与 acquire 互斥；历史 reader 只能作 expected hint，不得发布当前 matched/ICCID；Host authority 只撤 admission，不停 Engine。生产继续 BLOCKED：旧 Control 回滚不识别 marker，未获枚举 APDU 授权。 |
| `PCSC-D1-ABSENT-ENGINE-QUARANTINE-IMP` | 启动隔离离线实施 | `已测试，待复审` | 已新增共享 contract、Host acquire/release CLI、Control private permit/create/delete/card-probe/status 门禁、Host authority reason 及聚焦测试。静态审计确认两处 Docker create 和唯一 hard-delete 都在持有稳定 permit 时二次校验；释放本身不读卡，下一次正常 monitor cycle 才恢复 probe。最终证据：核心影响集 `220 passed, 25 subtests passed`；自动建线/card-agent/remote-modem/agent-health/notify 影响集 `128 passed, 2 subtests passed`；其余产品/更新边界 `30 passed, 1 deselected`；py_compile 和 `git diff --check` PASS。被排除的 `test_status_polling_cannot_trigger_an_ims_register` 在本次修改前的 HEAD 中同样失败：它要求 `main.py` 包含命令，而命令已在 `engine.py`，本批没有修改该旧断言或 REGISTER 逻辑。未构建、未部署、未操作设备。 |
| `PCSC-D1-PROD-GATE` | Control + Windows/macOS Agent 同批发布 | `全部产物 PASS；生产只读门禁待核；未部署` | 下一步只做生产只读核查并记录当前可回滚代际；0 active call/channel、0 paid lease/work、Control/Engine/Agent/VPCD exact generation 均闭合且部署方案预审 PASS 后，才允许传输/加载/同批更新。更新后只做无资费 health-v2/VPCD/current identity/线路保持验收。 |

## 恢复检查点（先读；比下方历史记录优先）

```text
checkpoint_id: FREE-FRANCE-SIP-PROFILE-PERSISTED-20260823T1612+08
goal_status: active
open_scope: 10.44.0.23 生产恢复部署 + UK/FR VoWiFi 注册恢复 + Agent/VPCD/健康状态收敛 + 后续无资费媒体验收
phase: FREE_FRANCE_PROFILE_PERSISTED_PENDING_AUTHENTICATED_LINE7_REPROVISION_AND_MEDIA_CONFIRM
implementation_id: CALL-UI-RUNTIME-STATUS-IMP6 + ENGINE-ADMISSION-FULL-BUILD-20260823 + DOWNLOAD-PROXY-24445 + HOST-SOCKS-PROXY-24444-20260823 + DOWNLOAD-PROXY-ROUTE-FIX-20260823 + LEAF-AGENT-RECONNECT + AGENT-HEALTH-PERSIST-FIX-20260823 + ADMISSION-WRITER-LAZY-IMPORT-SOURCEPIN-20260823 + VOWIFI-INCOMING-FALLBACK-EXACT-HANGUP-20260823 + FREE-FRANCE-SIP-PROFILE-20260823
pre_review_ids: call_ui_pre_review NEEDS_CHANGES→方案修订为 exact hangup；network_mac_pre_review NEEDS_CHANGES→方案修订为 exact linkedid hangup；prod_wrapper_paid_pre PASS；download proxy route fix 经 network_mac_pre_review PASS；Free France profile 最小持久修复经 network_mac_pre_review PASS，runtime hotpatch 方案被 network_mac_pre_review 拒绝，prod_wrapper_paid_pre 仅从付费角度 PASS，因此按更严格生命周期边界不热补 running Engine。
post_review_ids: network_mac_pre_review PASS（agent health 持久化修复、admission writer source-pin 修复）；prod_wrapper_paid_pre PASS（admission writer 无 paid call/SMS 暴露）；本轮 VOWIFI fallback 初次 post-review：call_ui_pre_review NEEDS_CHANGES（fallback hangup async race、overlay 应优先真实 JsSIP 来电）；network_mac_pre_review NEEDS_CHANGES（source_call_id 必须完整匹配且非空、必须按 engine_run_id 精确 generation、防旧 linkedid 误挂）；prod_wrapper_paid_pre NEEDS_CHANGES（生产 source_call_id 为 run-id 前缀格式、maintenance fence 未放行新 exact hangup）。FIX1 三方最终复审 PASS：call_ui_pre_review PASS，network_mac_pre_review PASS，prod_wrapper_paid_pre PASS。Free France profile 生产部署后只读核验 PASS：Control 源码/容器均加载 `208-15` defaults，Engine 1/7 未重启，paid ledger 未变化，敏感日志 0。
next_action: 已获用户授权“保留记录可随意部署”。10.44.0.23 已部署本轮 VOWIFI incoming fallback FIX1：记录目录 `/opt/mdd-gateway/data/deploy-records/codex-20260823T1508+0800-vowifi-incoming-fallback-fix1`；旧 Control image 保留 tag `mdd-sim-gateway/control:pre-vowifi-incoming-fallback-fix1-20260823`；最终 Control image `sha256:d59c80909605c2fddbb4e775d40c9215e24ad06ba36aa1407f9c0d21720e53f1`，容器 `7342bb54cd61...`。完整 Dockerfile build 因 legacy builder 不支持 `FROM --platform=$BUILDPLATFORM` 失败，BuildKit 重跑因 buildx 缺失失败，随后采用记录目录内 runtime overlay：基于 pre-fix image 只覆盖已评审的 `control/app/main.py`、`store.py`、`ami.py` 与 clean `webui/dist`；远端源码已同步同一运行文件，误同步到 `webui/` 根目录的 5 个 src 文件已明确删除并记录。`install.sh reload --mode docker --no-engines` 通过 `MDD_REUSE_CONTROL_IMAGE=1` 成功重建 Control；Engine 1/7 未重启，仍 `Registered` 且 `0 active channels/0 active calls`；Control 重启期间 enabled line 9 曾短暂自动启动但最终容器不存在，最终 `docker ps -a` 仅 Control、engine-1、engine-7。DB calls 表已迁移出 `engine_run_id` 列；paid-safety：sms guards 0、allowance pending 0、local modem sms unbound 0、最近日志无 Originate/MessageSend/allowance/media-admission；仅有 1 条 46h 前旧 open calls 行，`source_call_id=''`、`engine_run_id=''`、Asterisk 无活跃通话，新 fallback 不会显示或挂断该旧记录。宿主下载代理 `mdd-download-proxy.service` 最新记录目录 `/opt/mdd-gateway/data/deploy-records/codex-20260823T141514+0800-host-socks-proxy`：订阅 `https://yovey.me/filesfor172166/sing-box` 已重新拉取；监听 `127.0.0.1:24444`、`172.17.0.1:24444` socks，以及 `127.0.0.1:24445`、`172.17.0.1:24445` mixed；2026-08-23 15:20 复核 socks→gstatic `204`，约 0.62s，dockerd 不再引用旧代理。
  生产恢复状态：leaf mac 正确地址为 `leaf@192.168.111.171`；`mdd-agent reconnect --json` 后两个 PC/SC reader 重新 bridge：slot 1=Free/法国 instance 7，slot 10=giffgaff/英国 instance 1。Control 自动启动 engine-7/engine-1；首次人工 PC/SC 探测与 Engine USIM AUTH 竞争导致两线 401/card reset，随后停止绕过锁的读卡并按 7→1 顺序重启 Engine。Docker 重启后暴露 normal admission authority writer fail-closed：长运行 orchestrator 在 `/opt/mdd-gateway/host` 脚本目录上下文导入 `engine.admission_gate` 失败，旧代码缓存 `None` 并持续写 `deny_not_proven_before_allow`。`ADMISSION-WRITER-LAZY-IMPORT-SOURCEPIN-20260823` 已最小修复：显式把 repo root 放入 `sys.path[0]`，source-pin 到 `/opt/mdd-gateway/engine/admission_gate.py`，错误模块清缓存重试，deny path 返回 `(proved,cause)` 避免 tuple truthiness，并在 wrong source/protocol/missing probe/import failure 时 fail-closed。远端记录目录 `/opt/mdd-gateway/data/deploy-records/codex-20260823T142649+0800-admission-writer-lazy-import`，部署 hash `368f7544f28780c843edf792ccae44ac307646792ae388a37bf849f001070a17`；只同步 `host/mdd_admission_authority.py` 并只重启 `mdd-sim-gateway-orchestrator`，未重启 Control/Engine、未删 deny/fence、未拨号、未短信。
  当前只读验收：本地 `python3 -m pytest -q tests/test_admission_authority.py tests/test_engine_admission_gate.py tests/test_update_apply.py` 为 `65 passed`；生产 line 1/7 writer aggregate healthy，`admission-deny` absent，authority/gate status 的 protocol、identity digest、state digest、epoch、lease_seq、run_id 相互匹配，gate probe 两线 `allowed=True`；line 1/7 均 `SWu CONNECTED`、`USIM AUTH_OK`、PJSIP `Registered`；engine-1/7 仍 `0 active channels/0 active calls`，paid-work ledger 和最近 Originate/MessageSend 均 0。`network_mac_pre_review` 与 `prod_wrapper_paid_pre` 复审均 PASS，admission writer 修复可关闭。
  媒体入口当前状态：现网 `media-ingress-confirmations.json` 为空/不存在；在运行中 Control 逻辑下，Host `10.44.0.23:8443`、`192.168.111.225:8443`、`100.64.0.71:8443` 均能 resolve 到唯一 candidate，但 `confirmed=false`。现网 active dist `/app/webui/dist/index.html` 引用 `index-B6Js3Lc6.js`，该 JS 确认包含 `Confirm browser voice route`、`Confirm and test` 和 `/api/system/media-ingress/confirm` 逻辑。in-app Browser 访问 `https://10.44.0.23:8443/` 被自签证书 `ERR_CERT_AUTHORITY_INVALID` 拦截，按浏览器安全规则未绕过。因此当前下一步需要用户在已信任证书/已登录的实际浏览器页面点击“Confirm and test”，或提供已打开并已通过证书的浏览器标签供接管；随后才能无资费验证 WebRTC contact 与 Echo/RTP canary。
  本轮来电 fallback 修复已本地实施并部署：UI/网络预审均要求不能使用 broad `/api/instances/{iid}/hangup`，因此实现 exact linkedid hangup：`control/app/ami.py` 新增 `hangup_channels_by_linkedid()`，只挂 `CoreShowChannels` 中 `Linkedid` 完全匹配的 channel；`control/app/store.py` 新增 `get_call_by_id()` 并保存 `engine_run_id`；`control/app/main.py` 新增 `POST /api/instances/{iid}/calls/{call_id}/hangup`，要求 open `direction=in/transport=vowifi`、请求 body 的完整 `source_call_id` 非空且与记录完全匹配、记录必须携带 `engine_run_id`、完整 `source_call_id` 必须为 `<engine_run_id>:<linkedid>`、当前 runtime running/container_id 且 `runtime.engine_run_id` 完全一致，再只把裸 `linkedid` 传给 exact AMI hangup，matched=0 fail-closed，不 fallback all；新 exact hangup 已加入 maintenance hangup 豁免，维护期间仍允许终止已有来电。前端新增 `webui/src/vowifiIncomingFallback.js` 和 callCoordinator fallback：backend call_in 在没有真实 JsSIP/outgoing/checking/active call 时显示全局不可接听来电面板，无 Answer；Hang up 调 exact endpoint；Hang up 异步成功/失败只更新同一个 captured backend call，避免覆盖后来真实 JsSIP 来电；GlobalCallOverlay 在多个 incoming 中优先真实可接听 JsSIP 来电；Confirm media route 只调用 media-ingress confirm 并 reloadLine，不自动接听/不自动 media admission。FIX1 验证：py_compile PASS；聚焦 exact hangup/maintenance 测试 `7 passed, 1 warning, 8 subtests passed`；较宽后端 `tests/test_line_lifecycle.py tests/test_status_registration.py tests/test_engine_maintenance.py tests/test_store.py tests/test_engine_notify.py` 为 `184 passed, 1 warning, 25 subtests passed`；前端 `test:vowifi-incoming/test:vowifi-media/test:vowifi-media-behavior/test:cellular-incoming/test:live-status` PASS；`npm --prefix webui run build` PASS；touched-files `git diff --check` PASS；三方最终复审 PASS。部署：记录目录 `/opt/mdd-gateway/data/deploy-records/codex-20260823T1508+0800-vowifi-incoming-fallback-fix1`；Control image `sha256:d59c80909605c2fddbb4e775d40c9215e24ad06ba36aa1407f9c0d21720e53f1`；部署后 line 1/7 Registered，0 active calls/channels，paid-risk 计数无新增。
  剩余必须处理：1) `Registered` 只证明 IMS 注册，不能宣称可通话，仍需实际浏览器 Host 上 media-ingress confirm、WebRTC contact、每线 route-bound admission 与无资费 Echo/RTP canary；2) 本轮 fallback 只解决“无 WebRTC contact 时不能只有 banner”，不证明当前 call 可接听；3) 禁止再用绕过 Agent/Control reader lock 的手工 APDU 读卡作为常规验证；4) 部署脚本暴露 fresh host 依赖、BuildKit/legacy Dockerfile、Docker build 代理传递问题，需另走评审→修复→复审。
review_sessions: call_ui_pre_review, network_mac_pre_review, prod_wrapper_paid_pre
production_deploy: AUTHORIZED_AND_PERFORMED_WITH_RECORDS
paid_call_or_sms_test: DENY
current_increment: DOWNLOAD-PROXY-TUIC-20260823 + FREE-FRANCE-SIP-PROFILE-20260823。下载代理最新记录目录 `/opt/mdd-gateway/data/deploy-records/codex-20260823T162106+0800-download-proxy-tuic`：用户反馈代理仍慢后，只改宿主 `mdd-download-proxy.service` 配置的 `route.final`，从 yovey anytls 切到同一订阅内测速最快且 `sing-box check` 通过的 yovey TUIC；监听端口仍为 `127.0.0.1/172.17.0.1:24444` socks 与 `127.0.0.1/172.17.0.1:24445` mixed；只重启下载代理，未重启 Docker/Control/Engine。验证：四入口访问 gstatic 均 204（约 0.41–0.44s）、GitHub API 均 200（约 0.47–1.62s）、Docker Registry 均 401（约 0.57–0.64s，401 为 registry 未鉴权预期响应）；journal 明确显示出站 `outbound/tuic`。此前 `/opt/mdd-gateway/data/deploy-records/codex-20260823T1605+0800-download-proxy-route-fix` 已完成私网/loopback/link-local/CGNAT/ULA 直连规则修复。Free France 记录目录 `/opt/mdd-gateway/data/deploy-records/codex-20260823T1612+0800-free-france-sip-profile`：生产 `control/app/config.py` diff 只有新增 `CARRIER_SIP_PROFILES["208-15"]`（country FR、access_type wlan1、user_eq_phone False）；`py_compile` PASS；Control 容器内同一文件已更新并基于当前 image commit 为 `sha256:d542791056bcba1565dc6f5d620ae0c60f1c3e68c07b83d981af13150c3dfc9f`，`MDD_REUSE_CONTROL_IMAGE=1 ./install.sh reload --mode docker --no-engines` 成功。部署后 Control HTTPS 200；Control 容器内 `carrier_sip_defaults("208","15",...)` 返回 `country=FR`/derived BSSID/`wlan1`；engine-1/7 container id 未变、0 active calls/channels；paid ledger：actionable open call=0、pending outbound SMS=0、active SMS guards=0、pending allowance=0、active cellular leases=0、敏感日志 0。Control reload 仍触发现有产品副作用：enabled line 9 自动启动，engine-9 `SWu DOWN`、0 channel；已记录，未扩大处理。
current_next_action: line 7 当前只读观察已 `Registered`，但 running engine-7 的 `/etc/asterisk/pjsip.conf` 仍是旧 presentation（`i-wlan-node-id=ffffffffffff`、无 `accesstype`），所以不能把该注册归因于新 Free profile。由于生产 API 需要 admin session/CSRF，SSH 侧不能安全调用 `/api/instances/7/reprovision`；runtime hotpatch 虽然 paid-safety PASS，但被网络/生命周期评审拒绝，因为会破坏 generation/config/admission 一致性。下一步需要用户在已登录页面对 line 7 执行一次 scoped reprovision/start，或提供已认证控制路径；之后只读验证新 engine generation 的 `instance.json`/`pjsip.conf` 是否包含 derived BSSID、`country=FR`、`accesstype=wlan1`，再观察 REGISTER。媒体入口仍需实际浏览器点击 Confirm and test 后再做无资费 Echo/RTP canary；本轮未做真实通话/SMS/余额查询。
```

- [x] 已读取当前 Goal，确认七项总目标没有缩减。
- [x] 已核对工作树；它包含用户既有的大量未提交改动，禁止清理、回退或据此重做旧范围。
- [x] R6-C 严格方案已由两条既有评审链完成实施前评审；不得再创建同题评审或重新选择 A/B 方案。
- [x] 首段增量定位复核完成：`R6-C-ENTRY-AUDIT-1` 与 `R6-C-LOC-1`；确认 AMI
  `MessageSend` 绕过 dialplan，必须与 MT MESSAGE pre-202 hook 一起原子覆盖。
- [x] R6-C 第一段实现完成并记录精确文件差异。
- [x] R6-C 第一段本地无资费测试完成：全量 `1220 passed, 1 warning, 68 subtests passed`，
  聚焦 89 PASS，新增 19 PASS；详细证据见下方实施记录。
- [x] 两条原评审链完成首轮实施后增量复审 A1/B1，结论均为 `NEEDS CHANGES`；
  可复现发现已按 A1-F1..F4 / B1-F1..F3 登记，不得泛化重开整个方案。
- [x] 原两链经 A2/B2、A3/B3 整改后，`R6-C-ENGINE-IMP2-PRE-A4/B4` 双 `PASS`；
  已登记 `R1b-DEPLOY-2-PRE8-R6-C-ENGINE-IMP2`，实施边界冻结。
- [x] 仅实施 A1/B1 列出的整改；聚焦 131 PASS、全量 1233 PASS，并更新精确指纹。
- [x] 整改后一次性 Linux/Asterisk 行为 E2E PASS；脚本指纹、结果和私有日志位置已登记。
- [x] R6-C-ENGINE-IMP2-POST-B5 的 endpoint@domain P1 发现已由无 SIM/无资费探针复现；
  IMP2 不能冻结，必须进入窄范围 IMP3 预评审。
- [x] 两条窄范围预评审完成 endpoint@domain 最小修复方案核对；A6 `PASS`，B6
  初审 `NEEDS CHANGES` 后按要求补齐 endpoint@domain/非 IMS/patch anchor 测试边界，复核 `PASS`。
- [x] endpoint@domain IMP3 实施、聚焦测试、真实 Asterisk 无资费 E2E 已完成；双复审完成后才能重新进入冻结。
- [x] 两条原评审链完成 IMP3 实施后增量复审；A7 `PASS`，B7 初审 `NEEDS CHANGES` 后已按要求
  加强 E2E body/exactly-one 断言并重跑通过，B7 复审 `PASS`。
- [x] `PROD-WRAPPER-PRE-A1/B1`：评审 production bootstrap wrapper 最小接入方案。当前只读定位确认：
  `install.sh reload` 仍直接重启 Control；`host/mdd_update.py` 仍调用 `install.sh reload --no-engines`；
  非测试代码没有生成完整 `control-upgrade.json/rollback_control`、启动 `maintenance-proxy`、写
  `proxy-self.json` 或调用 supervisor `recover/revoke` 来线性化真实升级；同时除测试外尚未发现生产代码
  签发 `admission-authority.json` 的 `normal_committed` 或 `maintenance` authority。不得把已有
  supervisor/proxy/Engine gate 原语误报为生产 wrapper 已完成。
- [x] `PROD-WRAPPER-IMP1-REVISED-PRE-A2/B2`：修订后的最小安全实现只允许覆盖两点：
  1. host 侧生产 `normal_committed` authority writer/renewer，严格绑定 exact Engine Docker/run-id
     generation，持续写入每线 `admission-authority.json`，并可通过 gate probe 证明两步 warmup 与
     TTL 过期 fail-closed；
  2. 在完整 production wrapper 未完成前，公开 `reload --engines`、Web updater 对 R6-C 目标需要
     Engine 迁移的路径必须 hard fail-closed/manual-required；`--no-engines` 仅允许在所有运行 Engine
     已是 gate-capable digest 且 authority writer 健康时保留，否则拒绝。该增量不得声称完成
     production wrapper 或打开部署门禁。
- [x] `PROD-WRAPPER-IMP1-REVISED-PRE-A3/B3`：再次修订时必须补齐 A2/B2 的 P1：
  - `normal_committed` authority 字段语义固定：`issuer_boot_id` 为 writer boot UUID；
    同一 writer/line 的 `authority_epoch` 随 normal identity 变化单调递增；`lease_seq` 同 identity 内
    单调递增；`state_digest` 为 canonical normal state（protocol、writer identity、exact Engine facts、
    image ABI、data-root identity）SHA-256；`commit_id` 为该 digest 前 32 hex。
  - writer 读 Docker/run-id 后必须二次 inspect 同一 container/image/StartedAt/RestartCount，完全一致才写；
    任何 mixed generation 拒绝写入。
  - gate 侧必须在每次 socket admission 请求时同步检查本地 fail-closed fence：`engine-maintenance.json`、
    `pcscf-rebind.json`，以及 writer 发布的 `admission-deny` 文件；存在即当次 DENY，不依赖
    3 秒 TTL。writer 遇到 global/line fence、legacy ABI、facts unknown 或写失败时先原子发布
    `admission-deny` 并移除/损坏旧 authority，再等待 gate probe/status DENY；恢复 allow 时必须先连续
    写入两步 authority warmup，再移除 deny 并证明 gate probe ALLOW。
  - 所有 public reload 模式共享 preflight：目标源码/镜像要求 `mdd-admission-v1` 且存在 running legacy
    Engine 时拒绝；`--engines` 在完整 wrapper 未完成前拒绝；Web updater 必须在 `apply_tree()` 前基于
    解包后的 `source_root` 做同等目标/现状检查，不能半升级 checkout。
- [x] `PROD-WRAPPER-IMP1-REVISED-PRE-A4`：A3 后续修订还必须满足：
  - gate 本地 fence 命中时不仅返回 DENY，还必须执行等价 `state.deny(local_fence_*)` 清除内存中的旧
    ALLOW authority，防止 `admission-deny` 移除后复活旧缓存。
  - writer 进入 deny 时顺序为：原子发布 `admission-deny` → 移除/poison 旧 authority → 通过 gate probe
    或 status 证明 DENY 且旧 state 已清；若无法证明，writer 状态 unhealthy/manual-required，公开
    reload/updater gate 失败。
  - writer 恢复 allow 时顺序为：确认 deny 已清过旧状态 → 移除 `admission-deny` → 写当前 identity 的
    seq1/seq2 warmup → probe/status 必须匹配当前 writer 期望的 `authority_epoch/lease_seq`，并通过
    gate status 暴露的当前 `commit_id/state_digest` 或 identity digest 校验，不能只看 ALLOW。
  - per-line epoch/identity 状态需要持久化，避免 writer 重启后健康检查无法区分旧缓存与当前 writer
    authority；writer 重启可触发新 identity warmup，但不得把旧 ALLOW 误作当前健康。
- [x] `PROD-WRAPPER-IMP1-POST-A5/B5`：两条既定复审链只审本轮增量实现与测试证据：
  gate 本地 fence 清缓存、normal authority writer 的 deny/allow 顺序与当前 identity 证明、
  reload/updater fail-closed 门禁，以及不引入付费通话/SMS replay、误授权、真实部署或实机计费动作。
  初审结论均为 `NEEDS CHANGES`，P1/P2 已登记并由 FIX1 整改。
- [x] `PROD-WRAPPER-IMP1-POST-A6/B6`：两条复审链只审 FIX1 四项整改：
  `allow_not_proven` 主动撤权、engine list unknown 逐线撤权、reload/updater 当前健康证明、
  malformed terminal `control-upgrade.json` fail-closed。A6 `PASS`；B6 `NEEDS CHANGES`，要求
  `admission-deny` 写失败时也要 best-effort 移除/poison 旧 authority。
- [x] `PROD-WRAPPER-IMP1-POST-A7/B7`：两条复审链只审 FIX2：
  `_publish_deny()` 在 `admission-deny` 写失败时 best-effort remove/poison 旧 authority，并写
  unhealthy/manual status；新增测试必须证明 stale authority 被移除且 health gate 不会通过。A7 `PASS`；
  B7 `NEEDS CHANGES`，要求 fallback remove/poison 后也必须等待 gate DENY proof。
- [x] `PROD-WRAPPER-IMP1-POST-A8/B8`：两条复审链只审 FIX3：
  `admission-deny` 写失败 fallback 在 remove/poison authority 后调用 `_wait_denied()`；证明失败返回
  `deny_write_failed_not_proven` unhealthy，证明成功才返回 `deny_write_failed` healthy deny。A8 `PASS`；
  B8 `NEEDS CHANGES`，要求 gate socket admission 自身同步检查 authority missing/invalid，消除等待期间并发窗口。
- [x] `PROD-WRAPPER-IMP1-POST-A9/B9`：两条复审链只审 FIX4：
  gate socket admission 在处理每个 request 时同步读取 authority 文件；missing/invalid/poison 立即
  `state.deny(...)` 并 DENY，不等 watcher。A9/B9 均 `PASS`，IMP1 冻结为未部署状态。

### 防重复执行规则

1. 每次压缩恢复只允许执行上面第一个未勾选项；不得从 Goal 原文、聊天摘要或长历史自行生成另一个
   “当前任务”。完成一个勾选项时必须同时更新 `checkpoint_id`、`phase` 和 `next_action`。
2. 每次产品代码修改前，在本节登记 `implementation_id` 和预审编号；修改后登记精确文件清单与测试
   证据；两条复审结论都回写后，才把该实现标记为冻结。
3. 已冻结实现只能通过 `REOPENED(<原实现 ID>, <时间>, <环境>, <复现命令/日志>)` 重开。缺少其中
   任一项时视为旧信息或误报，只补调查记录，不改代码。
4. 子会话只接收当前 `checkpoint_id` 的增量范围；其旧结论以本文件记录为准。压缩后不得因子会话
   没有缓存上下文而让它重新评审整个模块。
5. 若本节与下方长账本矛盾，先只读核实并修正本节；在矛盾解除前不得修改产品代码、部署或实机拨号。

### 当前轮次账本（压缩后只续这张表）

| 轮次 / ID | 阶段 | 状态 | 证据 / 唯一下一步 |
|---|---|---|---|
| `BROWSER-CALL-READINESS-PRESENTATION-20260823T1534+08` | IMS 注册与本浏览器可通话状态分层 | `已预审→已实施→本地测试 PASS→三方复审 PASS→已部署` | 生产证据：line 1/7 IMS Registered、egress ready，但 `media-ingress-confirmations.json` 不存在，`media_ingress.status()` 对 10.44.0.23/192.168.111.225/100.64.0.71 均 `confirmed=false`，Asterisk `webrtc` endpoint 无 contact。预审：UI 要求状态分层且不影响短信/蜂窝；网络 PASS 不改后端；付费安全要求动作函数和 incoming Answer 双 gate。实施：`lineCallReadinessStatus()` 分层输出 IMS/蜂窝/browser voice；通话/短信/日志 SimSelector 共用 browser voice display-only；设备页 display-only；Softphone `placeCall()`、`verifyMedia()`、历史回拨和 answer 函数体用 `vowifiReady` fail-closed；GlobalCallOverlay Answer 需要当前 `mediaIngress.confirmed===true`。验证：全部 webui 脚本测试和 `npm build` PASS；三方实施后复审 PASS；无真实通话/SMS。部署：记录目录 `/opt/mdd-gateway/data/deploy-records/codex-20260823T1534+0800-browser-call-readiness-ui`，最终 Control image `sha256:6e636fccbe5d97d7ab319f9e5cf7a26c393f90191e7cea4f981f374cd635396e`；首次 commit entrypoint 污染已修复并记录。部署后直连 HTTPS 200，paid-safety 计数无新增。剩余：浏览器确认 media-ingress 后跑无资费 Echo/RTP canary；line 7 当前 Rejected 需继续排查。 |
| `VOWIFI-INCOMING-FALLBACK-EXACT-HANGUP-FIX1-20260823T1503+08` | 无 WebRTC contact 时来电交互 fallback | `初次复审 NEEDS_CHANGES→FIX1 已实施→已测试→三方复审 PASS→已部署` | 根因：backend `call_in` 只 toast，GlobalCallOverlay 只来自 JsSIP incoming；当 media-ingress 未确认/softphone 无 contact 时页面只有 banner。预审：UI/网络均要求不能用 broad `/api/instances/{iid}/hangup`，必须 exact source。初次复审问题：生产 `source_call_id=<engine_run_id>:<linkedid>` 未拆分/按 run-id 精确校验，空 source 可绕过，maintenance fence 未放行新 exact hangup，前端 fallback Hang up 异步回调可覆盖后来的真实 JsSIP incoming，overlay 未优先真实可接听来电。FIX1：call records 保存/匹配 `engine_run_id`，endpoint 要求完整 source 非空完全匹配、当前 runtime engine_run_id 完全一致、仅裸 linkedid 传 AMI；新 exact hangup 加入 maintenance 豁免；前端 captured backend guard 与真实 JsSIP 优先选择。验证：聚焦 `7 passed, 1 warning, 8 subtests passed`；较宽后端 `184 passed, 1 warning, 25 subtests passed`；前端相关脚本 PASS；`npm build` PASS；diff check PASS。复审：call_ui_pre_review/network_mac_pre_review/prod_wrapper_paid_pre 均 PASS。部署：runtime overlay image `sha256:d59c80909605c2fddbb4e775d40c9215e24ad06ba36aa1407f9c0d21720e53f1`，记录目录 `/opt/mdd-gateway/data/deploy-records/codex-20260823T1508+0800-vowifi-incoming-fallback-fix1`；最终验收 line 1/7 Registered、0 active calls/channels、paid-risk 计数无新增。下一步：实际浏览器完成 media-ingress confirm/Echo canary 后，才能声明可接听。 |
| `BROWSER-MEDIA-INGRESS-UNCONFIRMED-20260823T1443+08` | 浏览器媒体入口确认 | `已定位；需要实际浏览器确认，未触发通话` | 只读证据：`media-ingress-confirmations.json` 为空/不存在；Control 逻辑下 Host `10.44.0.23:8443`→`tun0/10.44.0.23`、`192.168.111.225:8443`→`wlp4s0/192.168.111.225`、`100.64.0.71:8443`→`tailscale0/100.64.0.71` 均有唯一 candidate，但 `confirmed=false`。现网 active dist 含 `Confirm browser voice route` 提示和 `/api/system/media-ingress/confirm` 动作。Asterisk engine-1/7 `webrtc` endpoint 均 `Unavailable` 且无 browser contact；这会导致后端 incoming 只 toast、没有 JsSIP incoming 全屏弹窗。in-app Browser 访问 `https://10.44.0.23:8443/` 被自签证书拦截，未绕过。唯一下一步：用户在实际已信任/已登录浏览器点击“Confirm and test”，或提供已通过证书的页面标签；随后复核 `media-ingress-confirmations.json`、softphone provisioning enabled、Asterisk webrtc contact，再跑无资费 Echo/RTP canary。 |
| `ADMISSION-WRITER-SOURCEPIN-20260823T1438+08` | normal admission writer 恢复 | `已预审→已测试→已部署→已复审，PASS；浏览器媒体未验收` | 根因：orchestrator 以 `/opt/mdd-gateway/host` 为脚本目录运行，`engine.admission_gate` 无法导入，旧 writer 缓存 `None` 后持续 fail-closed。修复 `host/mdd_admission_authority.py`：repo-root `sys.path[0]`、source-pin `engine/admission_gate.py`、bad module cache clear、wrong source/protocol/missing probe/import failure fail-closed、deny proof 返回 `(proved,cause)`。本地 `tests/test_admission_authority.py tests/test_engine_admission_gate.py tests/test_update_apply.py` 为 `65 passed`；远端记录 `/opt/mdd-gateway/data/deploy-records/codex-20260823T142649+0800-admission-writer-lazy-import`，部署 hash `368f7544f28780c843edf792ccae44ac307646792ae388a37bf849f001070a17`，只重启 orchestrator。生产 line 1/7 authority/gate digest/epoch/lease_seq/run-id 一致、`allowed=True`，`admission-deny` absent，SWu CONNECTED、USIM AUTH_OK、PJSIP Registered；0 active channels/calls，paid-work/Originate/MessageSend 均 0。`network_mac_pre_review` 和 `prod_wrapper_paid_pre` 复审 PASS。下一步只做无资费浏览器 media-ingress/WebRTC contact/Echo RTP canary；真实来电/拨号继续禁止，除非用户另行明确授权。 |
| `DOWNLOAD-PROXY-TUIC-20260823T1621+08` | 宿主下载代理 final 切换 | `已部署，验证 PASS；只重启代理服务` | 用户反馈代理仍慢。现网 `/etc/mdd/download-proxy.json` 原 `route.final=🇺🇸 yovey.me anytls`；对订阅候选有界测速后，TUIC 可用且 GitHub API 约 0.49s，shadowsocks/trojan/anytls 约 0.52s，vless reality/h2/grpc 异常，hysteria2 校验不通过。最小改动：同一订阅、同一 inbound、同一端口，仅将 `route.final` 切到 `🇺🇸 yovey.me tuic`，记录目录 `/opt/mdd-gateway/data/deploy-records/codex-20260823T162106+0800-download-proxy-tuic`；`sing-box check` PASS。验证四入口：gstatic 均 204（约 0.41–0.44s）、GitHub API 均 200（约 0.47–1.62s）、Docker Registry 均 401（约 0.57–0.64s），journal 显示 `outbound/tuic`；Docker/Control/Engine 容器未重启。 |
| `HOST-SOCKS-PROXY-20260823T1419+08` | 宿主下载代理整改 | `已部署，代理验证 PASS；Docker 重启副作用已转入 admission writer 修复闭环` | 用户反馈现有代理太慢，要求宿主机起 sing-box socks inbound 并使用 yovey 订阅出站。10.44.0.23 已在 `/opt/mdd-gateway/data/deploy-records/codex-20260823T141514+0800-host-socks-proxy` 留存记录；`mdd-download-proxy.service` 使用订阅内 shadowsocks/trojan/anytls/tuic + `urltest`，监听 `127.0.0.1:24444`、`172.17.0.1:24444` socks 和兼容旧调用方的 `127.0.0.1:24445`、`172.17.0.1:24445` mixed。验证四入口与 Control 容器内 socks 均返回 HTTP 204，出口 `185.238.248.134`；2026-08-23 14:38 复核四入口仍为 `204`，mixed 约 0.42s、socks 约 0.62–0.74s。`/etc/environment` 与 Docker daemon drop-in 已改成本机代理；dockerd 进程环境已确认 `dockerd_old_proxy_ref=no`。Docker daemon 重启前 engine-1/7 均 `0 active channels/0 active calls`；未拨号、未短信。副作用：Docker 重启导致 mdd 容器被重启/重排，一度暴露 normal admission writer fail-closed；该副作用已由 `ADMISSION-WRITER-SOURCEPIN-20260823T1438+08` 记录闭环。 |
| `PROD-AGENT-HEALTH-PERSIST-FIX-20260823` | 生产最小修复 | `已预审→已测试→已复审→已部署→已只读验收` | 根因：`AgentHealthRegistry._persist()` 合并 active `attachment.public()` 后无条件覆写 `online=false/connection=offline`。修复：只移除 `session_id`，active 连接按真实 freshness 写盘；`_load()` 仍强制历史记录 offline。验证：本地 `py_compile` + `tests/test_agent_health.py` 为 `24 passed`；`network_mac_pre_review` 预审/复审 PASS；10.44.0.23 Control overlay image sha256:105a37fdee8e... 已部署，记录在 `/opt/mdd-gateway/data/deploy-records/codex-20260823-agent-health-fix`；生产 `agent-health.json` 三台 agent 均 `fresh/online` 且无 `session_id`。未拨号、未发短信。 |
| `R6-C-PRE` + `R6-C-ENTRY-AUDIT-1` + `R6-C-LOC-1` | 实施前双评审 | `PASS，冻结` | 已确定 strict C；不得重新选方案或重做全量预审。 |
| `R1b-DEPLOY-2-PRE8-R6-C-ENGINE-IMP1` | 实施 | `已实施` | 精确文件清单与内容见下方“R6-C 首段实施记录”；不得再实施一遍。 |
| `R6-C-LOCAL-TEST-1` | 本地无资费验证 | `PASS` | 新增 19、聚焦 89、全量 1220 PASS；不得因压缩而重跑，只在新增代码后跑受影响集。 |
| `R6-C-ENGINE-POST-A1` | 实施后增量复审 A | `NEEDS CHANGES，FINAL` | 连续五轮聚焦均 `19 passed`，但先前已复现一次 flaky；无修改/部署/计费操作。A1-F1..F4 及 B1-F1..F3 已确认。 |
| `R6-C-ENGINE-POST-B1` | 实施后增量复审 B | `NEEDS CHANGES，FINAL` | 聚焦 `55 passed`，无修改/部署/计费操作；B1-F1..F3 已登记，并交叉确认 A1-F1/F3。 |
| `R6-C-ENGINE-IMP2-PRE-A2/B2` | 最小整改预审 | `NEEDS CHANGES` | A2：completion 证据、RP 严格长度、entrypoint 早期 PID1；B2：tag inspect/create TOCTOU。均已回写修订案。 |
| `R6-C-ENGINE-IMP2-PRE-A3` | 修订后最小整改预审 A | `PASS` | RP 证据/长度、早期 PID1 process-group、canonical image ID 均 PASS；保留精确实施语义。 |
| `R6-C-ENGINE-IMP2-PRE-B3` | 修订后最小整改预审 B | `NEEDS CHANGES` | 唯一缺口：固定 child-marker 可由初始容器 env 预置并绕过 supervisor。 |
| `R6-C-ENGINE-IMP2-PRE-A4/B4` | 独立 runtime 脚本增量预审 | `PASS，冻结` | 双链确认独立 `engine-runtime.sh`、无 marker 分支、single PGID/deadline 与双脚本原子发布。 |
| `R1b-DEPLOY-2-PRE8-R6-C-ENGINE-IMP2` | 最小整改实施 | `已测试` | 冻结六项已实施；聚焦 131 PASS、全量 1233 PASS、静态检查 PASS；精确文件与指纹见下方。 |
| `R6-C-ASTERISK-E2E-1` | 真实 Asterisk 一次性无资费 E2E | `PASS` | fixed source + 全部生产 patch 实编译/链接；无 SIM/无资费 E2E 全门禁 PASS。禁止压缩后重复跑。 |
| `R6-C-ENGINE-IMP2-POST-A5/B5` | 实施后双链增量复审 | `NEEDS CHANGES` | B5 P1 已复现：过期 authority 下 AMI `To: pjsip:volte_ims@example.invalid` 仍发出 SIP packet；IMP2 不可冻结。 |
| `R6-C-ENDPOINT-DOMAIN-REOPEN-1` | 无资费复现 | `CONFIRMED` | 私有 runner A，Docker `--network none` + 容器内 `dummy0`，loopback UDP sink；raw-string gate 覆盖 `pjsip:volte_ims/..` 但不覆盖 `pjsip:volte_ims@example.invalid`。产品代码未修改、未部署、未拨号、未发短信。 |
| `R6-C-ENDPOINT-DOMAIN-PRE-A6` | Asterisk 修复语义预审 | `PASS` | 确认最小位置为 `msg_send()` 中 `ast_sip_get_endpoint()` 成功后、任何 request/body/header mutation 前；exact endpoint id `volte_ims` 执行 `sms_out`；删除 raw destination gate；保持 endpoint RAII cleanup 与 RP completion 路径。 |
| `R6-C-ENDPOINT-DOMAIN-PRE-B6` | 测试与验证边界预审 | `PASS` | 初审要求 endpoint@domain expired fail-first、renewed exactly-one、非 IMS endpoint 负控、本地 patch anchor/无 raw gate 断言；修订方案复核 PASS。 |
| `R1b-DEPLOY-2-PRE8-R6-C-ENDPOINT-DOMAIN-IMP3` | 最小修复实施 | `已测试` | 只改 `engine/patches/asterisk/mdd_admission.py`、`tests/test_asterisk_admission_patch.py`、`tests/e2e_asterisk_admission_linux.py`；聚焦 108 PASS、语法/diff check PASS、真实 Asterisk 无资费 E2E PASS。 |
| `R6-C-ENDPOINT-DOMAIN-POST-A7` | 实施后 Asterisk 语义复审 | `PASS` | 无 findings；确认 endpoint-id gate、RAII cleanup、raw gate removed、非 IMS/RP completion 边界满足。 |
| `R6-C-ENDPOINT-DOMAIN-POST-B7` | 实施后测试证据复审 | `PASS` | B7 确认 exact body、SIP 200 OK 防重传、1 秒 no-extra-packet、module-load wait、TODO 指针和无资费 E2E 证据均满足。 |
| `R6-C-ENGINE-FREEZE-1` | 冻结 | `FROZEN` | R6-C strict positive admission gate + endpoint@domain IMP3 已冻结；仍未部署、未实机拨号/短信。后续 supervisor/wrapper 段另开同样门禁。 |
| `PROD-WRAPPER-SCOPE-1` | 只读定位 | `DONE` | 已确认缺口不在 `MaintenanceSupervisor`/`MaintenanceProxy` 原语本身，而在生产 `install.sh reload` 与 Web 一键更新 `host/mdd_update.py` 的接入：当前直接重启 Control/可直接删除 Engine，没有创建完整 manifest/proxy/self-facts，也没有 supervisor recover/revoke 门禁；除 E2E/单测外也没有 `admission-authority.json` authority writer。下一步只把该最小接入范围交给 A/B 双预审。 |
| `PROD-WRAPPER-PRE-A1` | 生命周期/manifest 预审 | `NEEDS CHANGES` | `recover()` 只能在 `rollback_committed` 后打开 rollback Control，不能作为升级期间 full proxy；local mode 没有可证明 rollback Control generation；端口/用户访问路径未定义；公开 reload/updater 未覆盖；authority writer 是硬依赖。建议先只落 docker-mode/`--no-engines`/normal authority issuer，local 与 `--engines` fail-closed。 |
| `PROD-WRAPPER-PRE-B1` | 付费/通话/SMS 预审 | `NEEDS CHANGES` | 无生产 authority writer；R6-C 部署场景中 `--no-engines` 不允许保留 legacy Engine；`reload --engines` 直接 `docker rm -f` 必须 hard deny 或 wrapper-only；旧 marker 必须保留到新 gate exact 证明完成。 |
| `PROD-WRAPPER-IMP1-REVISED-PRE-A2` | 修订预审 A | `NEEDS CHANGES` | authority 字段语义未固定；writer 必须 Docker/run-id 二次 inspect 防混代；所有 public reload 模式都要 gate；updater 必须在 `apply_tree()` 前做目标/现状 ABI 检查；`--no-engines` 成功条件需消费 writer/gate 健康。 |
| `PROD-WRAPPER-IMP1-REVISED-PRE-B2` | 修订预审 B | `NEEDS CHANGES` | 被动停止续租不够，旧 ALLOW 可留到 TTL；deny 条件必须主动撤权并纳入 `pcscf-rebind.json`、`engine-maintenance.json`、global fence。writer 不应把 paid-work 状态作为 normal allow 条件；paid drain 仍属于 wrapper/supervisor。 |
| `PROD-WRAPPER-IMP1-REVISED-PRE-B3` | 修订预审 B | `PASS` | B3 认可 gate 每次 admission 同步检查本地 fence，writer 主动 deny/allow handoff，writer 不读取 paid-work，`--engines` hard fail 与 updater preflight-before-apply 的最小范围。 |
| `PROD-WRAPPER-IMP1-REVISED-PRE-A3` | 修订预审 A | `NEEDS CHANGES` | `admission-deny` 不能只覆盖响应，必须清掉 gate 内存 authority；writer 的 ALLOW probe 必须证明当前 identity/epoch/seq，最好由 status 暴露 commit/state digest；per-line epoch 需持久化。 |
| `PROD-WRAPPER-IMP1-REVISED-PRE-A4/B4` | 最终修订预审 | `PASS` | A4 确认本地 fence 命中清缓存、status identity 校验、deny/allow 顺序和持久 epoch 后可实施；B4 确认不引入付费/通话/SMS replay 或误授权。当前只实施该最小增量，生产部署仍 DENY。 |
| `R1b-DEPLOY-2-PRE8-PROD-WRAPPER-IMP1` | 实施与本地验证 | `已测试，待复审` | 已实现 gate 本地 fence、host normal authority writer、reload/updater fail-closed 门禁；修复自查发现的“恢复 allow 前无法证明旧 DENY 已清仍移除 deny”缺口。本地聚焦 86 PASS、宽受影响 214 PASS、全量 1241 PASS；`py_compile`、`bash -n`、`git diff --check` PASS。未部署、未拨号、未发短信。 |
| `PROD-WRAPPER-IMP1-POST-A5` | 生命周期/门禁复审 | `NEEDS CHANGES` | P1：`allow_not_proven` 后遗留 authority；Docker engine list unknown 未逐线 deny；reload/updater 健康门禁可被 stale status 欺骗；malformed `control-upgrade.json` phase=committed 被误认为 safe。 |
| `PROD-WRAPPER-IMP1-POST-B5` | 付费/SMS 安全复审 | `NEEDS CHANGES` | P1：allow proof 失败可能 gate 已 ALLOW；engine list unknown 只写 aggregate 不主动 deny。P2：健康门禁缺 freshness/identity 关联；updater 只按 component label 枚举可能漏掉 name-owned legacy Engine。 |
| `R1b-DEPLOY-2-PRE8-PROD-WRAPPER-IMP1-FIX1` | A5/B5 整改与本地验证 | `已测试，待复审` | 四项整改完成：allow proof 失败立即 `_publish_deny()`；engine list unknown 按 persisted state 和 `instances/*/run` 逐线 deny；reload/updater 使用当前 `updated_at_ns` + identity/state digest + epoch/seq 关联健康证明；`control-upgrade.json` 复用 strict manifest 校验并要求 terminal line phases。聚焦 91 PASS、宽受影响 219 PASS、全量 1246 PASS；`py_compile`、`bash -n`、`git diff --check` PASS。未部署、未拨号、未发短信。 |
| `PROD-WRAPPER-IMP1-POST-A6` | 生命周期/门禁复审 | `PASS` | A6 确认 FIX1 已覆盖 A5 四类 fail-closed/证明链问题，未发现旧 authority 复活、mixed generation、reload/updater 半升级或状态 proof 不足。 |
| `PROD-WRAPPER-IMP1-POST-B6` | 付费/SMS 安全复审 | `NEEDS CHANGES` | P2：`_publish_deny()` 先写 `admission-deny` 后删 authority；若 deny 写失败，旧 authority 仍可能保留到 TTL。要求 deny 写失败时也 best-effort remove/poison 旧 authority，并写 unhealthy/manual status。 |
| `R1b-DEPLOY-2-PRE8-PROD-WRAPPER-IMP1-FIX2` | B6 整改与本地验证 | `已测试，待复审` | `_publish_deny()` 在 `admission-deny` 写失败时 best-effort remove/poison 旧 `admission-authority.json`，写 `deny_write_failed` unhealthy 状态；成功 deny 路径也统一使用 remove/poison helper。新增单测覆盖 deny 写失败且 stale authority 被移除。聚焦 92 PASS、宽受影响 220 PASS、全量 1247 PASS；`py_compile`、`bash -n`、`git diff --check` PASS。未部署、未拨号、未发短信。 |
| `PROD-WRAPPER-IMP1-POST-A7` | 生命周期/门禁复审 | `PASS` | A7 确认 FIX2 的 remove/poison helper 和 deny 写失败 unhealthy 状态，未发现 fail-closed/线性化缺口。 |
| `PROD-WRAPPER-IMP1-POST-B7` | 付费/SMS 安全复审 | `NEEDS CHANGES` | P2：deny 写失败 fallback 删除 authority 后立即返回，gate watcher 下一轮前仍可能内存 ALLOW。要求 fallback 也 `_wait_denied()`，未证明则 `deny_write_failed_not_proven` unhealthy。 |
| `R1b-DEPLOY-2-PRE8-PROD-WRAPPER-IMP1-FIX3` | B7 整改与本地验证 | `已测试，待复审` | deny 写失败 fallback 在 remove/poison 后等待 gate DENY proof；证明失败返回 `deny_write_failed_not_proven` unhealthy，证明成功返回 `deny_write_failed` healthy deny。新增 real `GateService` 测试覆盖先 ALLOW、deny 写失败、返回前已变 DENY。聚焦 93 PASS、宽受影响 221 PASS、全量 1248 PASS；`py_compile`、`bash -n`、`git diff --check` PASS。未部署、未拨号、未发短信。 |
| `PROD-WRAPPER-IMP1-POST-A8` | 生命周期/门禁复审 | `PASS` | A8 确认 FIX3 的 fallback `_wait_denied()` 与 real GateService 测试，未发现 fail-closed/线性化缺口。 |
| `PROD-WRAPPER-IMP1-POST-B8` | 付费/SMS 安全复审 | `NEEDS CHANGES` | P2：writer 等待 DENY 只能保证返回后安全；等待期间 gate socket 不同步读 authority，若 `admission-deny` 写失败则并发 paid admission 仍可能用内存 ALLOW。 |
| `R1b-DEPLOY-2-PRE8-PROD-WRAPPER-IMP1-FIX4` | B8 整改与本地验证 | `已测试，待复审` | gate socket admission 每次请求同步读取 authority 文件；missing/invalid/poison 立即 `state.deny(...)` 并 DENY，不等 watcher。新增测试覆盖长 watcher interval 下 authority 删除/poison 后立即 probe DENY，以及 writer fallback remove 后、wait 前并发 probe DENY。聚焦 94 PASS、宽受影响 222 PASS、全量 1249 PASS；`py_compile`、`bash -n`、`git diff --check` PASS。未部署、未拨号、未发短信。 |
| `PROD-WRAPPER-IMP1-POST-A9` | 生命周期/门禁复审 | `PASS` | A9 确认 gate socket admission 顺序为 local fence → sync authority → state.check；missing/invalid/poison 立即清缓存并 DENY。 |
| `PROD-WRAPPER-IMP1-POST-B9` | 付费/SMS 安全复审 | `PASS` | B9 确认 FIX4 关闭 paid path watcher-interval 窗口；未发现剩余 paid call/SMS replay、TTL/window 或 deploy-gate bypass。 |
| `R1b-DEPLOY-2-PRE8-PROD-WRAPPER-IMP1-FREEZE-1` | 冻结 | `FROZEN_NOT_DEPLOYED` | production normal authority + reload/updater fail-closed gates 已冻结；完整 production replacement wrapper、真实部署、拨号、短信、SIM/VoWiFi 计费动作均未执行且仍 DENY。 |
| `CALL-UI-MAC-AGENT-NET-IMP1-PRE` | 当前轮实施前双评审 | `NEEDS_CHANGES → 已采纳` | `call_ui_pre_review` 要求 App 级单一 CallCoordinator、Softphone 只订阅/控制、切页/换线/media-ingress revision 不 teardown 活跃/振铃通话；`network_mac_pre_review` 要求保留每次 route-bound media admission，并补 Agent 发布/音频协议契约 fail-closed。 |
| `CALL-UI-MAC-AGENT-NET-IMP1` | 实施与本地验证 | `已复审，NEEDS_CHANGES` | 已实现 App 级 VoWiFi CallCoordinator、全局来电 Answer/Decline 与通话 mini-widget、Softphone 改为 coordinator 控制视图；Agent 上报 `call_contract.version=2` 与 `audio_telemetry_version`，服务端对旧包/helper v1 fail-closed。复审 A 发现旧 UA 迟到事件和共享 audio removal；复审 B 发现 package_version 只上报未校验 digest。 |
| `CALL-UI-MAC-AGENT-NET-POST-A1` | UI/生命周期复审 | `NEEDS_CHANGES，FINAL` | P1：`BrowserPhone.stop()` 后 `emit()` 未全局检查 `_dead`，旧 UA 迟到 `registered/ended/failed` 可覆盖新状态；coordinator callback 未验证事件来源仍是当前 phone。P1：`stop()` 无条件 `remoteAudio.remove()`，idle 线路 reload 可移除 App 共享 audio sink。 |
| `CALL-UI-MAC-AGENT-NET-POST-B1` | 网络/mac agent/协议门禁复审 | `NEEDS_CHANGES，FINAL` | P1：`package_version` 仅上报，`call_contract_reason()` 未校验不可变 package/manifest digest；旧包可伪装 v2/v2 通过。 |
| `CALL-UI-MAC-AGENT-NET-IMP1-FIX1` | A1/B1 整改与本地验证 | `已复审，UI PASS / NET NEEDS_CHANGES` | `_dead` 后所有 softphone 事件吞掉，coordinator 校验事件来源；外部共享 audio 只在本 phone 绑定的 stream 匹配时清理，绝不 remove；蜂窝 call 存在时能力撤回不自动切走 UI；Agent 上报 `package_digest`，服务端按 `MDD_ALLOWED_AGENT_PACKAGE_DIGESTS`/配置 allowlist 精确匹配，unknown/mismatch fail-closed。UI 复审 PASS；网络复审要求补 mac 发布包 manifest 和 Control allowlist 安装闭环。 |
| `CALL-UI-MAC-AGENT-NET-POST-B2` | mac 发布闭环复审 | `NEEDS_CHANGES，FINAL` | P1：digest compare 正确，但 macOS CLI/GUI package 没有生成/携带 `manifest.json`，安装/发布流程没有把同一 digest 写入 Control allowlist，默认会 unknown/disabled。 |
| `CALL-UI-MAC-AGENT-NET-IMP1-FIX2` | B2 整改与本地验证 | `已复审，NEEDS_CHANGES` | 新增 `agent/package_manifest.py` 与 `agent/macos/Build-MacOS-Package.sh`，mac package root 生成 `manifest.json`、GUI runtime manifest copy 和 `control-agent-allowlist.env`；`agent/modem_agent.py` 支持 `_MEIPASS`/GUI Resources manifest lookup；`install.sh` local/docker 两条 Control 启动路径传入同一 allowlist；`docker-compose.yml` 暴露显式 env。复审发现 runtime 只 hash manifest，未校验 payload。 |
| `CALL-UI-MAC-AGENT-NET-POST-B3` | mac runtime integrity 复审 | `NEEDS_CHANGES，FINAL` | P1：保留 current `manifest.json` 但替换 CLI/GUI/helper payload 仍可报告 allowlisted digest；Agent 运行时必须严格解析 manifest 并校验所有 listed payload，同时拒绝额外/重复/越界/缺失。 |
| `CALL-UI-MAC-AGENT-NET-IMP1-FIX3` | B3 整改与本地验证 | `已复审，NEEDS_CHANGES` | `agent/modem_agent.py` 的 `_agent_package_digest()` 改为 verified digest：strict parse manifest、推导 CLI/package root 或 GUI Resources package root、校验每项 payload 的 size/sha256、拒绝未列 payload/重复/越界/缺失/篡改/符号链接；失败返回 `unknown`。复审发现 symlink directory 被静默跳过、任意嵌套同名 metadata 被全局忽略。 |
| `CALL-UI-MAC-AGENT-NET-POST-B4` | mac payload set 复审 | `NEEDS_CHANGES，FINAL` | P1：symlink directory 只从 `os.walk` dirnames 过滤，没有 fail-closed；任意子目录 `manifest.json`/`control-agent-allowlist.env` 被 basename 全局跳过，违反精确 payload 集合。 |
| `CALL-UI-MAC-AGENT-NET-IMP1-FIX4` | B4 整改与本地验证 | `已复审，NEEDS_CHANGES` | metadata 豁免改为精确绝对路径：root `manifest.json`、root `control-agent-allowlist.env`、GUI runtime `MDD Agent.app/Contents/Resources/manifest.json`；其他同名文件均为 extra。任何 symlink dir/file 直接 `unknown`。复审发现 package root 本身为 symlink 时仍可绕过。 |
| `CALL-UI-MAC-AGENT-NET-POST-B5` | mac package root symlink 复审 | `NEEDS_CHANGES，FINAL` | P1：manifest path 为 `/symlinked-package/manifest.json` 时 leaf 不是 symlink，`os.walk(root)` 不暴露起始 root，验证可通过。 |
| `CALL-UI-MAC-AGENT-NET-IMP1-FIX5` | B5 整改与本地验证 | `已复审，NEEDS_CHANGES` | `_verified_package_manifest_digest()` 在 walk 前拒绝 `os.path.islink(root)`；新增 symlinked package root → `unknown` 测试。复审发现 manifest schema 存在 type confusion：`version=True`、`size=True`/string/float、non-string name 可被接受。 |
| `CALL-UI-MAC-AGENT-NET-POST-B6` | manifest schema 复审 | `NEEDS_CHANGES，FINAL` | P1：JSON bool 等于 int 的 Python 语义和 `int(...)` 转换导致 malformed manifest 可过 schema；需要 exact type checks。 |
| `CALL-UI-MAC-AGENT-NET-IMP1-FIX6` | B6 整改与本地验证 | `已复审，PASS` | manifest verifier 改为 exact type checks：`version` 必须 `type is int` 且等于 1，entry `name`/`sha256` 必须原生 `str`，`size` 必须原生 `int`，拒绝 bool/float/string 和 non-string name/sha；新增 malformed cases 测试；相关 JS/pytest/build/py_compile/shell/diff-check 全部 PASS；未部署、未拨号、未发短信。 |
| `CALL-UI-MAC-AGENT-NET-POST-B7` | mac/network 最终复审 | `PASS` | B1–B6 闭环：macOS 包 manifest/allowlist、运行时完整性验证、严格 schema、unknown/mismatch/mutated/extra/nested metadata/各类 symlink 均 fail-closed；Control 使用同一 digest allowlist；App coordinator 与 fresh Echo/RTP canary 保持完整；未见 MTU/出口/国家路由回归。 |
| `CALL-UI-MAC-AGENT-NET-FREEZE-1` | 冻结 | `FROZEN_NOT_DEPLOYED` | App 级 VoWiFi CallCoordinator + mac/agent package digest gate + manifest payload integrity 已双复审 PASS；仍未部署、未实机 Echo、未真实拨号、未短信。 |
| `CALL-UI-MAC-AGENT-NET-CONT-VERIFY-1` | 继续验证 | `PASS / NOT_DEPLOYED_CONFIRMED` | 2026-08-23 10:44 复跑轻量 gate：VoWiFi JS 两组 PASS；`tests/test_modem_agent.py tests/test_agent_management.py tests/test_modem_registry.py tests/test_remote_modem_devices.py` 为 `259 passed, 2 subtests passed`；`py_compile`、`bash -n`、`git diff --check` PASS。pytest 实际执行 mac package script 的临时输出确认 root manifest、GUI runtime manifest copy 与 `control-agent-allowlist.env` digest 一致。只读核验 10.44.0.23：现网仅 `mdd-sim-gateway-control` 运行，远端源码/容器内 `control/app/main.py`、`agent/modem_agent.py`、`webui/dist/index.html` hash 与当前工作区不一致，说明现网尚未包含本轮修复。 |
| `CALL-UI-MAC-AGENT-NET-MAC-REALBUILD-AUDIT-1` | 真实 mac 包构建审计 | `PARTIAL / RELEASE_BUILD_BLOCKED` | 2026-08-23 10:47 只读/外置盘临时目录核实：本机没有 `cmake`、`ninja`、`PyInstaller`，`agent/dist` 不存在；`agent/macos/Build-MacOS-Package.sh` 是最终组装脚本，要求预先提供 PyInstaller CLI/GUI 产物、`MDD_CELLULAR_IO_BINARY` 和 `MDD_CALL_AUDIO_BINARY`。`agent/cellular-io/CMakeLists.txt` 要求固定 `LWIP_DIR` 与 `LIBUSB_ROOT`，`THIRD_PARTY.md` 明确发布构建消耗锁定源码包且不在客户 Mac 编译。已在外置盘临时目录成功 `go test ./...` 并构建 `agent/call-audio-helper`，输出 digest `253049a71c92485626647e956f2244403dc0dd1e0c90b1998331d0170a19925f`；但完整 mac 可发布包仍缺真实 `mdd-cellular-io` 和 PyInstaller 产物，不能声称已完成真实包构建。 |
| `CALL-UI-MAC-RELEASE-IMP2-PRE-1` | mac release 构建闭环预审 | `PASS` | `network_mac_pre_review` 三轮 `NEEDS_CHANGES` 后第四版 PASS。冻结语义：root-only manifest；development/unsigned 不生成 Control allowlist；release 要求签名 CLI、GUI app、helpers；wheelhouse manifest 需独立 SHA-256；archive 解包拒绝绝对路径、`..`、symlink/hardlink；不自动安装 Homebrew；不改 MTU/出口/国家路由。 |
| `CALL-UI-MAC-RELEASE-IMP2` | 实施与本地验证 | `已测试，待复审→PASS` | 实施 root-only manifest 与 package-root GUI discovery；`package_manifest.py --verify/--no-allowlist`；`Build-MacOS-Package.sh` honor `MDD_PYTHON`、参数化 PyInstaller work/dist、默认不覆盖 output、post verify、unsigned no allowlist；新增 `Build-MacOS-Release.sh`，要求外部 build/output root、锁定 libusb/lwIP、verified wheelhouse、arm64 Go/PyInstaller/file/lipo、ctest、otool、codesign。验证：`py_compile` PASS；`bash -n` PASS；`tests/test_modem_agent.py tests/test_agent_management.py tests/test_modem_registry.py tests/test_remote_modem_devices.py` 为 `260 passed, 2 subtests passed`；touched-files `git diff --check` PASS。未执行真实 release build、未部署、未拨号、未短信。 |
| `CALL-UI-MAC-RELEASE-IMP2-POST-1` | 实施后复审 | `NEEDS_CHANGES→PASS` | 初审发现 output 默认 `rm -rf` 与 arm64 证明不足；FIX1 改为默认 output 不存在、显式 `--overwrite` 且拒绝 unsafe path，release output 拒绝 system-temp/worktree/已存在；Go 强制 `GOOS=darwin GOARCH=arm64`，PyInstaller `--target-arch arm64`，helper/CLI/App 内所有 Mach-O 在签名/root manifest 前经 `file`+`lipo` 验证。复审 PASS。 |
| `CALL-UI-MAC-RELEASE-FREEZE-1` | 冻结 | `FROZEN_NOT_FULLY_BUILT_NOT_DEPLOYED` | macOS release 构建闭环代码已复审 PASS；真实 release 包仍未构建，因为本轮没有 release wheelhouse/sign identity 且未执行完整 libusb/lwIP/cmake/PyInstaller/sign/notarize 流程。现网 10.44.0.23 仍未包含这些修复。 |
| `CALL-UI-MEDIA-INGRESS-IMP3-PRE-1` | 多网卡/media ingress 预审 | `NEEDS_CHANGES` | 只读现网核验发现 `network-inventory.json` 把 MDD 自己创建的 `mdd-fr/mdd-gb/mdd-hk` per-line country egress TUN 当作浏览器 media ingress candidate，与真实用户入口 `wlp4s0/tailscale0/tun0` 混在一起。`network_mac_pre_review` 要求不能按前缀排除，必须有 exact ownership 来源；Control 必须先验证 raw inventory，再剔除 owned endpoint 并重算 effective generation。 |
| `CALL-UI-MEDIA-INGRESS-IMP3` | 实施与本地验证 | `已测试，待复审→PASS` | `host/mdd_orchestrator.py` 新增 generated sing-box TUN endpoint ownership、host inventory exact endpoint 排除、endpoint disappearance probe、atomic restore、`stop_singbox()`；`control/app/media_ingress.py` 读取 generated sing-box ownership，raw generation 验真后按 effective candidate 重新生成媒体 binding；`tests/test_media_ingress.py` 覆盖 MDD owned endpoint 增删/ifindex 抖动不影响 binding、用户 `mdd-vpn` 不误杀、planned apply 失败不发布 ownership、disable/restore 以 endpoint gone 为清 ownership 前提、malformed probe fail-closed。验证：`py_compile control/app/media_ingress.py host/mdd_orchestrator.py` PASS；`pytest tests/test_media_ingress.py tests/test_product_boundaries.py tests/test_country_egress.py tests/test_remote_modem_devices.py tests/test_line_lifecycle.py -q` 为 `268 passed, 27 subtests passed`；touched-files `git diff --check` PASS。未部署、未拨号、未短信。 |
| `CALL-UI-MEDIA-INGRESS-IMP3-POST-1` | 实施后复审 | `NEEDS_CHANGES×5→PASS` | 复审连续要求收紧生命周期窗口：planned config 不能提前成为 ownership；停用后必须确认 exact endpoint 消失才能删除 generated；新配置失败且旧配置恢复要原子写并验证旧进程；`ip -j` malformed/wrong-shape/非法 IPv4 local 一律按 present fail-closed。最终复审 PASS：只排除 exact active/generated sing-box endpoint，raw→effective generation 正确，用户 VPN 不按前缀误杀，不引入 MTU/出口/国家路由范围外修改。 |
| `CALL-UI-MEDIA-INGRESS-IMP3-FREEZE-1` | 冻结 | `FROZEN_NOT_DEPLOYED` | 多网卡/media ingress 内部 egress TUN ownership 修复已复审 PASS；现网 10.44.0.23 尚未部署本轮代码，真实 Echo/媒体路径验收未执行。下一步部署和实机验收仍需用户明确授权。 |
| `CALL-UI-STALE-STATUS-IMP4-PRE-1` | stale status cache 预审 | `PASS` | 只读/本地审计确认 `status_mod.compute()` 在 runtime stopped 时会返回 STOPPED，但 `_cached_line_status()` 作为 HTTP/UI 快照快速路径只要 cache 存在就直接返回旧状态，没有 TTL；若 poller 卡住或 `status_sampled_at` 缺失，旧 OK 可继续影响设备页能力和实例列表。`network_mac_pre_review` 认可最小修复在 `_cached_line_status()`：不做 live Docker probe，只做 bounded cache TTL 和 disabled 优先。 |
| `CALL-UI-STALE-STATUS-IMP4` | 实施与本地验证 | `已测试，待复审→PASS` | `control/app/main.py` 新增 `STATUS_CACHE_MAX_AGE_SECONDS`；`_cached_line_status()` 在 durable maintenance 后先处理 `enabled=false`，再检查 cache sample 时间必须是有限、非未来 monotonic 且未过 TTL；缺失/None/NaN/∞/未来/过期缓存返回 `REGISTERING/status_stale`，detail 只带 `stale_previous_state` 与 `stale_sample_age_seconds`；HTTP 路径不触发 Docker/Asterisk/AMI。`_with_status_activity()` 增加 `status_stale` 文案。验证：`py_compile control/app/main.py` PASS；`pytest tests/test_line_lifecycle.py tests/test_remote_modem_devices.py tests/test_status_registration.py -q` 为 `199 passed, 19 subtests passed`；touched-files `git diff --check` PASS。未部署、未拨号、未短信。 |
| `CALL-UI-STALE-STATUS-IMP4-POST-1` | 实施后复审 | `PASS` | 复审确认 TTL 基于 monotonic，缺失/None/NaN/∞/未来/过期值均 fail-closed 为 `REGISTERING/status_stale`；disabled 不会被 fresh OK 覆盖；stale 分支不会签发 OK 能力，不延长 poller 的 20 秒 OK grace，文案不误导为 P-CSCF 问题。 |
| `CALL-UI-STALE-STATUS-IMP4-FREEZE-1` | 冻结 | `FROZEN_NOT_DEPLOYED` | stale OK/status cache 修复已复审 PASS；现网 10.44.0.23 尚未部署本轮代码，真实页面/线路验收未执行。下一步部署和验收仍需用户明确授权。 |
| `CALL-UI-CELLULAR-INCOMING-IMP5-PRE-1` | cellular incoming 全局协调器预审 | `PASS` | `call_ui_pre_review` 要求做 App 级独立 `CellularIncomingController`，唯一 owner 执行 WS incoming→prepare→media phone→ring→Answer evidence→answerIncoming；Softphone 只能订阅/控制，不能再 own inbound cellular prepare/ring/answer；Decline 始终 `cellularCallHangup`，`cancelCellularCall` 只处理迟到未提交 prepare。 |
| `CALL-UI-CELLULAR-INCOMING-IMP5` | 实施与本地验证 | `已测试，待复审→PASS` | 新增 `webui/src/cellularIncomingCoordinator.js`、`webui/src/CellularIncomingOverlay.jsx`、`webui/tests/cellularIncomingCoordinator.mjs`；`webui/src/App.jsx` 挂载全局 cellular incoming overlay；`webui/src/views/Softphone.jsx` 删除 inbound cellular 页面级状态所有权，仅保留 outbound cellular；`control/app/main.py` 修复 `/cellular-call/hangup`：未 committed incoming prepared session 真实执行 remote `call.hangup` + authoritative `call.status`，未 committed outbound prepared session 仍只 cancel media。验证：`py_compile control/app/main.py` PASS；`pytest tests/test_remote_modem_devices.py tests/test_cellular_call.py tests/test_call_media.py tests/test_line_lifecycle.py tests/test_status_registration.py -q` 为 `212 passed, 19 subtests passed`；全部 webui 脚本 `test:cellular-incoming/test:cellular-media/test:vowifi-media/test:vowifi-media-behavior/test:agent-health/test:line-presentation/test:sms-submission` PASS；`npm --prefix webui run build` PASS；`git diff --check` PASS。未部署、未拨号、未短信。 |
| `CALL-UI-CELLULAR-INCOMING-IMP5-POST-1` | 实施后 UI/生命周期复审 | `NEEDS_CHANGES→PASS` | 初审发现 Answer 异步链在 Decline 后仍可能继续 `answerIncomingCellularCall`、已拒 source 的 ringing 重放可复活 overlay；修复为 answerToken、terminal source tombstone 后又发现前端调用 hangup 但后端未 committed incoming 只 cancel media。最终修复后复审 PASS：incoming prepared 真实物理挂断，outbound prepared 不误挂；前端 Decline/late-event/answer-token/tombstone 防护一致。 |
| `CALL-UI-CELLULAR-INCOMING-IMP5-POST-2` | 实施后网络/Agent/付费安全复审 | `NEEDS_CHANGES→PASS` | 初审发现 Decline 后晚到 browser `registered/incoming` 仍可能 ring、`stop({release:true})` 对 active/answering 只停浏览器媒体；修复为 releaseRequested+state gate、active/answering teardown 调用 `cellularCallHangup`。最终复审 PASS：incoming prepared 物理挂断并等待权威终态；outbound prepared 仅取消媒体，不误挂或产生付费动作；media evidence、Agent readiness、media ingress、MTU/出口/国家路由门禁未放宽。 |
| `CALL-UI-CELLULAR-INCOMING-IMP5-FREEZE-1` | 冻结 | `FROZEN_NOT_DEPLOYED` | cellular/remote modem 来电全局弹窗、接听、拒接和 teardown 语义已双复审 PASS；现网 10.44.0.23 尚未部署本轮代码，真实页面、无资费 Echo/媒体路径、蜂窝拒接实机验收未执行。下一步部署和验收仍需用户明确授权。 |
| `CALL-UI-RUNTIME-STATUS-IMP6-PRE-1` | Engine lifecycle 状态同步预审 | `NEEDS_CHANGES→PASS` | 只读现网核验确认 10.44.0.23 当前无 Engine 容器，line 1/7 run files/charon 日志为数小时前旧状态；生产 `_cached_line_status()` 仍是旧实现。预审要求 runtime stop/start/recreate 事件不能继续显示旧 `OK/Registered`；transition 不能伪装为 IMS 权威采样；不能触发 hangup/decline、媒体、付费、MTU、出口或国家路由修改。 |
| `CALL-UI-RUNTIME-STATUS-IMP6` | 实施与本地验证 | `已测试，待复审→PASS` | `control/app/main.py` 新增独立 `status_transitions`、per-line `status_runtime_epoch` 与 `status_publish_locks`；`runtime_changed()` 立即 bump epoch、清 `status_cache/status_sampled_at`、生成 `STOPPED/engine_stopped` 或 `REGISTERING/engine_changed` transition，并在 engine event 中附带 `status_transition` 后再广播标准 status transition；`_poll_instance_status()` 与 `push_status()` 发布前复核 captured epoch，旧 in-flight OK 样本不能回写；`_record_line_state()` 移出发布锁；删除/软删除清 transition。`webui/src/liveStatus.js` 与 `webui/src/App.jsx` 让 status 消息和带 transition 的 engine 消息走同一 live-status 更新路径；新增 `webui/tests/liveStatus.mjs`。验证：`py_compile control/app/main.py` PASS；定向 lifecycle/并发 7 PASS；`pytest tests/test_remote_modem_devices.py tests/test_line_lifecycle.py tests/test_status_registration.py tests/test_cellular_call.py tests/test_call_media.py -q` 为 `218 passed, 19 subtests passed`；`npm --prefix webui run test:live-status/test:line-presentation/test:cellular-incoming/test:vowifi-media/test:vowifi-media-behavior/test:agent-health/test:sms-submission` PASS；`npm --prefix webui run build` PASS；`git diff --check` PASS。未部署、未拨号、未短信。 |
| `CALL-UI-RUNTIME-STATUS-IMP6-POST-1` | 实施后 UI/状态机复审 | `NEEDS_CHANGES×2→PASS` | UI 复审先发现 engine event 与 status event 间仍有短暂旧绿窗口，已通过 engine `status_transition` 同包更新修复；网络复审先后发现旧 in-flight poll/push 可在 transition 后回写 OK、以及 `_record_line_state()` 在发布锁内会延迟 runtime transition，已通过 epoch+发布锁与锁外历史持久化修复。最终双复审 PASS：旧 OK 不会在 runtime transition 后复活，HTTP/UI 可即时去绿；CallCoordinator 仅保留 refreshPending/reload 语义，cellular incoming overlay 只处理 call event；未触碰媒体、Agent、付费、MTU、出口或国家路由。 |
| `CALL-UI-RUNTIME-STATUS-IMP6-FREEZE-1` | 冻结 | `FROZEN_NOT_DEPLOYED` | Engine lifecycle runtime status transition/epoch 修复已双复审 PASS；现网 10.44.0.23 尚未部署本轮代码，真实页面、Free/giffgaff/UK/FR VoWiFi 状态、Echo/媒体路径验收未执行。下一步部署和验收仍需用户明确授权。 |

每次评审、实施、测试或复审结束时，必须在同一次修改中更新本表和顶部
`checkpoint_id/phase/next_action`；不允许只把结果留在会话摘要或子会话里。

### CALL-UI-MAC-AGENT-NET-IMP1/FIX6 实施与验证记录（2026-08-23 10:39）

本轮只覆盖顶部 checkpoint 允许的三项最小整改：

- `webui/src/callCoordinator.jsx`：新增登录会话级 VoWiFi CallCoordinator。App 认证后常驻持有每条启用
  VoWiFi 线路的单一 `BrowserPhone`/UA、provisioning generation、呼叫状态、媒体测试状态和持久 audio sink；
  `mediaIngressRevision` 变化时，活跃/振铃通话只标记 `refreshPending`，终态后再重载 provisioning。
- `webui/src/App.jsx`：挂载 coordinator、全局持久 `<audio>` sink，以及任意页面可用的全局来电
  Answer/Decline overlay 和非来电通话 mini-widget。
- `webui/src/views/Softphone.jsx`：删除页面级 VoWiFi `Phone`/UA 所有权，改为纯订阅/控制视图；蜂窝通话仍保留
  原“prepare media → ring → browser evidence → answer”的链路，只通过 coordinator 复用同一个 audio sink
  创建短生命周期媒体 phone。
- `control/app/modem_registry.py`、`control/app/main.py`、`agent/modem_agent.py`：Agent 上报当前 call contract、
  音频 helper telemetry 版本和不可变 package/manifest digest；服务端对旧 agent 包、缺失 contract、helper v1、
  未知 telemetry、unknown digest 或 digest mismatch 统一 fail-closed，不把裸 `call_signalling/call_audio`
  布尔值误报为可通话。
- `CALL-UI-MAC-AGENT-NET-IMP1-FIX1` 采纳复审 A1/B1：`BrowserPhone.emit()` 在 `_dead` 后吞掉所有迟到事件；
  coordinator callback 校验事件来源仍是当前 phone；`stop()` 只移除自建 fallback audio，外部 App 共享 audio
  只在 srcObject 仍是本 phone 绑定 stream 时清理；蜂窝 call 对象存在时能力撤回不自动切回 VoWiFi，以保持挂断
  和状态轮询路径可见；服务端按 `MDD_ALLOWED_AGENT_PACKAGE_DIGESTS` 或配置 allowlist 精确匹配 agent digest。
- `CALL-UI-MAC-AGENT-NET-IMP1-FIX2` 采纳网络复审 B2：新增 `agent/package_manifest.py` 和
  `agent/macos/Build-MacOS-Package.sh`，macOS 最终 package root 生成 `manifest.json`、
  GUI app runtime manifest copy 与 `control-agent-allowlist.env`；Agent 运行时支持 `_MEIPASS`、
  executable 目录、GUI `Contents/Resources` 和显式 manifest 路径查找；`install.sh` local/docker 控制面
  启动路径传入同一 allowlist digest，`docker-compose.yml` 暴露显式 env。
- `CALL-UI-MAC-AGENT-NET-IMP1-FIX3` 采纳网络复审 B3：Agent 运行时不再只 hash manifest 文件；
  `_agent_package_digest()` 先 strict parse manifest，从 CLI package root 或 GUI `Contents/Resources`
  推导 package root，逐项校验 payload `size` 与 `sha256`，拒绝 duplicate、路径越界、缺失、篡改、
  symbol link 和未列 payload；只有完整校验通过才返回 manifest digest，否则返回 `unknown`。
- `CALL-UI-MAC-AGENT-NET-IMP1-FIX4` 采纳网络复审 B4：payload 集合枚举中 metadata 豁免只允许三个
  精确绝对路径：root `manifest.json`、root `control-agent-allowlist.env`、GUI runtime
  `MDD Agent.app/Contents/Resources/manifest.json`；任意其他同名文件视为 extra。任何 symlink directory
  或 file 直接 fail-closed 为 `unknown`。
- `CALL-UI-MAC-AGENT-NET-IMP1-FIX5` 采纳网络复审 B5：在 walk 前拒绝 package root 本身是 symlink
  的 manifest 路径，避免 `/symlinked-package/manifest.json` 绕过 symlink 检测。
- `CALL-UI-MAC-AGENT-NET-IMP1-FIX6` 采纳网络复审 B6：manifest schema 改为 exact type checks，
  `version` 必须原生 `int` 且等于 1，entry `name`/`sha256` 必须原生 `str`，`size` 必须原生 `int`；
  bool、float、string 数字和 non-string name/sha 均 fail-closed。
- 只读核验 `root@10.44.0.23`：当前只看到 `mdd-gateway-control` 运行，Engine 容器不在运行列表；
  远端 `/app/control/app/main.py`、`/app/host/mdd_orchestrator.py`、`/app/webui/dist/index.html`
  SHA-256 与本工作区不一致。该信息仅作复审/部署前证据；本轮未部署。

验证证据：

- `npm --prefix webui run test:vowifi-media` PASS。
- `npm --prefix webui run test:vowifi-media-behavior` PASS。
- `npm --prefix webui run build` PASS。
- `python3 -m pytest tests/test_agent_management.py tests/test_modem_agent.py tests/test_modem_registry.py tests/test_remote_modem_devices.py -q`
  PASS：`259 passed, 2 subtests passed`。
- `python3 -m pytest tests/test_agent_health.py tests/test_call_audio.py tests/test_media_ingress.py tests/test_media_admission.py tests/test_call_media.py tests/test_cellular_call.py tests/test_voice_registration.py -q`
  PASS：`73 passed`。
- `python3 -m py_compile agent/package_manifest.py agent/modem_agent.py control/app/modem_registry.py control/app/main.py` PASS。
- `bash -n install.sh agent/macos/Build-MacOS-Package.sh` PASS。
- `git diff --check` PASS。
- 未执行生产部署、未拨号、未发短信、未做真实 SIM/VoWiFi 计费路径操作。

实施后指纹（复审必须核对这些精确内容；变化即停止并重新登记 implementation）：

| 文件 | SHA-256 |
|---|---|
| `webui/src/callCoordinator.jsx` | `a03fd45d70cd8ac83d27367bfb9d94445c20cab26109dffa58cfbb224af8db18` |
| `webui/src/App.jsx` | `5857375871009f223921b2dedbb9fa73544be8d368eaf390d593b33887fb433a` |
| `webui/src/views/Softphone.jsx` | `e305464fbbedabedacc19835a3ff25f07a5227bb6cb39816593d5e16d6c97d75` |
| `webui/src/softphone.js` | `5f9350039ddb8dda402e6aca441c21b300b1054997c5867387c3239646372528` |
| `webui/tests/vowifiMediaGate.mjs` | `846f6d1e4fd8eaed6995b5c662b4d8f970252b594e67349ad1aa4f1614ff1b2d` |
| `webui/tests/vowifiMediaBehavior.mjs` | `1d38ddd079de6c7feddd283310d3713072f4abcf34871af7e68dd49ade7d6f62` |
| `control/app/modem_registry.py` | `181c20ef470a45c63e413e07c23b9d1f47af5f65ebdbe1e0a1261217a6e1b9fc` |
| `control/app/main.py` | `281aee9346a9e8f97e11adb068d63402035457beab0e36a6bd7a5dda45d849f8` |
| `agent/modem_agent.py` | `947583dedf2cba61fea256548a7f40c6b81a21315d8b648da036c72a100d3d7d` |
| `agent/package_manifest.py` | `9485399cd500062ac475dde53fcf5a6257c48edde632da0cb08b87e1d7f67314` |
| `agent/macos/Build-MacOS-Package.sh` | `33dd0b5b442b351438321dbc5a2cd8d1fca2ddbafbd7d6f2f174fc5459b4c433` |
| `agent/macos/Build-MacOS-Release.sh` | `5f1272fb97f2f843757653b7799cbbcc2aa3b271de2da9f930948eb36f03954a` |
| `install.sh` | `1c980a13d34f5430951131de3c16d40412243ab35c2d27d74b89f8bee8f1a04a` |
| `docker-compose.yml` | `6908da3ef5b0c568141d9a18d0711fb31537a57bceb7e4ba34d439e19433669b` |
| `agent/MODEM_AGENT.md` | `4c0a14ead5e227eddb02ebd6232bb3ec7dab3e991a86cdd7b1288e8b70e14e5a` |
| `tests/test_modem_registry.py` | `fa9d8ee469a4d2fab586eb1a978d98222001301f3cf7486ac3fc94c320cd4ff1` |
| `tests/test_remote_modem_devices.py` | `863631442e6fe8633829bcf10ab12d1a013ee23de25a820391d3f0bd9714565c` |
| `tests/test_agent_health.py` | `9fb071feedbffb6c47118f98230f628b24bcc439932bb37d61a15be27291ff9f` |
| `tests/test_modem_agent.py` | `b5685f59bb8358c4abf1f24ea6b167c5605d29f7015763a7dd2fc36582df456c` |
| `tests/test_call_audio.py` | `6952a537288a6fbba734d900da4765cd07308a640df07f31ce9e61424c1330d9` |
| `tests/test_agent_management.py` | `9bfae53e628d9ab2079d4e22a6952fb0cb2fa7f8a302b49ef7c4cc40e55ceefb` |
| `webui/dist/index.html` | `adbbfc867b5b94564c553f4829f1d4aa25386670f8c13e6015ba59a530287e7f` |
| `webui/dist/assets/index-BSZOCWXK.js` | `331ed4217ed9477a870fa0b13ad20cb2bf04cf52ec02a6dc7179728f072bdc0b` |
| `webui/dist/assets/index-Bsxla17J.css` | `2012084ff5922d856ce344a91641d7dc8991d790b7fcddda7be7ee0b361a2441` |

### PROD-WRAPPER IMP1 实施与验证记录（2026-08-23 09:53，含 A5/B5 FIX1、B6 FIX2、B7 FIX3 与 B8 FIX4）

`R1b-DEPLOY-2-PRE8-PROD-WRAPPER-IMP1` 只实现 A4/B4 冻结的最小增量：

- `engine/admission_gate.py`：每次 socket admission 与 status 发布同步检查
  `engine-maintenance.json`、`pcscf-rebind.json`、`admission-deny`；命中时执行
  `state.deny(local_fence_*)` 清除缓存 authority；status 暴露当前
  `authority_identity_digest`、`engine_generation_digest`、`normal_commit_id`、
  `normal_state_digest`、`updated_at_ns`。socket admission 也同步读取 authority 文件，
  missing/invalid/poison 立即 DENY，不等 watcher。
- 新增 `host/mdd_admission_authority.py`：host 侧 normal authority writer，以 Docker
  running Engine facts + `engine-run-id` 二次 inspect 稳定观察为 authority 来源；遇到 global/line
  fence、legacy ABI、unknown/write/probe 失败时发布 `admission-deny` 并移除旧 authority；恢复 allow
  前必须先证明旧 DENY 已清空，再两步 warmup，并用 gate probe/status 校验当前 epoch/seq/identity/digest。
  若 `admission-deny` 本身写入失败，也会 best-effort remove/poison 旧 authority，并等待 gate DENY
  proof；未证明则写 `deny_write_failed_not_proven` unhealthy 状态。
- `host/mdd_orchestrator.py`：把 writer 接入 orchestrator 独立线程，随服务 stop 一起停止续租。
- `install.sh`：公开 `reload --engines` 在完整 production replacement wrapper 完成前 hard fail；
  所有 reload 模式先拒绝 running legacy Engine；reload 后等待本次 reload 后更新的 normal authority/gate
  健康证明，并校验 identity/state digest/epoch/seq，否则 fail-closed。
- `host/mdd_update.py`：Web updater 在 `apply_tree()` 前对解包后的 `source_root` 做 ABI/legacy/健康门禁，
  使用与 install 近似的当前健康证明；Engine 枚举按 name ownership 覆盖缺失 component label 的旧容器；
  拒绝半升级 checkout。

精确验证文件：`tests/test_engine_admission_gate.py`、新增 `tests/test_admission_authority.py`、
`tests/test_update_apply.py`、`tests/test_maintenance_supervisor.py`。

验证证据：

- 聚焦回归：`94 passed`。
- 宽受影响集：`222 passed, 1 warning, 12 subtests passed`。
- 全量回归：`1249 passed, 1 warning, 68 subtests passed`。
- `python3 -m py_compile engine/admission_gate.py host/mdd_admission_authority.py host/mdd_orchestrator.py host/mdd_update.py` PASS。
- `bash -n install.sh` PASS。
- `git diff --check -- <本轮文件>` PASS。
- 未执行生产部署、未拨号、未发短信、未做真实 SIM/VoWiFi 计费路径操作。

实施后指纹（A6/B6 复审必须核对这些精确内容；变化即停止并重新登记 implementation）：

| 文件 | SHA-256 |
|---|---|
| `engine/admission_gate.py` | `58d7c35e2e458c1e937654e10ded746882e599c4fc2add33c4d879391463f210` |
| `host/mdd_admission_authority.py` | `cff06d2761c1112c73f2cc7ae291cdb400cb2a02d9497bd540f6da13f5e90be3` |
| `host/mdd_orchestrator.py` | `e9ed2ff650116109e6063b91e97f84a0c1ad25db8b42b2536654312405517404` |
| `host/mdd_update.py` | `134c38394587e62ab2b1343a4b071fdc997e7b7aaeaa931579200107214f4d59` |
| `install.sh` | `386381e96ea4302fc529de73705ac31e8e9028917f36774a1da511e2c6ae14b9` |
| `tests/test_engine_admission_gate.py` | `5a4f20e92e91cd68d5755994df62be26515b1836866d166fe3459dc9ef45015e` |
| `tests/test_admission_authority.py` | `d6cd100ad646d2e241e112d969d4681a3f136edf8096208f2b02b2e82c37d572` |
| `tests/test_update_apply.py` | `b46c760e0d7f543a86e184ec9d50ea7c4fe7615f0cfbad7b2b5a1c475e436932` |
| `tests/test_maintenance_supervisor.py` | `9cfba705a7db89803ea8758ace933a7368041372ba050088db6e309184faa799` |

### IMP2 实施与验证记录（2026-08-23 06:48）

`R1b-DEPLOY-2-PRE8-R6-C-ENGINE-IMP2` 只实现 A4/B4 冻结六项：authority canonical
schema、严格 3GPP RPDU 分类/完成消息旁路、MT 单一提交点与 MO 成功副作用排序、独立 outer/runtime
PID1 与单 PGID/绝对 deadline、Control 镜像 ABI canonical ID 门禁、对应发布路径和测试。未改 authority
writer、maintenance wrapper、任何通话终止路径或真实付费策略；未部署、未拨号、未发短信。

精确产品文件：

- `engine/admission_gate.py`
- `engine/entrypoint.sh`、新增 `engine/engine-runtime.sh`
- `engine/Dockerfile`、`engine/Dockerfile.overlay`、`install.sh`
- `control/app/engine.py`
- `engine/patches/asterisk/mdd_admission.py`
- `engine/templates/extensions.conf.j2`

精确验证文件：`tests/test_engine_admission_gate.py`、`tests/test_asterisk_admission_patch.py`、
`tests/test_engine_paths.py`、`tests/test_engine_maintenance.py`、`tests/test_pcscf_rebind.py`、
`tests/e2e_asterisk_admission_linux.py`。

验证证据：

- 聚焦回归：`131 passed, 1 warning, 12 subtests passed`。
- 全量回归：`1233 passed, 1 warning, 68 subtests passed`。
- `py_compile`、`bash -n`、`git diff --check` PASS。
- fixed Asterisk `d231cb2… + f1b60…`、fixed pjproject、全部生产 patch 在私有 runner A 实际
  `[CC] res_mdd_admission.c`、`[CC] res_pjsip_messaging.c`、两个 `.so` `[LD]` PASS。
- 一次性无 SIM/无资费 E2E：cold new MESSAGE 503；warm new MESSAGE 202，随后删除 authority 并跨
  3 秒 TTL 仍恰好 queue 1；RP-ACK/RP-ERROR 均 200、证据共 2、queue 副作用 0；截断/尾随/未知/
  249-byte RPDU 均 400；expired AMI packet 0；renewed AMI packet 1。
- 私有原始证据仅在 `/Users/fanli/.codex/private/mdd-r6c-imp2-20260823T0648/`：`build.log`、
  `e2e-run.log`、`e2e-result.json`、`asterisk.log`、`gate.log`。runner C 预检仅约 2.8 GiB root / 5.5 GiB
  data 可用，按资源约束回退 runner A。runner A 旧内核解包可选 GSM/Opus 包返回 ENOSYS；按既有
  runner 限制使用已成功链接的产物执行 `bininstall + ldconfig`，没有把环境错误计作代码失败。

实施后指纹（A5/B5 复审必须核对这些精确内容；变化即停止并重新登记 implementation）：

| 文件 | SHA-256 |
|---|---|
| `engine/admission_gate.py` | `7ef283b0fe04cff5d533840f07fb556a24bfc78de2d3e5fcb325cb90d9bd53fc` |
| `engine/entrypoint.sh` | `338912591690263eb24d83728e88012bc78db009ec77aa0df1cd7e61f13f53f9` |
| `engine/engine-runtime.sh` | `fcbed3157aacfc58281a7bd19401644977c9ae61b8e144416e475a1767c34c16` |
| `engine/Dockerfile` | `4a432cffcb534c94267f06d87aaebfefd510c6eb5ddf0a81e3ef4b6c9fb94bb3` |
| `engine/Dockerfile.overlay` | `95cf0e3c0474c262ef37294ffaefe501b667038d227bbfe131c5a318bfa88d51` |
| `install.sh` | `1984997fe22c7608be2bfaf35332cb00ab586c70dd55d96033c0c878974a8325` |
| `control/app/engine.py` | `0ea176afbcffc86c0f1d8c6c8edb411d2258273855e20e2e44763f60f0ff9cb6` |
| `engine/patches/asterisk/mdd_admission.py` | `6ff79560dc6078c9e13ad321bbe3efabe0c17efc47f0a143e894eb28e020ec16` |
| `engine/templates/extensions.conf.j2` | `b22203725ff8a4346828e3be3ea6b6921e023523085556ef6ea863b9820781fb` |
| `tests/test_engine_admission_gate.py` | `15177dea868345132b59994e4508c7bf01fb6faad12d2bb98f88d0739805a2bf` |
| `tests/test_asterisk_admission_patch.py` | `6c051eddcfc5a6fbf90ab147c7c61d35efd6d43530d6b59aa422382abaa7a005` |
| `tests/test_engine_paths.py` | `063e51f4cf453c79da47060cf6c61ea8efef25dbde66537c9a990af68f20515e` |
| `tests/test_engine_maintenance.py` | `60a7fdaa2f9c934007a8eec4b7209e2dcd4aabf729118e6f2838fa82d6e72fd9` |
| `tests/test_pcscf_rebind.py` | `fa66896cef04ac86378ef3b1184585f0ec4f4f2d238422962cd9bf8aea811f79` |
| `tests/e2e_asterisk_admission_linux.py` | `289afd5f5bd795108acb62caf00b4f7752cbc5fd29a0c72743da4a5e3d22ec3d` |

### R6-C endpoint@domain 复现记录（2026-08-23 08:23）

`R6-C-ENDPOINT-DOMAIN-REOPEN-1` 只复现 B5 P1，不修改产品代码。外置诊断副本在
`/Volumes/micron512g/tmp-project/codex-audit-tmp/mdd-r6c-endpoint-domain-20260823T080732+0800/`；
私有 runner A 原始日志在
`/Users/fanli/.codex/private/mdd-r6c-endpoint-domain-20260823T080732+0800/probe-run9.log`。
探针在 Docker `--network none` 容器内创建本地 `dummy0` 供 Asterisk 初始化 EID，所有 SIP 包仅发向
127.0.0.1 UDP sink；无 SIM、无资费、未部署、未拨号、未发短信。

复现结论：过期 authority 下，现有 AMI raw destination gate 能阻断
`pjsip:volte_ims/123@127.0.0.1:5099`，但不能阻断
`pjsip:volte_ims@example.invalid`；后者仍发出 SIP MESSAGE packet。该行为与 pinned Asterisk
先解析 endpoint、再丢弃 domain 的语义一致，因此 IMP2 不可冻结。IMP3 最小修复候选必须由双预评审确认：
在 serializer 内 `ast_sip_get_endpoint()` 成功后读取 endpoint object id，对 exact `volte_ims` 执行
`sms_out` final gate；移除 raw destination gate，避免双重语义和 URI 字符串猜测；RP-ACK/RP-ERROR
完成路径不得被当成新的 outbound work。

### R6-C endpoint@domain IMP3 实施与验证记录（2026-08-23 08:34）

`R1b-DEPLOY-2-PRE8-R6-C-ENDPOINT-DOMAIN-IMP3` 只做 A6/B6 预审通过的最小修复：
`sms_out` final gate 从 `sip_msg_send()` raw destination 字符串判断移到 outbound serializer
`msg_send()` 中 `ast_sip_get_endpoint()` 成功后；仅当
`ast_sorcery_object_get_id(endpoint) == "volte_ims"` 时 gate。删除 raw `carrier` parser，避免
`endpoint@domain` 绕过和双重语义；非 IMS endpoint 不 gate；endpoint cleanup 沿用 pinned source 的
RAII；RP-ACK/RP-ERROR completion 仍不作为新 outbound work。

精确文件：

- `engine/patches/asterisk/mdd_admission.py`
- `tests/test_asterisk_admission_patch.py`
- `tests/e2e_asterisk_admission_linux.py`

验证证据：

- 本地 patcher 单测：`4 passed`。
- 本地 R6-C 聚焦：`108 passed, 1 warning`（`tests/test_asterisk_admission_patch.py`、
  `tests/test_engine_admission_gate.py`、`tests/test_engine_maintenance.py`、`tests/test_engine_paths.py`）。
- `py_compile` 与 `git diff --check` PASS。
- 私有 runner A IMP3 副本从 git HEAD 还原 `res_pjsip_messaging.c` 后应用当前 patcher；两个相关 `.so`
  直接重编/链接 PASS；生成后源码仅保留 endpoint-id gate，未发现 `carrier` raw parser。
- 正式无 SIM/无资费 Asterisk E2E PASS：`expired_domain_ami_packets=0`、
  `warm_domain_ami_packets=1`、`non_ims_ami_packets=1`，同时保留 `expired_ami_packets=0`、
  `warm_ami_packets=1`、completion/invalid RPDU/queue-after-TTL 既有断言。B7 修正后，成功
  AMI action 的 loopback sink 会回最小 SIP `200 OK` 停止重传，并按 exact body 与 1 秒
  no-extra-packet 检查证明每个 action 恰好一个 MESSAGE packet。
- 私有原始证据仅在
  `/Users/fanli/.codex/private/mdd-r6c-endpoint-domain-20260823T080732+0800/`：
  `imp3-direct-build2.log`、`imp3-e2e-run5.log`。IMP3 正式 E2E 源码以
  `tests/e2e_asterisk_admission_linux.py` 的下方指纹为准；外置目录
  `/Volumes/micron512g/tmp-project/codex-audit-tmp/mdd-r6c-endpoint-domain-20260823T080732+0800/`
  仅保存 runner 脚本和早期 REOPEN 诊断副本，不能单独作为 IMP3 E2E 源码证据。未部署、未拨号、
  未发短信。

实施后指纹：

| 文件 | SHA-256 |
|---|---|
| `engine/patches/asterisk/mdd_admission.py` | `729b86caaa522bf05e2f2423163cd0c66f11bdb5542eafb1106d9f593e32284c` |
| `tests/test_asterisk_admission_patch.py` | `a668e10c123c6d1a9d6eff2ef55a0ca93248ce5597fb7d699b6a625db0a8c5ea` |
| `tests/e2e_asterisk_admission_linux.py` | `acc379d6db1a550ed6c2470b0bccd562f3bb602ec7867174ac2f6864f1eacda0` |

### R6-C A1/B1 复审发现（2026-08-23 05:53）

- `A1-F1 / P2`：authority canonical schema 未闭合；当前可接受 bool `version`、任意非空
  `engine.started_at`、裸 64-hex image 和 1 字符 txid；非字符串 `mode=[]` 还会抛未捕获
  `TypeError`杀死 watcher。现有 run-id 正则不是规范 UUID，entrypoint 的 `date +%s-$$` fallback
  又不符合自身正则。整改边界：与 Control/proxy 的唯一 canonical schema 一致；使用 plain-int
  version、先验类型再查枚举、规范 StartedAt、精确 `sha256:<64hex>`、至少 8 字符 txid、
  规范小写 UUID；所有坏输入只转成 `AuthorityError` 并 fail-closed，不能杀 watcher。
- `A1-F2 / P2`：并发协议测试使用 `write_text` 原地截断，watcher 可合法读到中间坏 JSON，
  首次聚焦测试已复现 `1 failed, 18 passed`，随后单测 5 次通过。整改边界：fixture 使用
  与生产一致的 atomic replace，或明确推进 seq3/4；不得降低生产 fail-closed。
- `A1-F3 / P2`：PID1 在 `service.start()` 和 `Popen()` 后才安装 TERM/INT handler，存在 Asterisk 已
  spawn 但未接管信号的窗口；独立预算 8s+2s+2s 又可超过常见 Docker 10s。整改边界：
  spawn 前建立 stop intent/handler（或 block signal），所有停止阶段共用一个绝对 deadline。
- `A1-F4 / P2`：`control/app/engine.py` 的 normal/dev/start_absent 路径可把新 entrypoint、template 和
  gate 挂载到无 C pre-202/pre-send hook 的旧 Engine，`_start_container()` 在删除旧容器/清理 runtime 前
  没有核验目标镜像 ABI。整改边界：三条启动路径共用唯一镜像 inspect，必须精确等于
  `io.mdd-sim-gateway.admission-abi=mdd-admission-v1`；在任何旧代删除、runtime 清理或 container create 前
  fail-closed，禁止给旧镜像补标签伪装升级。
- `B1-F1 / P1`：MT MESSAGE 在 C hook 回复 200 并发 RP-ACK 后，`[volte_ims_msg]` 又执行
  marker+positive lease gate；入队到 dialplan 间 lease 过期/出现 marker 会丢掉已被运营商确认的短信。
  整改边界：接收提交点必须唯一且在首个 200/202/RP-ACK 前，不在后续消费路径二次拒绝。
- `B1-F2 / P2，A1 交叉复审升为 P1`：pre-202 hook 在解析内容前一律执行 `sms_in` gate，连 pinned source 可明确识别的
  RP-ACK `0x03` / RP-ERROR `0x05` 完成消息也可在 lease 过期时被 503。整改边界：仅对新业务
  SMS 做正授权，不拦截完成/终止消息。
- `B1-F3 / P2`：MO `[msg-from-local]` 首次 gate 后先写 OUT log/notify，再到 C final gate；慢 I/O 跨
  TTL 时可记录成功但实际未提交。整改边界：提交前唯一 final gate 必须在任何成功记录/
  notify 之前，且不能破坏无自动重放与原有失败语义。

A1 还记录一个非本段缺陷：当前产品尚无 admission-authority writer；它已属于任务板后续
supervisor/wrapper 段，本轮只保持部署门禁关闭，不重复算作 IMP1 缺陷。

#### IMP2 必须通过的最小测试门禁

1. MT 在 pre-202 获准后人为阻塞队列跨过 TTL，再撤权：仍恰好投递一次；首次拒绝时
   则零 2xx/RP-ACK/queue/log/notify。
2. 过期授权下的 RP-ACK/RP-ERROR 能完成且零新短信副作用；短包、未知 TPDU 不能伪装
   completion 绕过 gate。
3. MO 首次快速检查后阻塞跨 TTL：final MessageSend 拒绝，零载波提交、零成功 log/notify；
   正常成功路径各恰好一次，AMI 直达仍在 serializer 前 fail-closed。
4. 参数化拒绝四类非 canonical authority；正常 renew 使用 atomic replace，另以受控
   `truncate → 半写`证明立即 DENY、同 seq 不恢复、两个新 seq 才恢复。
5. TERM 分别落在 gate start 前、Popen 期间、spawn 刚返回后，都不遗留子进程；Asterisk、
   gate thread、helper 均延迟/忽略 TERM 时，全部 TERM/KILL/join/reap 不超过同一绝对预算。
6. normal、dev、start_absent 对缺 ABI/错 ABI 镜像全部拒绝，且断言旧容器未删、runtime 未清、
   路由和实例配置均未改；exact ABI 才可进入原 create 流程。

#### `R6-C-ENGINE-IMP2-PRE` 待双链确认的精确变更清单

1. `admission_gate.py`：将 authority 规范收紧为 plain-int version、先验字符串 mode、与
   Control 相同的 RFC3339Nano StartedAt、exact `sha256:<64hex>`、Control 同款最少 8 字符 txid、
   canonical lowercase UUID run-id。`entrypoint.sh` 只由 Python `uuid4()` 生成 run-id，取消不合规的
   `date +%s-$$` fallback。不扩大 watcher 异常捕获来隐藏编程错误；而是使每个外部字段在
   运算前完成类型验证，坏输入统一成为 `AuthorityError`/DENY。
2. `mdd_admission.py` 对 pinned `res_pjsip_messaging.c` 注入一个共享、无副作用、最多 248-byte
   的 RPDU 分类器（格式按 3GPP TS 24.011 表 7.4/7.7/7.8）：非 3GPP SMS 为新工作；
   network-to-MS RP-DATA `0x01` 必须具有 message-ref、2..11-byte originator LV、0-byte destination LV、
   1..232-byte TPDU LV，所有长度 exact 消耗 body；RP-ACK `0x03` 必须至少 type+ref，若有
   RP-User Data 则必须是 exact `0x41,length,value` TLV；RP-ERROR `0x05` 必须有 1..2-byte
   mandatory RP-Cause LV，若有 RP-User Data 同样 exact TLV。截断、长度不符、未知 MTI、>248 bytes
   都是 invalid。out-of-dialog 与 in-dialog 在原各自 content-type 检查后共用分类：
   completion 不调 gate，但必须继续恰好一次走现有 `parse_rpdata: SMS RP-DATA '<hex>'` 证据路径，
   且 hex 仅按实际长度生成（修复现 `hex_body` 固定读 256 bytes 的越长/未初始化日志）；
   随后直接回 200，零新短信 queue/用户历史/notify。invalid 回 400 且零副作用；只有
   新工作在任何 2xx/queue/RP-ACK 前执行 `sms_in` gate。Control 正则和投递判定逻辑不改。
3. `extensions.conf.j2`：从 `[volte_ims_msg]` 删除两个 marker 检查和第二次 `sms_in` gate，
   因 C pre-ACK 是唯一 MT 准入线性化点。`[msg-from-local]` 保留早期 marker/gate 作快速拒绝，
   但先执行受 C final gate 保护的 `MessageSend`；只有 exact `MESSAGE_SEND_STATUS=SUCCESS`后才写
   OUT 日志和 notify，其他状态零成功副作用并走现有失败挂断语义。
4. `entrypoint.sh` 永远只做 outer：生成一次 canonical UUID，立即
   `exec admission_gate.py supervise -- /bin/bash /engine-runtime.sh`。现有最长约 150 秒的 PIN/SWu/P-CSCF
   初始化全部移入新的、非 Docker ENTRYPOINT 的 `engine-runtime.sh`，末尾直接 `exec asterisk -f`。
   不使用任何 child-marker 或环境分支，因而初始容器 env 无法绕过 gate；`engine-runtime.sh`
   只使用已由 GateState 验证且由 outer 导出的同一 run-id，不再生成。Python supervisor 从任何
   keeper/SWu/AMI 子进程出现前就是 PID1，初始化 shell、其后台子树与 Asterisk 始终属于
   supervisor 创建的同一新 process group；不再另写一套分阶段 shell PID trap。full/overlay/
   dev mount 和 runtime fingerprint 必须原子包含两个脚本。
   `admission_gate.py` 在 `service.start()`/`Popen()` 前安装 TERM/INT handler 并保留 pending signal；
   若信号落在 start/spawn 窗口，完成当前有界操作后立即转受控退出。首次停止
   intent 生成唯一 monotonic absolute deadline；gate 先执行非阻塞 `request_stop()` 停接受，随即与
   Asterisk 和既有 helper 并行收 TERM，禁止先 join gate 再终止子进程的串行预算；
   在同一 deadline 内保留小段 KILL/reap 预算；所有 wait/join/helper cleanup 只消费 remaining，
   禁止任何最后的无界 `process.wait()`。即使初始化 shell/Asterisk 主进程自然退出，PID1 也会
   对其原 process group 做有界 TERM/KILL/reap，清理留存后台子进程；主进程的原 exit status 仍保留。
5. `control/app/engine.py`：新增唯一 `_require_engine_admission_abi(client, image)`，通过 Docker image
   inspect 要求 exact `io.mdd-sim-gateway.admission-abi=mdd-admission-v1`，并返回 inspect 得到的 canonical
   `sha256:<64hex>` image ID。`_start_container()` 在调用
   egress/config write、解析/删除旧容器、清 runtime 和 create 之前唯一次验证，因而 normal/dev/
   start_absent 自然共用；缺镜像、缺/错 label 一律 fail-closed。normal/dev/start_absent 的
   `containers.run` 一律使用这个 canonical ID，不继续使用可在 inspect/create 之间被 retag 的
   输入 tag；immutable digest 启动还要求 canonical ID 与请求 digest exact 相等。
6. 只修改上述文件及对应测试/E2E fixture；不改 authority writer、wrapper、BYE/CANCEL/
   re-INVITE/UPDATE/ACK/PRACK/h/hangup/release，不改付费重放策略，不部署、不实发。

### 当前实现基线指纹（仅用于检测意外回退/重复覆盖）

以下是进入 R6-C 实施前保留的 POST-R5 文件指纹；R6-C 合法实施会产生新指纹并在下一检查点替换，
不能因为指纹变化自动回退文件：

| 文件 | SHA-256 |
|---|---|
| `host/mdd_maintenance_proxy.py` | `60cbf6ac2ef43a124bdc01f7037da298c3726b2fbddcfce2f7efb4ef36fea243` |
| `host/mdd_maintenance_supervisor.py` | `e432713111a10922651462b4715fcaf5f637f26b446adac51d0d86666cee8112` |
| `host/mdd_orchestrator.py` | `2d968cb2a5a525bf3be629c79ee4477ea6fb1ab988a32704593aa5dfcf895286` |
| `control/app/engine.py` | `309b289d96f9fc6f9af902dbbb2119697691adf93f762016c9b460491adb602e` |
| `control/app/main.py` | `60ab7b0b7c4a2938c8e6c599f00bbe141a008a124f38890d4269ff2488aa6f05` |
| `install.sh` | `c2f718550280a1bb34295cb9d2334948faa061b5d6144875cfff23e1169ec335` |

### R6-C 首段实施记录（`R1b-DEPLOY-2-PRE8-R6-C-ENGINE-IMP1`）

- 新增 `engine/admission_gate.py`：严格 authority schema、Engine generation digest、首次序列 warmup、
  同 identity 严格递增、3 秒本地 monotonic TTL、missing/corrupt/replay/stall fail-closed、Unix peer
  credential 与 exact nonce/framing；PID1 有界监督 Asterisk 并清理既有 helper 子树。
- 新增 GPL-2.0-only Asterisk resource/patch：`res_mdd_admission` 提供唯一 Unix client 和
  `MDD_ADMISSION(kind)`；`res_pjsip_messaging` 在 out-of-dialog/in-dialog MESSAGE 的 200/202、queue、
  RP-ACK 前门禁，并在 AMI `MessageSend` 的 IMS endpoint submit 前门禁；RP-ACK 直发完成路径不拦。
- 五个 dialplan 入口在首个副作用前要求 exact `ALLOW`：carrier call、MT SMS、media canary、browser
  carrier call、MO SMS；`h`/BYE/CANCEL/re-INVITE/终止路径未接 gate。
- Engine runtime/entrypoint/dev mount/fingerprint 已纳入 daemon；full Dockerfile 才发布
  `io.mdd-sim-gateway.admission-abi=mdd-admission-v1`。无 base-fp/ABI predecessor 与不精确的
  `MDD_ENGINE_BASE_IMAGE` 不得 overlay 或盖上新能力标签。
- 新增 `tests/test_engine_admission_gate.py` 与 `tests/test_asterisk_admission_patch.py`。本地首段 19 PASS；
  相关聚焦 89 PASS；全量 `1220 passed, 1 warning, 68 subtests passed`；py_compile、
  `bash -n engine/entrypoint.sh install.sh`、`git diff --check` 通过。
- fixed Asterisk `d231cb2… + f1b60…`、fixed pjproject、全部生产 patch 的私有 runner A `make res`
  实际出现 `res_mdd_admission.c`/`res_pjsip_messaging.c` 的 `[CC]` 与两个 `.so` 的 `[LD]`，无新增
  compiler error/warning。原始证据：
  `/Users/fanli/.codex/private/mdd-r6c-module-compile-r5-20260823053837-77566.log`。
- runner 失败账本（不得压缩后重试同一错误）：Docker Hub metadata timeout；runner A/C 的旧 Docker
  seccomp 对 Fedora 44 tar 返回 ENOSYS；BuildKit 缺 buildx；手工根目标、缺 `ASTTOPDIR`/顶层 flags
  三种命令错误。权威首编译入口为 fixed source + all patches + 顶层 `make res`；最后的 wrapper rc=1
  仅来自错误的 `nm -D` optional-API 动态符号假设，编译/链接本身已 PASS，运行时装载仍须 E2E。
- 两条原评审链的 `R6-C-ENGINE-POST-A1/B1` 均为 `NEEDS CHANGES`，发现已单独编号；当前只做
  IMP2 最小整改交叉预审。首段未冻结；production deploy、真实通话和短信继续 DENY。

#### 实施后指纹（`R6-C-LOCAL-TEST-1` 时点）

下列指纹只用于检测后续是否发生新增改动；若 E2E 或复审要求修改，必须登记新的
`implementation_id`、受影响测试和更新后指纹，不能将变化误当成旧实现未做。

| 文件 | SHA-256 |
|---|---|
| `engine/admission_gate.py` | `ecf7f24fbca608c00ad99b4e7d80d4fe167cb35dacb104ab2b66a88de2ab93de` |
| `engine/patches/asterisk/mdd_admission.py` | `aba6859e6aa1a02f9a041e0e3a78a2fefd917598e8ed333bfd70ae5e6ed9bd19` |
| `engine/patches/asterisk/mdd_admission/mdd_admission.h` | `e795459b559a13d9f904007c3ca2c294d7aafb3c04040e102e1944a78dd4b408` |
| `engine/patches/asterisk/mdd_admission/res_mdd_admission.c` | `53678d73e354183ecb7e70178179fc60cf473ff076a3297d4f126be5c10717c2` |
| `engine/templates/extensions.conf.j2` | `88c08fcecb9e8fa5a229c6aca0d1695f68fba7b22e1c19e248c9e92009697cc5` |
| `engine/Dockerfile` | `0c692ec180b55c7634b9f97533a4d60a108befcbc7e6d1d77ae3e9178cb65b28` |
| `engine/Dockerfile.overlay` | `87e0ca9810339b7c6197a10824c73c9a175f3476cf3bbd84e3ed621ba69da428` |
| `engine/entrypoint.sh` | `57a9cfbdf3860c5dbe659bfffa402ea89e9676e086c66f6b3fe49c59e5d73a1c` |
| `install.sh` | `28fe53cd602b52942baa3b0321d7240d24334b7baf3bb3fd32988131177de647` |
| `control/app/engine.py` | `167dcddbeaed35d1de037dd07576277d7b792022221577ad3ee1890f341acf83` |
| `tests/test_engine_admission_gate.py` | `5f58d0a3bb7db1f547073572a9a188eb7a56cd2aafc03254982b535f177be3a5` |
| `tests/test_asterisk_admission_patch.py` | `198d0b0da22a0cbf6510b5231a411ce36baee12ae74da1067c1075a7e5f5cf5d` |
| `tests/e2e_asterisk_admission_linux.py` | `4aa2bc0aa45c232d54c48ecec4919443a8b6cbcdbd7c50ca0f655cf2420297b1` |

## 压缩恢复协议（强制）

1. 本文件是唯一任务状态源；会话摘要只用于定位资料，不得据摘要重开、重做或宣称完成。
2. 每次会话压缩、进程恢复或执行者切换后，必须依次核对：本文件的“当前执行游标”、工作树差异、
   测试证据、两条既有评审会话的最新结论；四者不一致时停在原门禁并补证据，不能猜。
3. 状态为 `已复审/已部署/已实机验收` 的工作默认冻结。只有出现带时间、环境和复现证据的新缺陷，
   才能在任务板登记 `REOPENED`；不得因为看见旧代码或旧对话再次“顺手修复”。
4. 每轮评审均单独编号，记录发现、处置和复审结果。`NEEDS CHANGES` 只打开所列缺陷，不把本轮
   已通过的边界退回“待研究”；修复后由原两条评审链复核增量和受影响边界。
5. 任何状态推进都必须先写入对应证据；没有精确测试结果、评审结论或部署 generation 时，保持
   原状态，禁止用“应该完成”“看起来正常”代替。
6. 恢复工作时先只读执行“当前执行游标”所列门禁；不得先全库搜索旧问题或根据对话摘要生成新
   修复清单。若游标、任务状态和逐轮账本矛盾，先修正本任务板，禁止修改产品代码。

## 当前执行游标（压缩后从这里继续）

- 当前唯一开放项：`R6-C-ENGINE-IMP2-PRE`。A1/B1 均为 `NEEDS CHANGES`，先由原两链交叉
  核对上述六项发现的最小整改边界；双链确认前不修改产品代码。R6-C IMP1 保留为已实施基线，
  禁止整段重写；Asterisk E2E 暂停，禁止验证已知有缺陷的版本。
- 已保留的实现：P-CSCF 按 Asterisk 进程代际不可变、持久 marker、完整 entrypoint 重启、
  exact-generation 校验、已有通话与 BYE/CANCEL 不拦截、所有付费动作不重放。
- 冻结测试基线：DEPLOY-1 聚焦 R1b 为 `230 passed, 21 subtests passed`；全量为
  `1062 passed, 1 warning, 66 subtests passed`；py_compile、shell syntax、`git diff --check` 通过。
  DEPLOY-2 当前最终全量基线为 `1147 passed, 1 warning, 68 subtests passed`；Web 六组脚本、
  production build、py_compile 和 `git diff --check` 通过。
- R1b 轮 3 所有发现均已关闭：SIP MESSAGE 双门禁、同步 `LOCK_NB`、WSS hard deadline、
  stop/abort 有界退避、显式 REGISTER 的同步 worker 锁所有权、invalid-applied 写入顺序均已由
  R4-A/R4-B 最终复审明确 `PASS`。这些范围已冻结，不得再次按“待复审”处理。
- 当前部署门禁仍关闭。DEPLOY-1 的 stdout 契约缺陷已经修复、双复审及实际镜像验证完成；DEPLOY-2
  的付费/竞态链 PRE5 与配置/生命周期链 PRE7 均已 `PASS` 并冻结。整条 SMS 持久防重放、release
  coordinator 最终所有权及 proxy durable revoke/forward 原语已经 02:46 双最终复审 `PASS`，现一并
  冻结；无带时间复现不得重开。PRE8 POST-R3 双链分别复现一个 P1/P2，现已作 POST-R4 最小整改：
  recover/commit 的 manifest/mode 锁全部有界；部分线路 fence 已释放后允许新活跃通话存在，只完成
  同代剩余 fence 清理，真实代际/健康变化仍撤权。全量为 1198 PASS，私有 Linux host-mode E2E
  再次 PASS；当前只等待原两条链 POST-R4 最终复审。生产 bootstrap wrapper 仍缺失，无论本轮是否
  双 PASS 都必须保持部署门禁关闭。POST-R4 付费链已 `PASS`；配置链又复现 release-pending commit
  未与 lease/stop generation 线性化的 P2；POST-R5 方案经双链预审补齐 generation token、最终 exact
  proof、stop intent 与共享 deadline 后已经实现，全量 1201 PASS、私有 Linux E2E PASS。POST-R5
  配置链已 PASS，但付费链复现持 lease 执行无界 directory fsync、marker 先消失的 P1；现停在 R6-PRE
  双链已否决 marker-only A/B 并完成严格 C 预审：必须由新版 Engine 的本地单调正授权 gate 覆盖
  slow unlink/lease expiry，且 production wrapper 先升级并证明 gate-capable Engine 才能清旧 marker。
  当前 R6-C Engine 首段已实施并本地测试 PASS，正等待一次性 Asterisk 行为 E2E 与
  两条原评审链 A1/B1 增量复审。在三项证据齐全前不得冻结 PRE8、不得部署。

## 当前目标与不变量

- Free/giffgaff 的 VoWiFi 必须反映完整链路真实状态，能呼入、呼出、双向媒体并可靠挂断；
  `Registered` 不能单独显示为“通话正常”。
- 浏览器在任何已登录页面收到来电时都应有可操作的接听/拒绝界面；不能只有 toast/banner。
- macOS 统一 Agent 必须复用 Windows 的领域能力和服务端通话链，CLI/GUI 等价；4054 蜂窝语音
  只有当前发布包、信令、UAC/TCC、voice registration 全部通过才可显示可用。
- 新宿主的 MTU、媒体入口、多网卡/VPN 和国家出口必须自动探测或引导确认，禁止固定 IP、默认路由猜测。
- 付费动作不自动重放；活跃/未知通话绝不因健康、部署、号码学习或出口恢复被销毁。
- 所有实现先预审，实施后由两条独立评审链复审；真实收费验证必须用户授权且次数有界。

## 权威环境基线

- 当前服务端：`10.44.0.23`；镜像/下载代理仅使用已约定的宿主代理配置。
- UK 线路：`gb → mdd-gb → london`。
- France 线路：`fr → mdd-fr → fr via lxb`，不再走 GB。
- 2026-08-22 20:48 只读核验：Control、Engine 1、Engine 7 均自 20:31 持续运行，容器 ID
  未变化且 `RestartCount=0`；最近没有新的自动 recovery/freeze。France 为 `Registered`；UK 为
  `reg_temporary/503`，由同一 Asterisk 原位等待 Retry-After，未拨号。
- 2026-08-22 21:43 再核验：Control 没有新的 health-freeze/capture/auto-recover，原“两分钟
  destructive recovery”已停止；但 Engine 1 在 21:32 出现一次同容器 `exit 139` 自动重启
  (`RestartCount=1`)。前序证据为 SWu 进程退出、P-CSCF 改变并执行 res_pjsip reload；这不是
  Control 两分钟重建，需作为独立 R1 稳定性问题完成根因评审，不能误报为全部重启已消失。
- 当前 Git 基线 HEAD：`49ac6e5`。工作树包含大量尚未形成里程碑提交的既有实现；不得清理、覆盖或
  用旧提交替换这些变更。

## 任务状态

| ID | 事项 | 状态 | 当前权威结论 / 下一门禁 |
|---|---|---|---|
| R1 | 两分钟 Engine 自动重建 / IMS 注册风暴 | R1b-DEPLOY-2 PRE8 R6-C IMP1 已测试，A1/B1 `NEEDS CHANGES` | strict C Engine gate IMP1 保留；只开放 A1-F1..F3/B1-F1..F3 的 IMP2 最小整改预审。E2E 等整改后再做；现线仍为旧 digest，部署门禁关闭。|
| R2 | MSISDN 学习触发额外 REGISTER | 双复审 PASS 待部署 | Control 后台 IMS 号码学习已整体删除；Control 只剩用户显式 register API 可主动发送 REGISTER；Engine 的初始/自然恢复注册保持。|
| R3 | 注册证据与 IMS 号码的结构化耐久化 | 部分实施 | 日志派生的 No-response/Temporal/Fatal 已去敏并绑定 container ID+StartedAt incarnation+ICCID；Engine 原生结构化注册/PAI 事件仍待未来实现。|
| R4 | Free/giffgaff 真实呼入/呼出与网络链 | 已预审 | 20:55 两线 IMS 均已 Registered、出口正确；两条 WebRTC endpoint 均 Unavailable，当前阻塞是浏览器 listener/media ingress/Echo，不是 IMS 出口。|
| R5 | 全局可操作来电 UI | 已预审 | 根因：VoWiFi UA 仍只在 `Softphone` 页面 mount；需 App 级单一 `CallCoordinator`，页面仅订阅，不能创建第二套 UA。待第二评审返回后实施。|
| R6 | 多网卡/VPN 媒体入口 | 已部署待实机确认 | 已改为按认证浏览器会话从宿主 inventory 选择/确认并做无资费媒体证明；无固定地址/default-route。两轮复审 PASS。用户仍需在实际浏览器确认并跑本地 Echo。|
| R7 | macOS Agent / 4054 蜂窝语音 | 待部署核验 | `.162` 实机确认仍运行 12:19 旧包：audio helper protocol v1、旧 CFUN/NO CARRIER 行为；当前源码要求 helper v2。需原子部署同一源码的 GUI+两个 helper。|
| R8 | 新机 MTU 门禁 | 已测试待发布闭环 | 国家 TUN authoritative MTU=1280，Engine 缺失/非法值启动前 fail-closed；direct 路径有 PMTU/EMSGSIZE 自愈。仍需纳入正式安装/发布验证。|
| R9 | 国家出口进程隔离 | 待设计 | 当前所有国家共用 sing-box 进程；FR 配置变化会短暂影响 GB。后续按 country 拆进程/generation，不能混入当前 UK 最小修复。|
| R10 | Windows/macOS/Linux 统一 Agent 拓扑 | 部分完成 | Windows/macOS health 与多 Modem/PCSC 已实现；Linux provider 仍未实现。Agent health 只作父级可达性/展示/veto，不能替代 modem/call/paid lease 真值。|

## 不得重复实现的已闭环事项

1. **`reg_temporary` 分类与低频等待**：严格解析同一 Asterisk Temporal 行，保持 `REGISTERING`
   而非伪绿；不 capture/stop/failover。两条独立复审 PASS，已部署 `.23`，实机同容器验证通过。
2. **多入口媒体不使用固定 IP**：宿主 inventory + per-session binding + SDP/ICE rewrite + Echo
   admission 已实现；不得退回 `10.44.0.23`、默认网卡或全局 advertise address。
3. **付费蜂窝通话 lease/watchdog**：health 不能清 lease、确认 ended 或延长 deadline；不得用新的
   Agent health/topology 重写该安全状态机。
4. **Windows Agent 服务/CLI/GUI 基础**：统一 runtime/配置和安装门禁已实现并经过既有复审；当前任务
   只补尚未验收的来电/部署表现，不另写一套 Windows Agent。

## 本轮变更日志

### R1/R2 — 2026-08-22 实施中

- 预审 A（付费/生命周期）：PASS WITH REQUIRED BOUNDARIES。
  - generic `registering` 只能观察，不累计 destructive budget。
  - `reg_unanswered + active_channels=0` 保留一次有界、代际安全恢复；active/unknown 不动。
  - Control 启动、状态轮询和号码学习不得主动 REGISTER。
- 预审 B（配置/macOS）：确认旧 `.14` 已存在 `401 → 200 OK → 4 秒后第二次 REGISTER → 503`；
  `_msisdn_checked` 仅内存且 `_verify_ims_msisdn` 在 Control 重启后主动发 REGISTER，与时序吻合。
- 第一版“从 Docker 日志被动学习 P-Associated-URI”在实现后复审中被否决并已撤回：
  - 生产配置不保证把该响应头输出到 stdout，单测 mock 不能证明现场可用；
  - 日志证据未绑定当前 ICCID、配置修订和 Engine generation，存在 SIM swap/手工编辑竞态；
  - `msisdn_pending_apply` 与运行中 AMI/Engine 身份会形成不一致状态。
- 当前本地实现：
  - generic `registering` 和明确 `reg_rejected` 清旧失败预算、保持同一 Engine，不进入 capture/failover；
  - `reg_unanswered + zero channels` 与明确 tunnel/local failure 的原有代际安全恢复保持；
  - 后台 `_verify_ims_msisdn`/`learn_msisdn`/日志解析全部删除，不再由状态轮询学习号码；
  - 生产 Control 中仅显式 `POST /api/instances/{iid}/register` 可发送 `pjsip send register`；
    Engine/SWu 在首次隧道就绪或既有受控隧道恢复时仍拥有正常注册，不把它误删为 Control 副作用；
  - explicit fatal REGISTER 的 Asterisk 原位重试从 30 秒放慢到 3600 秒；普通无应答仍为 30 秒；
  - 页面区分原位注册/低频重试与真正的 tunnel 自动重建，不再把普通 REGISTERING 写成将重建。
- 第一轮实现后复审：两条均 `NEEDS CHANGES`（无 P1）。整改已完成：
  - 不再把所有 `registering` 混为同一状态；启动宽限期后，缺 SIM bootstrap、本地 Asterisk
    状态不可读、注册器长期 Unregistered 分成明确 local reason，仍走 exact-generation +
    authoritative-zero-call 的有界恢复；正常注册推进保持原位观察。
  - 当前固定 Asterisk 的真实 fatal 文案同时支持 `Fatal response '403'` 与
    `'403' fatal response received ... retrying in '3600' seconds`，按最新事件判定。
  - No-response/Temporal/Fatal 去敏证据原子保存到当前 line run 目录，绑定 container generation
    与 ICCID SHA-256；日志滚出后同一代仍可判定，新代启动自动清除，Registered 写入同代成功
    tombstone 覆盖旧失败（不使用可能误删新代证据的 unlink）。
  - 修正过时的“后台临时打开 PJSIP logger”注释和 Retry-After 过度承诺文案。
- 第二轮实现后复审：两条均 `NEEDS CHANGES`（无付费通话回退）；指出 Docker 同 ID 重启、
  poll/push 并发覆盖、Registered tombstone 写盘失败和持久 schema 不严格。整改已完成：
  - owner 现在同时绑定 Docker container ID、`State.StartedAt` Asterisk incarnation 和非空 ICCID
    SHA-256；同容器自动重启不再继承旧 `unanswered`。
  - 同 owner 的 failure/Registered read-classify-write 由进程内锁串行；文件写入再按源事件时间 CAS，
    迟到旧 failure 不能覆盖 Registered tombstone。
  - Registered 在落盘前先提交有界内存 fence；磁盘满/只读时，当前 Control 生命周期仍拒绝旧
    failure 触发破坏性恢复。
  - 只有 version=1、有限正数 observed_at、64 位 event key、非空 SIM 指纹且 owner 完全匹配的
    持久 failure 才能驱动 `reg_unanswered`；无源时间的 live 日志可展示但不作耐久恢复证据。
- 最终聚焦测试：`test_status_registration/test_engine_paths/test_line_lifecycle/`
  `test_auto_provision/test_product_boundaries` 为 `187 passed, 29 subtests passed`。
- 全量测试：`PYTHONPATH=. ./.venv/bin/pytest -q tests/` 为
  `1025 passed, 1 warning, 66 subtests passed`。
- Web：`npm run test:line-presentation` 与 `npm run build` 均通过；dist 已按当前源码刷新。
- 第二轮整改后复审 A：`PASS`，实跑聚焦 `187 passed, 29 subtests`，无剩余 P1/P2。
- 第二轮整改后复审 B：`PASS`，实跑聚焦 `187 passed, 29 subtests`；付费 lease、挂断、
  媒体路径及 exact-generation/zero-channel 恢复无回退。
- **尚未完成**：部署；SWu/P-CSCF reload 后 exit 139 独立稳定性闭环；
  Engine 原生自然注册身份/PAI 事件仍为 R3。

### R1b — P-CSCF 变化 / Asterisk exit 139（2026-08-22 实施中）

- 现场证据：Engine 1 保持同一 Docker container ID，但 `RestartCount=1`；Docker 事件为
  `die exitCode=139 → start`，`OOMKilled=false`。退出前依次出现 SWu 重连、P-CSCF 变化、
  `render.py`、`module reload res_pjsip.so` 与显式 `pjsip send register`。因此当前只能把热重载
  认定为高置信触发候选，不能误报为唯一根因；它也不是已经停止的 Control 两分钟重建。
- 预审 A（配置/生命周期）：P-CSCF 是一个 Engine/Asterisk 进程代际的不可变配置；变化后 SWu
  只原子记录 desired marker 和 Engine run-id，不得热重载或显式 REGISTER。Control 持久门控新
  VoWiFi 呼叫、短信、浏览器媒体和新的蜂窝媒体锚点；已有通话、BYE/CANCEL、挂断和付费 lease
  必须继续。随后只允许一次 `core stop gracefully`，由既有 `unless-stopped` 策略从完整 entrypoint
  启动新代并在 Asterisk 前应用新 P-CSCF。
- 预审 B（付费/竞态）：优雅退出事务必须由 Control 管理，并精确绑定 container ID、
  `State.StartedAt` 和 Engine run-id；Control 重启需从 marker 恢复门控。活跃/未知通话不强停，
  不允许 reload/kill 兜底；B→C 合并为最新 desired，回到旧 P-CSCF 要有有界 cancel/abort；首次
  启动 Asterisk 尚未 ready 时不得制造关机 marker；短信不得自动重放。
- 当前实施门禁：先完成上述代际 marker、精确优雅退出、全入口持久 admission fence 与回归测试；
  再由两条原评审链独立复审。两条都 PASS 前不构建部署，不进行真实计费通话。

- 轮 2 整改实现（22:52）：
  - `control/app/engine.py`：admission flock 改为 `LOCK_NB`；明确拒绝与 response-lost 分流；
    stop/abort 各自持久 3 次预算和 5/10 秒退避，耗尽后 marker 留存并要求人工处理；显式 CLI
    Docker 交换改为 5 秒 transport bound。
  - `control/app/main.py`：admission acquire/release 不再放到可遗留的 worker；新 INVITE 与 SIP
    MESSAGE 的 WSS 提交有硬 deadline，超时/取消同步 abort transport；显式 REGISTER 加 final
    admission gate；rebind 耗尽/损坏状态进入可见 ERROR，而非无限尝试。
  - `engine/templates/extensions.conf.j2`：`[msg-from-local]` 在任何 OUT 日志、notify 和
    carrier `MessageSend` 前增加 `STAT(e,marker)`；BYE/CANCEL/re-INVITE 路径未改变。
  - `engine/pcscf_state.py`：保留同一事务的有界 retry metadata；成功 abort 后真正的新事务才清除
    旧预算。已有 marker-before-address 与 invalid-applied→unknown 逻辑未重复重写。
  - 新增回归覆盖：invalid applied 的 fail-closed/写入顺序、flock contention 非阻塞、WSS send
    吞取消时 transport abort、marker 最终取得锁、SIP MESSAGE 双门禁、显式 REGISTER 竞态、
    stop/abort negative rc 退避/预算/人工状态。
- 轮 2 整改测试（22:52）：
  - 聚焦（最后增量后）：`230 passed, 21 subtests passed`。
  - 全量（最后增量后）：`1056 passed, 1 warning, 66 subtests passed`。
  - `py_compile`、`bash -n engine/entrypoint.sh install.sh`、`git diff --check` 均通过。

#### R1b 逐轮评审账本

| 轮次 | 类型 | 结论 | 已打开或关闭的范围 |
|---|---|---|---|
| R0-A/R0-B | 实施前双评审 | PASS WITH BOUNDARIES | 确定进程代际不可变 P-CSCF、持久 marker、exact-generation 优雅退出、已有通话与付费 lease 不破坏。|
| R1-A | 实施后配置/生命周期复审 | NEEDS CHANGES | 打开 bootstrap/render/seed 原子性和 Asterisk 未 ready 时丢失发现；已按单锁事务与“发现先耐久化”整改。|
| R1-B | 实施后付费/竞态复审 | NEEDS CHANGES | 打开 cancel 跨代误删 marker、最终载波入口 TOCTOU、fallback 竞态；已按跨代复核、admission boundary、fresh-confirmation 整改。|
| R2-A | 增量复审 | NEEDS CHANGES | 聚焦 `165 passed, 21 subtests` 但覆盖不足；打开 P1-2 WSS 无界持锁，以及 P2-1 明确拒绝后 reservation 卡死、P2-2 显式 REGISTER/本地 MESSAGE 绕过、P2-3 applied 损坏未门控。|
| R2-B | 增量复审 | NEEDS CHANGES | 聚焦 `210 passed, 17 subtests`；打开 P1-1 SIP MESSAGE 绕过及 P1-2 可取消线程/无界 WSS 持锁。其余 abort 跨代、入呼/外呼/canary、paid lease、BYE/CANCEL、bootstrap/fallback 已通过。|
| R2-IMP | 最小整改与测试 | PASS（实现者自测） | 2×P1/3×P2 均有实现或误报反证；聚焦 229、全量 1055 通过。只表示可送审，不等于复审 PASS。|
| R3-A | 配置/生命周期增量复审 | PASS 后等待最终 delta 确认 | 曾确认轮 2 全部关闭，实跑 214+29；因随后 double-cancel 同步 worker 增量，已要求只读再确认，不能沿用旧 PASS 部署。|
| R3-B | 付费/竞态增量复审 | NEEDS CHANGES → 已整改待确认 | 复现显式 REGISTER 双 cancel 早释 flock；已把锁所有权整体移入同步 worker，新增回归后聚焦 230、全量 1056。|
| R4-A | 配置/生命周期最终 delta | PASS | 同步 worker 锁所有权、409 fail-closed、异常 finally、API 响应及既有 WSS/对话路径均通过；实跑 115+9。|
| R4-B | 付费/竞态最终 delta | PASS | 全量 1056+66；确认 MESSAGE 双门禁、hard deadline、REGISTER 双 cancel、有限 stop/abort、paid lease/挂断均无回退；未做真实拨号/短信。|

#### R1b 部署执行账本

- 23:03 部署前只读门禁：Engine 1/7 均为 0 active channel；按生产语义查询
  `list_open_cellular_call_leases` 返回空列表。旧代分别为
  `d57645aa… / StartedAt 13:32:01 / RestartCount=1` 与
  `e40ade43… / StartedAt 12:30:59 / RestartCount=0`；尚未切换或强停。
- 已生成服务端源码回滚包
  `/opt/mdd-gateway-backups/source-before-r1b-20260822-2303.tar.gz`；随后以校验 dry-run 为空确认
  本地当前源码与 `/opt/mdd-gateway` 同步，明确排除生产 `data/`、镜像、Git 元数据及本任务板。
- 服务端 Docker 下载链使用宿主既有 `127.0.0.1:24445` HTTP relay，relay 出口为约定的
  `10.44.0.14:1081` SOCKS；未把代理地址固化进产品代码或镜像配置。
- 23:12 首次 `./install.sh reload` 在 PC/SC GitHub tarball 下载处以 `wget` 网络错误 4 退出；旧
  Engine 未切换。宿主 relay 与 SOCKS 探针均成功，构建容器的直连探针超时；复现确认 GNU wget
  未使用仅有的大写代理变量，小写 `https_proxy` + host network 探针成功。该失败归类为构建网络
  配置，不是代码/校验和失败。
- 23:17 以一次性 `--network host` 和小写 build proxy 参数重新构建；代理只作为 Docker 预定义
  build arg，不写入 Dockerfile/最终 ENV。期望 runtime/base fingerprint 分别为
  `580c3267…954e3fa` / `1034ce26…e1235e8`。PC/SC tarball SHA-256 已通过并完成编译，当前进入
  sysmocom Asterisk 构建。此时旧 Engine 仍提供服务，尚无部署 generation。
- 下一门禁：记录新镜像 ID、runtime/base fingerprint 与镜像内关键源码 hash；再次检查 active
  channel/open paid lease，然后才允许 graceful cutover。切换后还要记录新容器 ID、StartedAt、
  run-id、RestartCount、UK/FR 隧道/注册/出口和超过旧两分钟故障窗口的稳定观察。
- Engine 新镜像构建完成：`sha256:f16a1461…a6a394`，runtime/base fingerprint 为
  `580c3267…954e3fa` / `1034ce26…e1235e8`；镜像内 `pcscf_state.py`、`swu_ike.py`、`render.py`、
  `extensions.conf.j2`、`entrypoint.sh` 与服务端源码 hash 全等，Asterisk 20.7.0 可执行，最终 ENV
  无代理，未发现 runtime `res_pjsip reload`/强制 REGISTER 字符串。
- Control 新镜像构建完成：`sha256:afeb3c23…38a6cf`，版本 1.3.13；关键 Control、Store、
  Orchestrator、WebUI hash 与服务端源码相等，镜像内 py_compile 通过，最终 ENV 无代理。`.23`
  缺 BuildKit/buildx，构建时只在 stdin Dockerfile 流中把已核实宿主平台展开为 linux/amd64；仓库和
  服务端 Dockerfile 未修改、未固化平台。
- **R1b-DEPLOY-1 REOPENED（现场新增，23:44）**：新 Engine 真实启动时 SWu 已 CONNECTED，随后
  `invalid P-CSCF bootstrap result` 并在 Asterisk 前重启。根因已逐行确认：
  `pcscf_state.render_bootstrap()` 调用真实 `render.py` 时继承 stdout；其多行 `[render]` 日志被
  entrypoint 的 command substitution 一起捕获，首词不再是 `fresh/fallback/none`。原测试没有使用
  会输出 stdout 的真实 renderer，故 1056 测试与双复审均未覆盖此 shell/CLI 契约。
- 部署安全事件：停旧 Control 会关闭 VPCD；卡移除观察随后停止并删除两个 Engine，因此“先停
  Control 再 graceful Engine”的原部署假设不成立。事件前已再次确认两 Engine 0 active call、open
  paid lease 为空，所以没有计费通话被切断，但新代被意外创建。该切换顺序已作废，禁止复用。
- 已把 `engine:latest` 回滚到旧镜像 `sha256:a4195af7…5a924`。France 实例 7 的旧代
  `670d4f60…` 已恢复为 `Registered`、0 call、RestartCount=0；UK 实例 1 旧代已恢复且
  RestartCount=0，但仍在既有 SWu CONNECTING/有界重连，尚未恢复 Asterisk，不能误报为已恢复。
- 最小候选仅为隔离 bootstrap CLI stdout（renderer 日志转 stderr）并补真实 renderer/entrypoint
  契约回归；原两条评审链的实施前评审均已 PASS，现只允许进入该最小实施。
- `R1b-DEPLOY-1-PRE-A` 配置/生命周期预审：`PASS`。将 renderer 子进程 stdout 定向到
  父进程 stderr，renderer stderr 继续继承；CLI stdout 必须严格只有一行
  `fresh <ip>`/`fallback <ip>`/`none`。禁止用 `tail -1` 隐藏协议污染。必测 IPv4/IPv6、
  fresh/fallback/none、子进程失败/超时不提交 marker，额外 stdout token 仍 fail-closed。
- `R1b-DEPLOY-1-PRE-B` 付费/竞态预审：`PASS`（部署有新必须边界）。只改子进程 fd
  路由，不改变 flock 内 render→applied→pcscf→marker unlink 顺序，也不改 admission、已有通话、
  BYE/CANCEL、SMS 或 REGISTER。回归要在 disposable Engine 环境运行 actual `render.py`，核对
  stdout/stderr、pjsip P-CSCF/applied 一致性及 fresh/fallback marker 语义。部署绝不 stop/replace
  Control/VPCD；必须固定新旧 digest、逐线、持续 admission fence，exact-generation graceful
  stop/recreate，第二线不提前切换。若现有 Control 内没有该安全原语，宁可不部署。
- `R1b-DEPLOY-1-IMP`（00:13）：只修改 `engine/pcscf_state.py` 中 renderer 子进程 fd 路由，
  stdout 转父进程 stderr；`engine/entrypoint.sh` 将 bootstrap 协议严格限为 `none` 或两字段
  `fresh|fallback address`，任何额外 token/多行污染均 fail-closed。未修改 flock 交易顺序、
  admission、通话/BYE/CANCEL、SMS/REGISTER 或 paid lease。
- `R1b-DEPLOY-1-TEST`（00:13）：新增真子进程多行 renderer 的 fresh IPv6/fallback IPv4/
  none/非零/timeout 契约回归，验证 stdout/stderr、applied/pcscf 及 marker 提交语义；新增
  entrypoint IPv4/IPv6/多余 token fail-closed 回归。结果：单文件 `23 passed`；受影响组合
  `221 passed, 29 subtests passed`；全量 `1062 passed, 1 warning, 66 subtests passed`；
  `bash -n`、`py_compile`、`git diff --check` 通过。disposable Engine 内 actual `render.py` 仍是构建后、
  部署前强制验证门禁，不用本地伪 renderer 冒充。
- `R1b-DEPLOY-1-POST-A`（00:20）：配置/生命周期复审 `PASS`，无 P1/P2；复跑 23 项及
  shell/Python/diff 门禁通过，确认 Bash 3+ 正则、stdout/stderr、fresh/fallback/none、失败原子性
  和额外 token fail-closed 均符合预审边界。
- `R1b-DEPLOY-1-POST-B`（00:20）：付费/竞态复审 `PASS`，无 P1/P2；复跑 23 项及静态门禁
  通过，确认同一 flock 内交易顺序未变，三文件未触及 admission/通话/BYE/CANCEL/
  SMS/REGISTER/paid lease。同时重申 actual `render.py` 必须在 disposable Engine 中验证；现有
  restart/start 不是安全切换原语，在新原语双评审前不得切换现线。
- `R1b-DEPLOY-1-IMAGE`（00:29）：以已完整构建的 R1b base digest `sha256:f16a1461…a6a394`
  生成未指向 `latest`的候选镜像 `sha256:63460158…63b7b6`；runtime/base fingerprint 为
  `039ef46a…560101` / `1034ce26…235e8`。镜像内 `pcscf_state.py` / `entrypoint.sh` hash 与本地
  精确相等，`render.py` 仍为已复审基线 `7237250a 5605`。
- `R1b-DEPLOY-1-ACTUAL-RENDER`（00:29）：在 `--network none` 的一次性候选 Engine 中运行实际
  `/usr/local/bin/render.py`。fresh IPv6 stdout 精确为 `fresh fd00::2`，fallback IPv4 精确为
  `fallback 10.0.0.2`；各 19 行 `[render]` 只在 stderr，pjsip bind/match、`pcscf.applied`、
  `pcscf` 一致，fresh 清 marker/fallback 保留。用缺 AMI 凭据的实际 render 失败验证：
  CLI 非零、stdout 0 字节、无 applied/pcscf 提交、marker 保留。候选镜像未部署。
- `R1b-DEPLOY-2-PRE-A/B`（00:40）：“原子覆盖两个运行时文件 + same-address 普通
  P-CSCF marker + 旧 Control 自动 reconcile”方案双预审均 `NEEDS CHANGES`，且付费/竞态链定为
  P1，禁止执行：
  - 旧 SWu 重连/恢复时合法重发当前同一 P-CSCF；未 reserve 的 same-address marker 会被删除为
    `cancelled`，已 reserve 则会转 `cancel_requested`并可触发 `core abort shutdown`。所以它不是持久
    部署 fence，不能保证重启或“失败保 marker”。
  - 外部一次性 Python 只能持文件 flock，无法持活 Control 进程内 `hub.recovery_lock` /
    `engine_recovering`，会与 health/manual lifecycle 形成双 owner。
  - 原子覆盖顺序（Python 先于 entrypoint）本身可行，但容器会呈现旧 Image ID + 新 writable
    layer，只能作为明确记录 hash/模式/回滚边界的临时机制，不能在前两个安全问题未解决时执行。
- 00:35 现网只读证据：UK 容器 `7dde7972…` 和 France 容器 `670d4f60…` 仍运行旧
  `sha256:a4195af7…5a924`，均 `RestartCount=0`、`0 active channels`、IMS `Registered`；两线 marker
  均 absent，open cellular paid lease 为 `[]`。这些只是现状，未作为切换授权。
- `R1b-DEPLOY-2-PRE-A`（00:41）：配置/生命周期链 `NEEDS CHANGES`。
  - A 层若持 `.pcscf-rebind.lock` 跨 `start_absent`/ready，会与新 Engine entrypoint 的
    `init-run` 取同一 flock 形成确定性死锁。flock 只能覆盖旧代 fence 与 exact graceful
    stop/remove；记录 `source_removed` 后须清临时 P-CSCF fence并释放 flock，再由持久且独立的
    `engine-maintenance.json` 单独门控新工作，随后才能按 immutable digest 启动 absent Engine。
  - 原 B 的 SIGTERM 存在付费操作 TOCTOU；采样式 B2 也不能作为无停机切换。若继续 bootstrap，
    必须 exact pause 唯一旧 Control、持续刷新 PC/SC maintenance marker、证明完整 generation、
    Docker lifecycle quiet、全局 paid lease 空且所有 Engine authoritative zero，并具备旧 Control
    digest/完整容器配置回滚；任何未知立即失败关闭。
  - maintenance marker 必须严格 schema、原子耐久化、有限 attempts/manual fail-closed；
    `_clear_runtime_state`、health/hotplug/manual start 及 candidate dialplan 都必须把它作为 veto，
    BYE/CANCEL/hangup 继续放行。
- `R1b-DEPLOY-2-PRE-B`（00:41）：付费/竞态链 `NEEDS CHANGES`。
  - 同样确认 A 层 flock 死锁，要求在创建/等待新 Engine 前释放，之后仅由独立 maintenance marker
    维持 admission。
  - 明确否决让 Engine 保持在线的 B/B2：carrier inbound、已排队 INVITE/MESSAGE、AMI/WSS/
    Docker 已提交动作都无法被有限采样排除；`pcsc-maintenance` 不是 Engine admission fence。
  - 不注入旧进程代码时，唯一可继续评审的最小 bootstrap 是一次有界维护停机：exact pause 旧
    Control；从外部对每个 exact Engine 调用已审 `capture_and_stop_if_idle`，由 graceful barrier
    阻止新 carrier call并等待滑入通话自然结束；所有 Engine 均非 force 安全移除后才删除旧
    Control、启动新 Control。active/unknown、paid lease、SQLite 不确定或 generation 变化均立即
    停止，不能强停或盲目回放。
  - 仍须在实施前核实旧 Engine 实际 `extensions.conf` 已有 P-CSCF `STAT()` 最终门禁，补
    same-ID restart 下 capture 有界 fail-closed 回归，并设计第一条已停止而后续线路失败时的安全
    事务/回滚；第一条新代未按 digest/run-id/StartedAt/Asterisk/hash/稳定窗口验收前不得动第二条。
- 因双预审尚未 PASS，本轮**没有修改产品代码、没有部署候选镜像、没有切换或停止任何现线**。
  上述结论是新的设计门禁，不会使 DEPLOY-1 已冻结实现退回或触发重复修复。
- `R1b-DEPLOY-2-EVIDENCE`（00:42，只读）：逐个检查现线 UK/France 容器内实际
  `/etc/asterisk/extensions.conf`，两者都**没有** P-CSCF `STAT()` 最终门禁；这与当前源码不同，
  因而 A 层的临时 P-CSCF fence 不能被旧现线用作 bootstrap 依据。两线仍为精确旧 digest、
  `RestartCount=0`、0 active channels；Control 仍为精确旧 generation且未暂停。
  同一运行中 Asterisk 的 `core show help core stop gracefully` 明确说明该命令停止接受新呼叫并等待
  所有现存通话正常结束，因此 bootstrap 只能依靠 `capture_and_stop_if_idle` 内已绑定 incarnation 的
  graceful barrier，而不是不存在的 dialplan gate。此证据只收窄设计，不授权执行维护。
- `R1b-DEPLOY-2-DOCKER-PROBE`（00:43）：在 `.23` 用 `--network none --restart no` 的一次性
  候选 Engine 容器验证 bootstrap 所需 Docker 对象语义：exact pause 后 inspect 为
  `status=paused/Paused=true`；exact kill 后同一容器 ID 保留为 `exited/137`；rename 后按同一 ID
  `start` 能恢复，完整对象未被重建；最后仅删除该 Codex 命名的一次性探针并确认不存在。现线
  Control/Engine 未触碰。官方 Docker 文档也把 pause/kill/rename/start 分别定义为暂停全部进程、
  杀死运行容器、重命名容器和启动停止容器；因此保留 exact stopped Control 作为 rollback 对象
  比删除后按猜测重建配置更可验证，但仍须双预审通过。
- 同一探针随后按生产一致的 `restart=unless-stopped` 重跑：paused 容器经 exact `docker kill` 后
  等待 2 秒仍为同一 ID `exited/137`、`RestartCount=0`，未被策略自动拉起；显式 `docker start`
  才以同一 ID 恢复运行。探针已删除。这关闭了“kill 后旧 Control 会自行复活”这一 Docker 语义
  假设，但产品事务仍必须逐步 inspect，不能仅依赖本次探针。
- 又以两个 `--network none` 临时容器实跑完整名称切换/回滚：旧对象 pause→kill→rename 为唯一
  rollback 名后，原名称可创建 candidate；删除精确 candidate 后，旧对象可按同一 ID 复名并
  start，且 `unless-stopped` 配置仍保留。所有探针已删除。该结果证明“保留旧 Control 对象”在
  当前 Docker 上可实现，尚不等于应用级双预审 PASS。
- `R1b-DEPLOY-2-PRE2-A`：配置/生命周期链对修订 A 的 flock 拆分、独立 marker、exact
  start-absent 与 crash convergence 认可；Bootstrap 仍为 `NEEDS CHANGES`。它要求 pause→kill
  必须有目标 daemon canary（已完成），并指出 partial rollback 不能在旧 Control 停止时先启动旧
  Engine：Agent/VPCD 经 Control，Engine 无法可靠 ready，且旧版 Engine/Control 不认识新 marker。
  回滚必须先以 fresh process 恢复唯一旧 Control，同时从外部隔离用户 REST/WSS、保持 VPCD 和
  Engine callback 通路，再逐线恢复旧 Engine；owner handoff、global committed/rollback_committed
  CAS、create-spec hash和逐线清理顺序必须成为协议及测试。
- `R1b-DEPLOY-2-PRE2-B`：付费/竞态链同样认可 A 层，Bootstrap 仍为 `NEEDS CHANGES`。它已根据
  `.23` canary 撤回“Docker API kill 会自动拉起 unless-stopped”的误报；保留 exact terminal/
  RestartCount/events 观察。剩余 P1 与 A 链一致：不得在旧 Control paused 时先启动不识别 marker
  的旧 Engine。quiet window 必须至少 `64*T1`（通常 32 秒）并持续复核 generation/events/
  paid lease/channel；出站短信 `pending/sending/inflight` 必须为空，unknown 绝不重发；旧 Engine
  无 run-id 时 schema 需显式 `legacy_run_id_absent` 并绑定 ID/image/StartedAt/RestartCount/PID。
- `R1b-DEPLOY-2-SMS-EVIDENCE`：Asterisk 20.7.0 源码核验发现 `core stop gracefully` 只阻止新
  channel；`res_pjsip_messaging.c::module_on_rx_request` 在 shutdown 期间仍可对新的 MESSAGE 返回
  202 并 `ast_msg_queue()`，没有 `ast_shutting_down()` 判断。现线 taskprocessor 同时存在
  `ast_msg_queue` 与 `pjsip/messaging`，当前队列均为 0。因此 active-channel=0 不能证明短信 quiescent，
  不能把此 P2 当作文字免责声明跳过；修订设计必须增加可验证的 PJSIP messaging admission/drain
  barrier，或保持一条不会丢 inbound SMS 的 durable event/reconciliation 路径。
- `R1b-DEPLOY-2-ROLLBACK-EVIDENCE`（只读）：现线旧 Control 实际同时监听用户/API
  `0.0.0.0:8443` 与 `:8000`，VPCD 独立监听 `35963..35978`；默认 Engine bridge 为
  `172.17.0.0/16`。旧镜像内实际代码确认：fresh hub 没有 card；`pcsc-maintenance` 新鲜时
  `card_monitor` 跳过整轮；`_line_auto_start_allowed` 对空 card list 返回 `no_card`。因此 rollback
  可先启动 fresh 旧 Control，并用宿主 txid 隔离仅阻断外部 8443/8000、保留 loopback与 Docker
  bridge callback、完全不阻断 VPCD 端口；在 marker 存在时旧内部 owner不会因枚举自动创建 Engine。
  该证据修正了此前错误的“先旧 Engine、后旧 Control”顺序，仍待 PRE3 双评审。
- `R1b-DEPLOY-2-PRE3-A/B`：两链仍为 `NEEDS CHANGES`，但均确认 rollback“先唯一旧
  Control、后旧 Engine”的方向和 A 层其余事务边界已收敛。新打开范围严格限于：
  1. 远程 Agent 的 VPCD/modem/health 也经 8443 WebSocket；单纯 L4 阻断外部 8443 会连 Agent
     一起挡住，rollback fresh Control 必须使用 path-aware TLS/WSS maintenance proxy（只放行
     Agent/VPCD与 Engine callback），或可证明与用户来源不重叠的 allowlist；
  2. messaging 模块 absent + 两个 taskprocessor queue=0 仍不能证明卸载前已 202 的 MESSAGE 已
     durable 到 Control；它可能已离开队列而后台 `notify.py`/HTTP 尚未完成。必须在旧 Control仍
     运行时卸载并完成 callback/durable drain，之后才准 pause；
  3. 保留 Engine时的 module reload 必须核实 module、PJSIP service/msg tech/serializer/queue及无资费
     local MESSAGE canary，且卸载到 terminal 全程绑定 exact incarnation。
- PRE3 误报处置：配置链再次要求“目标 daemon canary”，但 `.23` 的 `unless-stopped`
  pause→Docker API kill→terminal/端口释放→rename/start canary已经实跑并登记；它是每台目标机的
  强制 preflight，不会被提升成未经证明的跨 daemon 假设，也不会因此重复实现 Docker 逻辑。
- `R1b-DEPLOY-2-MESSAGE-PROBE`：在 `.23` 用候选 Engine digest、`--network none`、只读最小
  Asterisk 20.7 配置运行一次性容器，未触碰现线。模块加载时本地 UDP MESSAGE 得到
  `202 Accepted`，`ast_msg_queue` processed 从 0→1；non-force unload 后模块 absent、
  `pjsip/messaging` taskprocessor 消失、同一 MESSAGE 得到 `501 Not Implemented`；non-force load
  后 module/serializer 恢复、同一 canary 再次 `202`，queue processed 变 2。远端容器/临时目录和
  外置盘测试 fixture 均已精确删除并确认。该探针证明 unload/load 的 admission 响应与基本可逆性，
  但不反驳 PRE3 的 durable-drain P1；PRE4 仍需在 Control在线时证明卸载前已 202 的回调已排空。
- `R1b-DEPLOY-2-PRE4-A/B`：两链仍为 `NEEDS CHANGES`，但 rollback alternate-port old Control +
  独立 path-aware maintenance proxy 方向通过；白名单本身未发现用户付费入口。剩余 P1 已收窄为
  legacy MESSAGE watcher 建立时序：必须在入口隔离后、任何 quiet/no-notify 采样前先记录起始
  inode/offset并持续观察整个 pre-drain→unload→post-drain；期间任何 append/rotate/truncate 都中止、
  reload、保持 Control在线。这样不会把“最后一次 no-notify 与 checkpoint之间”的新 202 错包进
  baseline。新增 P2：代理需无歧义规范化 `/api/...` 与实际 `/mdd/api/...`，query不参与 path
  决策，严格校验 WS upgrade/HTTP method/真实 socket peer；入口隔离需在 established accept 前
  主动 RST 旧浏览器连接；events文件系统与SQLite写入能力须先证明，post-drain窗口大于
  notify 3 秒 + concat约1秒 + margin。
- `R1b-DEPLOY-2-PRE5-B`：付费/竞态链正式 `PASS`，无剩余 P1/P2；连续双文件 watcher、RST +
  external established=0、8 秒 post-drain、proxy Agent/Engine 白名单与真实 peer 门禁均通过。该范围
  现在冻结，配置链后续整改不得重开或弱化。
- `R1b-DEPLOY-2-PRE5-A`：配置/生命周期链仅剩部署封装 `NEEDS CHANGES`：alternate upstream
  18443/18000 必须只绑定 loopback并覆盖 IPv4/IPv6外部拒绝；proxy健康后再原子把标准端口从RST
  交接给proxy；inotify须先注册文件+父目录再取stat baseline并排空注册期事件；SQLite探针须真
  commit/独立连接读回/commit删除；proxy whitelist/full模式须持久绑定txid并只根据
  `rollback_committed`恢复。核心 maintenance/付费/MESSAGE 方案不再打开。
- `R1b-DEPLOY-2-PRE6-A`（01:05）：配置/生命周期链确认 nft 原子交接、RST/`ss` 门禁、inotify
  注册时序、SQLite 真提交探针及 durable deny-by-default 均已关闭；只保留两个增量：
  1. rollback old Control 若为 Docker bridge，容器内 `127.0.0.1` 不能充当宿主/proxy upstream；
     必须按实际 network mode 选择可证明且外部不可达的拓扑，否则 fail-closed；
  2. proxy 普通重启改变进程代际后，不能依赖已退出的安装器或人工重新开放 full mode，必须有仍
     存活且有界、按 durable `rollback_committed` 事实恢复的 owner。
- `R1b-DEPLOY-2-PRE7-A`（01:11，`PASS`，只复核 PRE6 两项）：bridge rollback 使用两个明确分支，禁止
  猜测网络拓扑。原 Control 为 host network 时，old upstream 与 proxy 均为 host network，old 只
  监听宿主 loopback alternate ports。原 Control 为 bridge 时，old 在容器内监听 `0.0.0.0` 的
  alternate ports，宿主 publish 仅绑定 `127.0.0.1`；old 与 proxy 另接仅二者加入的专用 maintenance
  bridge，proxy 只访问 old 的该网络地址。old 为管理 Engines 所需的原网络若必须保留，则在
  `DOCKER-USER`/nft 按 ingress bridge、目标 old-network IP 和 alternate port 拒绝一切访问，proxy
  不走该地址；启动门禁用 Docker inspect、IPv4/IPv6 socket/外部探测和 proxy upstream probe 共同
  证明。任何多余 attachment、可外部访问或事实不明都保持 RST/deny 并中止。
- `R1b-DEPLOY-2-PRE7-EVIDENCE`（01:09，只读）：`.23` 当前唯一 Control 的 Docker inspect 为
  `network_mode=host`、`restart=unless-stopped`、无 PortBindings，且只加入 host network；因此本次
  现场 rollback 将走 host-network/loopback 分支。bridge 分支仍须作为通用代码被自动判别和测试，
  不能把现网事实硬编码成固定部署假设。
- PRE7 的 proxy 恢复 owner 是 proxy 本身的启动状态机，不是安装器：每次进程启动先
  maintenance-deny，并以文件锁读取 fsync 后的 global manifest；只有 global phase 为同一 txid 的
  `rollback_committed`、manifest 中 immutable proxy image/container ID 与自身容器 ID/镜像声明
  一致、old upstream 精确健康且路径门禁自检通过，才用 CAS+fsync 把 mode 绑定到本次随机
  `process_boot_id` 后切 full。Docker 对同一容器的普通 restart 保留 container ID，故可由新进程
  自动重新证明并恢复；容器被重建、txid/image/upstream/manifest 任一不一致时永久 deny，必须由仍
  持有部署事务 owner 的显式恢复流程更新 global manifest，旧进程不能凭陈旧 mode 自行开放。
- PRE7 最终结论：host/bridge 两分支、外部旁路拒绝及事实不明 deny 已关闭 loopback 拓扑 P1；proxy
  每次启动先 deny，再以 durable manifest、exact container/image、old upstream 和路径自检重新证明，
  CAS/fsync 后才开放并绑定新 `process_boot_id`，已关闭普通重启永久 deny P2。两条预审链至此均
  `PASS`，后续不得在无新复现的情况下重开 PRE1–PRE7。
- `R1b-DEPLOY-2-IMPLEMENT-1`（实施中，未部署）：新增严格 per-line
  `engine-maintenance.json` schema、原子 write/fsync/dir-fsync、文件锁、损坏/存在即 fail-closed、完整
  source/target Docker+StartedAt+restart/PID/run-id/image 事实与合法 phase CAS；普通 `_clear_runtime_state`
  保留 marker。`start_absent` 与普通 start 共用唯一 create-spec 路径，但前者要求 immutable digest、
  durable marker 和 Docker name 真 absent，存在任何对象均不 remove/clear。
- Candidate Control 的统一 admission 已接入 global/per-line marker：新 call/SMS/REGISTER、浏览器初始
  INVITE/MESSAGE、普通 start/recovery/hotplug 均被挡；status sampler 在 marker 期间只展示 maintenance，
  不接触 Docker/AMI；HTTP mutation fence 保留 authenticated Engine drain callback 与 VoWiFi/蜂窝 hangup，
  BYE/CANCEL/in-dialog 流量仍不取新工作锁。Candidate dialplan 对 incoming/outgoing call、incoming/outgoing
  SMS 及 media canary 均在持久日志/notify/paid动作前检查 maintenance marker。
- `R1b-DEPLOY-2-IMPLEMENT-2`（实施中，未部署）：新增独立 stdlib TLS/HTTP/WSS maintenance proxy 与
  单独 Dockerfile；无 Docker socket。维护模式只允许 exact Agent/VPCD WS path/method/Upgrade 与真实
  socket peer 白名单中的 Engine callback，拒绝重复 context、encoded slash/backslash、dot segment、
  paid/management path 和 XFF 伪造。proxy 每次进程先 deny；只有 strict global manifest、exact
  container/image self facts、old upstream 双端健康、policy self-test、二次 manifest CAS 一致时，才
  fsync mode 并把 full 绑定本次 `process_boot_id`；后台每 0.5 秒撤销失效授权。
- `R1b-DEPLOY-2-IMPLEMENT-3`（实施中，未部署）：新增 Linux inotify 双文件 watcher 原语，严格按
  parent+existing-file 注册→drain→baseline stat→drain/re-stat 顺序 arm；任何 append/create/rotate/
  truncate/attrib/overflow 或 100ms stat差异均中止。新增真实 logs write/fsync/rename/dir-fsync/delete
  探针、生产 SQLite insert+commit/独立读回/delete+commit/第三连接确认及 open lease/pending outbound
  work 只读门禁。尚未接入完整 host transaction wrapper，不能据这些原语宣称可部署。
- 当前聚焦自动化：maintenance/notify/P-CSCF/engine paths/line lifecycle 为
  `189 passed, 1 warning, 21 subtests passed`；新增 proxy/watcher/maintenance 组合为
  `49 passed, 1 warning`；py_compile 通过。两条原评审链正在做实施中增量审计，未形成最终复审。
- `R1b-DEPLOY-2-IMPL-AUDIT-1`（01:45，两链 `NEEDS CHANGES`，新代码证据触发合法
  `REOPENED`，不是重开旧评审）：发现并已在本地整改的项目包括：
  - `start_absent` 改为 maintenance lock 内严格校验 txid、intent phase 与 immutable digest；任何
    损坏/其他事务/错误 phase/digest 均不 create/remove；
  - phase machine 新增 exact-source `aborted` 终态与 `rollback_starting → rollback_started →
    rollback_verified`，rollback 新 generation 固定 source image digest，禁止 wrapper 直接 unlink；
  - source facts 新增显式 `run_id_mode=present|legacy_absent`，legacy 只允许 source 真正缺文件，所有
    candidate/rollback generation 必须有格式受限 run-id；version 禁止 bool、StartedAt/run-id 格式收紧；
  - status sampler、disabled stop、health recovery 在 recovery lock 内重查 marker；`push_status` 和
    `pcscf_rebind/cp_mode` callback 在维护期只展示/广播 deferred，不做 AMI/reconcile/recovery/config；
  - mutation fence 精确放行 committed cellular call `release`，仍阻止 answer/commit/ring/new call；
    cellular SMS 与 legacy local dial 新增 shared per-line maintenance admission，远程 SMS 在 RPC 前写
    pending、结果/unknown 在释放锁前耐久化，legacy dial 在 ATD 前写 paid lease；
  - global manifest 仅在 strict `committed/rollback_committed` 且所有 line terminal 时解除 Control
    global fence；per-line marker仍独立；incoming/outgoing direct-reject call 不再从 `h` handler制造
    无 start row 的 terminal 记录；proxy 响应允许合法重复 `Set-Cookie`，仅 framing/routing header
    重复失败关闭。
- IMPL-AUDIT-1 仍开放的唯一设计增量是 proxy full authorization/revocation，配置链对“常驻 host
  supervisor 代替 proxy 自证 Docker”方向认可但要求补齐：授权前后 exact Control inspect 相同，
  host alternate socket PID/cgroup 或 bridge attachment owner 精确；proxy ready file+本地 health
  `process_boot_id`+exact inspect 三方一致；撤权必须 supervisor 先 durable `revoking(epoch)`，proxy
  停止新 admission 并主动关闭/等待 full HTTP/WSS active=0、fsync `deny_applied` ack，supervisor 收到
  ack 后才能改变 manifest。inotify/50ms 只能唤醒，不能作为撤权正确性证明。该增量须再次 PASS 后
  才实现 supervisor/proxy full mode；当前 proxy full mode不可部署。
- `R1b-DEPLOY-2-IMPL-AUDIT-2` 付费/竞态链增量复审 `NEEDS CHANGES`（本地无拨号/无短信
  canary 复现）：
  - P1：已提交的蜂窝通话 release 在首次 `call.hangup/status` await 取消时，可以在
    `termination_task` 建立前退出；媒体仍 fresh 时 paid lease supervisor 还会续租。证据为同一
    operation ID 保留但 server-owned termination supervisor 未创建。
  - P1：本地 ModemManager SMS 的 durable reservation 在 `to_thread` worker 内才建立；HTTP task 在
    reservation 前取消会先释放 maintenance flock，worker 仍可继续 Create/Send。
  - P2：远程 SMS 在 RPC 前确认 `ModemUnavailable` 时直接返回，已预写的 message 可长期停在
    `pending`，使 outbound-zero 维护门禁无法收敛。
  - 本轮只打开上述三项。release/hangup 维护放行、health/status/callback 维护无生命周期
    副作用、dialplan 无 ghost call row、legacy run-id schema 等已通过边界保持冻结。
- `R1b-DEPLOY-2-IMPL-AUDIT-2-PRE`（双链 `PASS`）：
  - committed release 在 `commit_lock` 内且首个 `await` 前发布 `terminating`/deadline 并创建或复用唯一
    server-owned coordinator；退出锁后 HTTP 只 `shield` 同一 task。coordinator 是持久状态、有界
    hangup/status 和告警的唯一 owner；paid lease 见终止状态立即停止新 renew，不等待 task。
  - 本地 SMS 由同一有总 deadline 的同步 worker 按 `hub.recovery_lock → engine maintenance flock →
    短 SQLite 事务` 排序；worker 自己持 flock，锁内最终 marker 复查、pending、reservation、mmcli
    及最终状态落盘。async 任意次数取消均不能释放 worker 所有的 flock。
  - reservation 的 existing message 必须精确匹配全部字段；`message_id IS NOT NULL` 唯一索引升级前
    检测冲突并 fail-closed，禁止静默删除。完全相同重入返回 `created=False` 且绝不重放短信。
  - 远程 SMS 仍在 boundary 内预写 pending；只有证明 RPC 未提交的 `ModemUnavailable` 落盘
    `failed`，已开始发送后断线/超时保持 `unknown`。
- `R1b-DEPLOY-2-IMPL-AUDIT-2-IMP`（本地实施，未部署）：
  - `main.py/call_media.py`：release 请求在首个可取消 await 前发布唯一 server-owned
    release coordinator；coordinator 在 `commit_lock` 内发布唯一 termination owner，HTTP 只 shield。
    termination owner 持久 `terminating` 失败仍继续有界挂断；lease supervisor 见终止状态停止新 renew。
  - `main.py/cellular_sms.py/store.py`：本地 SMS 改由 server-owned async task 持 `hub.recovery_lock`，
    其有总 180 秒预算的同步 worker 自己持 engine flock 到最终状态落盘；`message_id` 部分
    唯一索引、冲突升级 fail-closed、exact reservation 返回 `created=False` 并不重放。Create
    超时改为 `unknown`。远程 pre-send unavailable 在解锁前收敛为 `failed`。
- `R1b-DEPLOY-2-IMPL-AUDIT-2-TEST`：新增无拨号/无短信回归，覆盖 HTTP 取消时 termination
  owner 存活、并发 release 唯一 owner/operation ID、持久化失败仍挂断、terminating 停止 renew、
  本地 SMS 重复取消仍持 flock、reservation 幂等/冲突升级、远程 unavailable 先 failed 后解锁。
  结果：三个直接文件 `104 passed, 1 warning`；maintenance/notify/proxy/guard/lifecycle/path 组合
  `310 passed, 1 warning, 21 subtests passed`；`py_compile` 和 `git diff --check` 通过。现进入双链实施后复审。
- `R1b-DEPLOY-2-IMPL-AUDIT-2-POST-DELTA`（02:23，本地实施，**未测试、未复审、未部署**）：
  - 两条复审链在 310 基线后新指出：SMS 不能只保护本地 ModemManager worker，必须把 transport
    选择到响应交付的整条提交纳入 server-owned owner；Control 重启后也不能凭时间过期再次提交。
    release 请求必须在等待 `commit_lock` 前同步发布，且 signaling cancel 后只能由 release
    coordinator 收敛 lease/媒体，不能由请求协程与 coordinator 双重关闭。
  - 当前增量已加入每线路 durable `sms_submission_guards`（UUID operation、payload hash、active/
    orphaned/completed、结果缓存和显式 ack）；Control 启动把未完成 owner 标为 orphaned，同一操作只
    返回缓存或 unknown，禁止自动重发。Web 保存未确认 operation，成功后 ack；unknown 只能由用户
    明确确认后解除。该增量尚未跑迁移/API/Web 回归，不能宣称安全。
  - release 的 `release_requested` 已在首个 await 前同步置位，信令 RPC 前后均复核；cancelled
    signaling 不再自行保存 terminal/关闭媒体，而是让已发布的唯一 coordinator 在取得锁后完成。
    仍须以真实 manager/close 路径回归证明没有遗漏或双 close。
- `R1b-DEPLOY-2-PRE8`（设计双链 `PASS`，实现门禁仍关闭）：proxy 不得自证并自行提升 full；常驻
  host supervisor 是唯一授权 owner。它必须核对 exact Docker generation、alternate socket owner、
  ready/health/process boot 三方一致并以 CAS+fsync 授权；撤权先写 durable `revoking(epoch)`，proxy
  原子停止新转发、主动收敛已授权 HTTP/WSS，`active=0` 后写 `deny_applied`，supervisor 取得 ack
  才能改变 manifest。当前 proxy 已实现 durable `active_full/forwarding_full`、manifest 变化撤权和
  reserve→forward commit 线性化；相关原语已纳入 02:36 全量测试，但 supervisor 尚未实现，所以
  仍不可部署。
- `R1b-DEPLOY-2-IMPL-AUDIT-3`（02:28，双链 `NEEDS CHANGES`，仅打开当前增量）：
  - 两链共同确认 release 仍有 pre-signalling media wait/live-check 竞态：请求异常分支会先从真实
    manager 删除 session，使等待 `commit_lock` 的 coordinator 不能保存 `cancelled`。
  - 配置链发现 proxy manifest/digest 损坏只触发内存撤权；恢复旧文件后同 process boot 可自行
    full，必须持久锁存 `revoking/deny_applied`。它同时确认 active/forwarding 计数、zero-active ack
    及 reserve→forward commit 已关闭旧发现。
  - 付费链发现 ACK 直接删除唯一 SMS 幂等记录：服务器已删除但 ACK 响应丢失后，同 operation 会
    再发；且 Web 与后台 allowance 会自动 ACK `uncertain`。必须分离 per-line guard 与永久 operation
    receipt，unknown 只能由用户明确确认。配置链另确认 maintenance paid gate 只应计严格 `active`；
    completed/orphaned 防重放记录跨升级保留但不是仍在执行的付费动作，未知 state fail-closed。
- `R1b-DEPLOY-2-IMPL-AUDIT-3-IMP/TEST`（02:36，本地实施，未部署，等待双复审）：
  - 新增 durable `sms_submission_receipts`；ACK 在同一 SQLite 事务先保存 operation/payload/transport/
    结果回执再释放 per-line guard，ACK 本身响应丢失后相同 operation 只返回缓存或 acknowledged
    unknown，绝不进入 transport owner。Web 对 uncertain 必须显式确认；allowance unknown 不 ACK。
    upgrade guard 只计严格合法的 active guard，任何未知 state 失败关闭。
  - answer/commit 在 `release_requested` 后所有 media-live/result/timeout/exception 分支均不自行
    close，由唯一 coordinator 保存 `cancelled` 并调用真实 manager.close。新增呼出/来电两个真实
    manager 竞态子用例，RPC 均为 0；`_close_cellular_media` 又在入口和最终 manager removal 前
    复核 current task 的 owner 身份，覆盖 release 恰在旧 closer 的 Agent/AMI await 中发布的 TOCTOU。
  - proxy 在同一 manifest→mode 锁序内把 full authorization 验证失败持久 CAS 为 revoking，若
    active=0 直接 deny_applied；damage→observe→恢复原 manifest 后 begin_full 仍拒绝。
  - 聚焦（最终 close TOCTOU 后）：`160 passed, 1 warning, 2 subtests passed`；全量：
    `1141 passed, 1 warning, 68 subtests passed`。Web 六组脚本（含新增 SMS submission safety）和
    production build 均通过；py_compile、`git diff --check` 通过。未拨号、未发送短信、未部署。
- `R1b-DEPLOY-2-IMPL-AUDIT-4`（02:40，配置链先 PASS、付费链增量 `NEEDS CHANGES`）：
  - mode 文件本身损坏时，旧实现只设置 `revoke_event`；恢复损坏前的合法 full JSON 后，同 process
    boot 可重新接受旧 epoch。manifest 认证失败后撤权落盘失败也有相同复活窗口。
  - close owner 二次核对会误挡后续权威终态清理：`hangup_failed` 后 poller 取得双 terminal 样本，
    虽已写 `terminal_confirmed`/`release_state=terminated`，非 coordinator task 仍不能 remove manager。
- `R1b-DEPLOY-2-IMPL-AUDIT-4-IMP/TEST`（02:43，本地实施，未部署，等待双最终复审）：
  - proxy 新增 process-boot scoped、不可逆 `authorization_lost` latch；`begin_full/recheck/commit_forward`
    均检查它，mode/manifest 认证损坏、撤权或 finish 持久写失败均 latch+revoke。恢复旧 JSON 不能解除，
    只有新进程成功 `initialize_deny` 后才能由未来 supervisor 用新 epoch 授权。新增 mode corrupt→
    restore 与 revoke fsync/write failure→restore 两个失败关闭回归。
  - `release_state=terminated` 被视为合法终态 close owner；后续权威双 terminal 样本可清理真实 manager，
    同时仍保留 manager removal 前的 owner 二次复核。新增 hangup_failed→terminal_confirmed 实 manager
    回归。
  - 最终聚焦 `163 passed, 1 warning, 2 subtests passed`；全量
    `1144 passed, 1 warning, 68 subtests passed`；Web 六组脚本、production build、py_compile 与
    `git diff --check` 全部通过。未拨号、未发送短信、未部署。
- `R1b-DEPLOY-2-IMPL-AUDIT-5/FINAL`（02:46，双链最终 `PASS`，本范围冻结）：
  - 配置链补出格式合法但 owner epoch/phase/proxy/digest 不匹配时 `_full_matches=False` 未 latch；
    已统一为：只要 mode 声称当前 boot full，`begin_full/recheck_full/commit_forward/observe_mode`
    任一路径认证不匹配，均在同锁域调用不可逆 latch 并尽力持久 revoking/deny_applied。参数化覆盖
    合法 owner epoch 变化后分别进入三条数据路径，再恢复原 manifest 仍拒绝。
  - 配置链最终复跑 proxy `32 passed` 并 `PASS`；付费链最终复跑相关 `142 passed + 2 subtests`
    并 `PASS`，确认未提交请求不写 upstream、已提交请求仍由 finally/finish 递减并收敛撤权，
    权威 terminal 清理无回退。
  - 最终全量 `1147 passed, 1 warning, 68 subtests passed`；Web SMS safety 与 production build、
    py_compile、`git diff --check` 通过。未拨号、未发送短信、未部署。此范围无新复现不得重开；
    host supervisor 仍未实现，是当前唯一开放实现门禁。
- `R1b-DEPLOY-2-PRE8-FINAL`（02:55，实施前双链 `PASS WITH BOUNDARIES`）：
  - 配置/生命周期链确认 supervisor 必须是宿主唯一授权 owner，依次证明 manifest、
    exact proxy/Control Docker generation、ready、admin health、alternate socket owner 及
    host/bridge 拓扑；所有未知、超时、schema 损坏或写盘失败均保持 deny/manual-required。
    supervisor 复用现有 host orchestrator 托管，不新增独立 systemd 服务。
  - 付费/竞态链要求 full 绑定 supervisor 进程代际与单调活性租约；supervisor
    被杀、重起或租约超时必须自动撤权。`full→revoking` 后新请求立即拒绝，
    已 commit 的 HTTP/WSS 必须有绝对 drain deadline，不能被 drip-feed 无限续期；仅同
    txid/container/image/process boot/epoch 的 `deny_applied + active=0 + forwarding=0` 是有效回执。
  - 两链表述的“自动授权”与“显式本地触发”存在边界差异，实施取严格交集：
    常驻 supervisor 不轮询旧 manifest 自动开闸；本地事务所有者通过 root-only Unix
    socket 显式发出一次性 `recover`，supervisor 完整复证后才签发新 epoch。接口只有
    `status/revoke/recover`，不暴露跳过证明的 `grant`。
  - 固定锁序为 supervisor singleton 生命期锁 → manifest lock → mode lock。Docker、
    admin health 和网络证明先在锁外完成，最终 CAS 锁内仅做有硬超时的 exact
    Docker/socket 复核，锁内禁止调用 proxy admin health。同一事实有界失败后粘滞
    `manual_required`，只有新 proxy boot、新 manifest generation 或显式 `recover` 可重试。
- `R1b-DEPLOY-2-PRE8-IMP-1/TEST`（03:17，本地实现，未部署）：
  - 新增由现有 host orchestrator 托管的 root-only Unix socket supervisor；实现 exact
    manifest/proxy/Control/Engine generation、host/bridge ingress owner、ready、应用层 health、
    process/host boot 和单调 lease 证明；只允许显式 `recover`，无 grant/轮询自动开闸。
  - proxy mode 加入 `host_boot_id/supervisor_boot_id/lease_seq`；健康 full 不再有固定 15 秒寿命，
    只有 revoke 后才开始绝对 drain deadline；ready 文件仅在 process boot/epoch 变化时写盘。
  - 增加全局 entry fence、Control 取得线路锁后的二次 fence 检查、exact Engine 代际清单、
    rollback Control create-spec、标准 8443/8000 ingress owner 与双入口 `/api/auth/status` 证明。
  - 聚焦测试 `96 passed, 1 warning`；全量 `1171 passed, 1 warning, 68 subtests passed`；
    `py_compile`、`bash -n install.sh`、`git diff --check` 通过。私有 Linux runner C 以隔离 Docker
    环境运行 supervisor/proxy 单测 `55 passed`；原始日志仅保存在私有目录。未部署、未拨号、
    未发短信。
- `R1b-DEPLOY-2-PRE8-POST-R1`（第一轮实现后双链 `NEEDS CHANGES`，已作首轮整改）：
  - 打开健康 full 被 15 秒关闭、planned drain 阻塞 5 秒心跳、entry fence 与 paid/Engine admission
    未线性化、recover 在 post-proof 前开 full、标准入口 owner/bridge 范围证明不足、fence 清理无
    安全所有权、Engine 只核数量、Control source/current StartedAt 混用、signal handler 执行阻塞
    revoke、ready 每 100ms fsync，以及 upstream 只探 TCP/TLS 等问题。
  - 已按复审边界增加独立 heartbeat、revoke 后 deadline、严格 entry fence owner、recover 在 fence
    内复证再清、exact Engine/Control/Docker create-spec、标准入口 cgroup/docker-proxy owner、双入口
    应用 health、轻量 signal stop 与低写放大 ready；全量和私有 runner 单测结果即上一条证据。
- `R1b-DEPLOY-2-PRE8-POST-R2`（03:37，第二轮双链只读复审完成，`NEEDS CHANGES`）：
  - **P1 可复现**：`revoke()` 与 `_heartbeat()` 未共享 lease 串行锁。旧 heartbeat 可在 revoke 写盘
    失败并清 `_proof` 后重新写 `full lease_seq`、复活 proof；撤权不能声称单调或失败关闭。
  - **P1 架构矛盾**：renew 持续要求每线 `engine-maintenance.json`，而 Control 只要该 fence 存在就
    拒绝新 call/SMS/REGISTER/start；删除它恢复业务后下次 renew 又失败。必须在 global fence 仍存在
    时，把 terminal exact line facts 交接到同 txid 的非 fence proof snapshot，再按锁序清线路 fence，
    最后清 global fence；崩溃恢复与 >5 秒 renew 都只能使用严格 snapshot 复证。
  - **P1 入口线性化**：recover 发布/释放 global fence 未全部纳入 Engine→PCSCF 双锁；该 fence
    只有 Control 读取，Engine 的 carrier INVITE/MESSAGE 仍只检查 per-line marker，最终零样本与
    CAS 之间可进入新 carrier 工作。必须把 global fence 投影为 Engine 可见的 final-admission fence，
    覆盖 inbound/outbound call、media canary 和双向 MESSAGE，但不得阻断 BYE/CANCEL/re-INVITE/
    hangup；发布后再做连续零证明并在锁内最终复核。
  - **P1 付费终止**：urgent revoke 后 proxy narrow whitelist 会拒绝 Control 已明确允许的 HTTP
    hangup/cellular release。必须只放行精确终止路径，继续由 Control 做 session/CSRF/line/call owner
    鉴权；answer/commit/dial/SMS 仍拒绝。
  - **P2 可复现**：proxy 固定 `Content-Length` 响应读完后缺少 `return`，keep-alive 响应会错误进入
    close-delimited 读取直至超时，虚增 active/forwarding 并拖延 drain。
  - **P2 supervisor 生命周期**：heartbeat 线程只 join 2 秒，若阻塞于无上界 flock，singleton 已
    释放后旧线程仍可能写 mode/status，与新 supervisor 代际竞争。所有锁等待须有总上界和
    stop-generation fence；旧 heartbeat 确认退出前不得释放 singleton，写前复核当前代际。
  - **P2 drain audit**：同步 planned drain 最长 20 秒时主线程暂停完整 renew，独立 heartbeat 仍为
    最后一次旧 proof 续租。drain 期间必须有非重入、有界的完整 audit；proxy/Control/ingress/admin/
    generation 任一变化立即 urgent revoke并停止续租。
  - **P2 bridge 兼容边界**：当前证明硬依赖 `docker-proxy`，在 userland-proxy=false 或 NAT/rootless
    Docker 会安全拒绝但不可用。最小版本先在安装/preflight 明确检测并提示所需模式，私有 runner
    覆盖 userland-proxy 开/关；不能把未支持模式误报为已支持。
  - **生产门禁**：仓库当前没有非测试代码生成完整 `control-upgrade.json/rollback_control`、创建
    maintenance proxy 或写 self-facts；本轮即使整改双 PASS，也只能证明 supervisor 原语，不能标记
    为可部署。后续须另开 production bootstrap wrapper 卡片并再次预审。
  - 两链无资费聚焦分别为 `96 passed, 1 warning` 与 `180 passed, 2 subtests passed`；这些通过项不
    覆盖上述反例。当前动作只整改本条已列 P1/P2 并补精确时序测试；整改后交回原两链复审，双 PASS
    前不跑生产 wrapper、不部署、不进行计费操作。
- `R1b-DEPLOY-2-PRE8-POST-R2-IMP/TEST`（03:53，本地整改，等待双复审）：
  - heartbeat 与 revoke 最终 CAS/失败失效共用 `_lease_lock`；revoke 写盘失败时在同锁内清 proof，
    旧 heartbeat 无法再发布 full。heartbeat 的 manifest/mode flock 均有单调总上界，停止代际写前
    二次复核；旧 heartbeat 未确认退出时 singleton 不释放。
  - recover/planned revoke 在全线路 Engine→PCSCF 锁内建立 final-admission：复用现有旧 Engine
    已认识的严格 `engine-maintenance.json`，不引入要求旧镜像识别的新文件。终态 exact records 先
    写入 supervisor-owned `maintenance-line-proof.json`，再逐线 fsync 清 marker，最后清 global fence；
    renew 只从严格 snapshot 校验 Docker/run-id，部分清理或新 proxy deny 代可幂等重建 marker。
  - planned drain 每个 lease 周期执行完整 renew audit，不再只续心跳；最终零样本后再次持全线路锁
    复核 marker、paid work 和 exact Engine，再进入与 heartbeat 串行的 CAS。
  - proxy narrow 模式仅新增三个精确 POST 终止路径（line hangup、cellular hangup、exact call
    release），仍由 Control session/CSRF/call-owner 鉴权；answer/commit/dial/SMS 继续拒绝。固定
    `Content-Length` body 转发完成后立即 return，不再等待 keep-alive close。
  - bridge 未支持 userland-proxy=false 时继续 fail-closed，但改为明确
    `proxy_bridge_userland_proxy_required` 诊断；真正的 installer/preflight 属于下一张 production
    wrapper 卡片，不能在本轮误报支持。
  - 新增精确回归：heartbeat/revoke 交错+disk full、snapshot 写失败、partial cleanup 二次 recover、
    line lock contention、跨两个 renew 周期业务稳态、drain full audit、heartbeat stop/singleton、
    narrow termination 白名单、fixed-length 0/非0/HEAD/204 keep-alive、bridge unsupported 分支。
  - 聚焦 `214 passed, 1 warning, 9 subtests passed`；全量
    `1191 passed, 1 warning, 68 subtests passed`；未部署、未拨号、未发短信。当前门禁：原两条链
    复审本增量，同时在私有 Linux runner 运行无 SIM/无计费 host-mode E2E。
- `R1b-DEPLOY-2-PRE8-POST-R3`（04:13，第三轮双链 `NEEDS CHANGES` 后已整改待最终复审）：
  - 付费链指出多线路 marker 逐个删除不是跨 Engine 原子边界：第一线开放后若第二线清理失败，旧
    recover 会 urgent revoke，可能中断刚进入的 carrier call 或使 MESSAGE 不确定。现将单线 marker
    成功删除定义为该线不可逆开放；其后任何纯清理 I/O 失败进入 `release_pending`，保持 exact full
    lease 和 Control global fence，不撤权、不重放动作。renew 或新 supervisor 从 snapshot 重建缺失
    marker、只清剩余 fence；真实两线“第二 marker unlink 失败”及新代恢复已覆盖。
  - 配置链指出 heartbeat 异常处理仍进入 revoke 的无界 manifest/mode flock，且旧 partial 测试仅
    单线。heartbeat 异常现只通过 `_lease_lock` helper 失效 proof、停止 lease_seq，由 proxy 自有 5 秒
    单调租约 deny；revoke 自身 manifest/mode flock 也改为总时限。拿不到 lease lock 时设置 stop，
    不在锁外改 proof；singleton 仍等旧 heartbeat 退出。
  - fixed-length、termination narrow、完整 drain audit、final zero→CAS 等上一轮整改保持。新增
    global unlink/目录 fsync response-loss：若当前路径已消失即视为完成，重启后旧 fence 若重现仍会
    安全阻塞并可幂等清理。
  - 私有 Linux E2E 首先暴露两个夹具/通用性问题并已收敛：脚本源码挂载路径已改正；proxy 自身份不
    再唯一依赖 Docker 默认 hostname，而是核对 Docker `--cidfile` 与 root 写入 self-facts（旧 hostname
    仅兼容）。Docker top-level Mounts 在 create-spec hash 前规范排序，消除同集合顺序噪声而不删字段。
  - 私有 runner C host-mode 真实 Docker E2E 最终 `PASS`：exact inspect/CID、8443/8000/19090 owner、
    Control TLS/plain 应用探针、deny→recover full→planned revoke→deny_applied 全部闭环；无 SIM、无
    通话、无短信。原始日志仅在私有目录。
  - 最终当前全量 `1193 passed, 1 warning, 68 subtests passed`；py_compile、`bash -n install.sh`、
    `git diff --check` 通过。当前仍等待原两条链最终复审；production wrapper 缺失继续作为下一开放项，
    本轮不得标记可部署。
- `R1b-DEPLOY-2-PRE8-POST-R4`（04:20，第四轮最小整改已测试，等待双链最终结论）：
  - 配置链上轮 P2：`recover()` grant CAS 与 `_commit_entry_fence()` 的 manifest/mode flock 仍可能
    无界。两处现全部使用单调总时限 `_bounded_flock`，分别返回 `recover_*_lock_timeout` 与
    `commit_*_lock_timeout`；recover 争用始终保持 deny、无 proof，部分释放续租时的纯 commit 锁
    争用保持 exact full/proof 和 `release_pending`，有界返回供后续重试。
  - 付费链上轮 P1：两线路部分释放后，首线可进入新的 carrier active call；旧 renew 再要求全局
    zero 会误 urgent revoke。现在只有已存在同 txid/exact-generation snapshot 且部分 marker 已清的
    清理阶段使用 `require_zero=False`；首次 recover、planned drain 仍严格要求 zero，任何 Engine
    generation/health 改变仍立即撤权。新增“两次 active-call renew 不撤权、随后 generation 改变必须
    撤权”回归，未发起真实通话。
  - 聚焦 `221 passed, 1 warning, 9 subtests passed`；全量
    `1198 passed, 1 warning, 68 subtests passed`；py_compile、`bash -n install.sh`、
    `git diff --check` 均通过。私有 runner C host-mode Docker E2E 再次 `PASS`，覆盖 exact inspect/CID、
    三入口 owner、Control TLS/plain 应用探针、deny→full→deny_applied；无 SIM、通话或短信。
  - 当前门禁：原配置/生命周期链与付费/挂断链正在只读终审；双 `PASS` 前不冻结 PRE8，不打开
    production bootstrap wrapper，更不部署。
- `R1b-DEPLOY-2-PRE8-POST-R4-REVIEW/R5-PRE`（04:27，单链 PASS、单链 NEEDS CHANGES）：
  - 付费/挂断链 `PASS`：确认 partial release 后的新活跃通话不会再要求 zero 或误撤权；真实
    Engine generation 改变仍 fail-closed；精确 hangup/release、BYE/CANCEL 与“不重放付费动作”
    边界无回退。
  - 配置/生命周期链复现 P2：`renew()` 捕获局部 proof 后，`_commit_entry_fence()` 未与
    `_lease_lock`、`stop_event`、`_active_generation` 线性化。若 heartbeat 在 commit 等锁期间失效
    proof，或 stop 已发生，旧 commit 仍可能清 marker/global fence 并误发布 full；line、manifest、
    mode 各自重置 timeout 还可能累计超过代理 5 秒 lease。
  - 拟议最小方案已交回两链实施前确认：统一绝对 deadline；重型 proof 后仍持线路 admission 锁，
    再按 `_lease_lock → manifest → mode` 做快速二次 CAS；只有同 proof/同 generation/未 stop 才允许
    不可逆 unlink；stop 与成功发布也通过 lease generation 线性化。双链确认锁序与付费边界前不改
    产品代码。
- `R1b-DEPLOY-2-PRE8-POST-R5-IMP/TEST`（04:35，双链预审边界内实施，等待最终复审）：
  - 新增独立 `_lease_generation` 与一次性 recovery claim。heartbeat 只替换 Proof/推进 lease_seq，不
    改 generation；recover、proof invalidation、revoke 与 stop 才推进 token，避免合法 heartbeat 使
    commit 永久饥饿，也阻止已经失效的局部 Proof 继续清 fence。
  - commit 的 line/manifest/mode/lease 全部共享同一绝对 deadline；重型 phase-1 释放 manifest/mode
    后，最终严格按 `line → lease → manifest → mode`，再次核对 exact proxy、Control、全部 Engine
    container/StartedAt/PID/RestartCount/run-id、ready/entry 与 network facts。deadline、token、stop
    intent 或 owner 任一不符均在 unlink 前失败。
  - heartbeat 失败现在在释放 lease lock 前先推进 invalidation generation，消除“heartbeat 已失败但
    handler 尚未来得及失效 proof”的窗口。stop 先发布 intent/唤醒循环，再有界取得 lease lock推进
    generation；commit 在每个不可逆 unlink 前复核 stop/token。renew 成功状态也在 lease lock 内复核。
  - 新增三条精确回归：同 generation 的 heartbeat Proof 刷新不饿死 partial cleanup；commit 等待
    line lock 时 heartbeat invalidation 或 stop intent 先发生，均不再 unlink 剩余 marker/global fence，
    不发布 post-stop full。既有 partial active-call→连续 renew→真实 generation change fail-closed 保持。
  - supervisor 专项 `36 passed`；聚焦 `224 passed, 1 warning, 9 subtests passed`；全量
    `1201 passed, 1 warning, 68 subtests passed`；py_compile、`bash -n install.sh`、
    `git diff --check` 均通过。私有 runner C host-mode Docker E2E 再次 `PASS`；无 SIM、通话或短信。
    当前门禁仍为双链最终复审；production wrapper 与部署均未打开。
- `R1b-DEPLOY-2-PRE8-POST-R5-REVIEW/R6-PRE`（04:48，配置链 PASS、付费链 NEEDS CHANGES）：
  - 配置/生命周期链 `PASS`：generation token、一次性 recovery claim、两阶段 exact proof、stop intent、
    共享锁 deadline 与 `line → lease → manifest → mode` 均关闭上轮 P2，无剩余可复现 P1/P2。
  - 付费链复现一个新 P1：final commit 在持 `_lease_lock` 时调用 `_durable_unlink()`；该函数先
    `os.unlink` 再做无界 directory fsync。若 fsync 跨过 proxy 5 秒 lease，per-line marker 已不可见，
    global fence 又只有 Control 识别；旧 Engine 的 carrier INVITE/MESSAGE 可越过 marker，已有 full
    WSS 仍可能接听。无资费注入复现为 deadline 已超时、marker 消失、global fence 尚在。
  - 当前尚未实施下一改动。双链正预审两个边界：A 将 namespace unlink 与 durability fsync 分段，
    final lease 区只做可见 unlink、锁外 fsync（崩溃只会使 marker 重现而 fail-closed）；B 增加 Engine
    可识别的第二全局 guard，但会触及此前“rollback 旧 Engine 兼容”与 production wrapper preflight。
    方案未确认前禁止凭直觉修改、部署或计费验证。
- `R1b-DEPLOY-2-PRE8-R6-C-PRE`（04:51，严格方案双链预审完成，尚未实施）：
  - 双链共同否决 marker-only A/A′/B 作为严格证明：拆出 directory fsync 只能缩小已复现窗口，
    `os.unlink` 本身仍可能跨 lease；超时后重建无法撤销已接纳的 INVITE/MESSAGE。新增一个存在型
    global guard 只把同一问题推迟到最后一次 unlink，旧 rollback Engine 也不认识。
  - 采用严格 C：每个新版 Engine 运行默认 DENY 的本地 monotonic positive-admission gate；authority
    严格绑定 iid、Engine container/image/StartedAt/run-id generation digest、txid/manifest、proxy process
    boot、supervisor boot、epoch 与严格递增 lease_seq。首次 seq 只 warmup，同代观察到递增后才允许；
    重复/倒退/损坏/socket stall/watcher crash/约 3 秒 TTL 过期全部 DENY，TTL 严格短于 proxy 5 秒。
  - 不能只靠 dialplan/AGI：pinned Asterisk 的 incoming MESSAGE 在进入 dialplan 前可能已经 202/入队。
    最小实现必须含 Engine Unix-socket daemon 与小型 Asterisk C client/hook，在 MT MESSAGE 返回 202 和
    queue 前 fail-closed；carrier/browser initial INVITE、media canary、MO MESSAGE 在首个副作用/最终
    carrier submit 前检查。BYE/CANCEL/re-INVITE/UPDATE/ACK/PRACK、精确 hangup/release、已有 paid
    lease/termination 与 `h` handler 永不拦截；自动初始/周期 REGISTER 可继续，显式 REGISTER/reload
    仍属新动作并 gate。
  - production wrapper 不再是 PRE8 之后可选的独立卡片，而是严格 C 的前置迁移步骤：旧 marker 与
    zero-call/MESSAGE barrier 始终保留；逐线 start_absent 到 immutable gate-capable target digest，证明
    exact Docker/run-id、gate ready/protocol 和本地 expiry canary；所有线取得 exact lease/ack 后才能清
    marker。legacy Engine 不支持 gate 时只能 fenced/manual，绝不能为可用性清 marker或回放动作。
  - maintenance 完成并且 namespace/directory fsync 与 manifest 全部 durable 后，永久 host orchestrator
    按 exact Engine generation 签发 `normal_committed`；临时 proxy 可撤销。Engine/gate 重启默认 DENY，
    由永久 orchestrator 重新 exact inspect 后签发 normal。entrypoint 必须共同监督 gate/Asterisk，gate
    退出即有界停止 Asterisk并使容器非零重启。
  - 核心无资费验收：slow unlink 跨 Engine TTL 后，即使 marker 已消失，合成 INVITE/MESSAGE 仍在
    Progress/202/queue/log/notify/Dial/MessageSend 前拒绝；既有模拟通话仍可 BYE/精确挂断。另覆盖
    schema/peer credentials/nonce/replay/clock jump/gate crash、同 container restart 新 run-id、多线路
    隔离、normal handoff、wrapper 每阶段 crash/response-loss，禁止真实拨号或短信。
  - 当前游标：先实现/测试 Engine gate 协议与 pinned Asterisk pre-202 hook，再实现 supervisor per-line
    lease 和 production wrapper phase machine；每段仍须原双链复审。严格 C 未完成前 PRE8 不得冻结、
    构建部署或宣称可用。

## 2026-08-23 macOS raw-USB Agent 数据/VPCD 抢占修复

- `MAC-RAWUSB-DATA-SIM-GUARD-45644CB8`（18:24 部署完成，评审/复审均 PASS）：
  - 背景：10.44.0.25 macOS GUI Agent 在 EC20 private raw-USB 数据模式下，`cellular_active`
    短暂清空或 companion 重启窗口会让普通 AT/VPCD/SMS 状态探测重新抢占 SIM lane，实机表现为
    周期性 `Early UICC check`、`Bridge online; forwarding AT+CSIM APDUs`、`RPC sms.list failed:
    modem is not connected` 循环。
  - 预审/复审：`network_mac_pre_review` 多轮指出必须使用 sticky release proof、bootstrap
    `DATA_DISABLE` proof、区分 bootstrap connect 与 VPCD/status guard、owner-side probe 一次性授权，
    并禁止 paid recovered/armed reconnect 触发 Early UICC/CFUN；最终 PASS。`prod_wrapper_paid_pre`
    要求 bootstrap `DATA_DISABLE` 与 paid-call cleanup 优先级隔离，最终 PASS。
  - 实际修改：仅 `agent/modem_agent.py`、`tests/test_modem_agent.py`。新增
    `_private_data_release_proven` 与 `_private_data_sim_guard_active()`；private raw-USB 未完成 desired
    reconciliation、数据 active、link starting/connecting/up、或未有成功 explicit disable proof 时，
    普通 VPCD/status/SMS/call starter 均 fail-closed/paused。启动先通过 companion `DATA_DISABLE`
    获取 bootstrap release proof；只允许基础 connect 上线 Control，VPCD 与普通状态探测仍等 desired
    reconciliation。`cellular.ensure` 的 private registration probe 改为一次性 owner-operation 授权，
    link up/starting/connecting 不 raw-AT probe。paid armed/safety hold 时跳过 bootstrap
    `DATA_DISABLE`；paid reconnect 调用 `connect(allow_uicc_maintenance=False)`，跳过 Early UICC、
    UICC repair、call-audio preparation/probe、voice-registration repair 与 `AT+CFUN?`/`AT+CFUN=1`
    maintenance 路径；保留基础 identity 与 `AT+CLCC` 能力以便 `call.status`/`call.hangup` cleanup。
  - 本地验证：`python3 -m py_compile agent/modem_agent.py tests/test_modem_agent.py` PASS；
    `python3 -m pytest -q tests/test_modem_agent.py -q` PASS；
    `python3 -m pytest -q tests/test_modem_agent.py tests/test_agent_management.py
    tests/test_macos_isolation.py tests/test_cellular_backend.py -q` PASS。
  - 构建：外置盘目录
    `/Volumes/micron512g/tmp-project/codex-audit-tmp/mdd-macos-agent-release-20260823T182150+0800`；
    package `/output/mdd-agent-macos-arm64`；digest
    `45644cb876ff961500802a73f321043e14fe257321d8bf65ace74fbc0cfb002a`；manifest verify、codesign
    verify、CLI `--help`、arm64 Mach-O 检查均 PASS。
  - 部署：server `root@10.44.0.23` 记录目录
    `/opt/mdd-gateway/data/deploy-records/codex-20260823T182249+0800-mac-agent-45644cb8`；
    Control allowlist 已更新为同 digest，`MDD_REUSE_CONTROL_IMAGE=1 ./install.sh reload --mode docker
    --no-engines`，未重建 engine。Mac `fanli@10.44.0.25` 记录目录
    `/Users/fanli/Library/Application Support/MDD Agent/deploy-records/20260823T182424+0800`；
    新 GUI PID `20453`，EC20 `862547055201716`/ICCID `89852312388530152529` 在线。
  - 实机无资费验证：10.44.0.25 新进程自 `2026-08-23 18:24:39` 启动后统计：`Early UICC check`
    仅启动阶段 1 次，`Bridge online; forwarding AT+CSIM APDUs` 0，`RPC sms.list failed` 0；
    自 `18:24:53` 起上述三项和 `Connected to usb:2c7c` 均为 0，说明周期性抢占循环停止。server
    `/opt/mdd-gateway/data/orchestrator/remote-modems.json` 已登记该 SIM `online=True`、
    digest `45644cb876ff961500802a73f321043e14fe257321d8bf65ace74fbc0cfb002a`、data `connected`。
    目标 Mac 新进程日志中 `ATD`、`ATA`、`AT+CMGS`、`AT+CUSD`、`Originate`、`MessageSend`
    计数均为 0；server 最近 10 分钟 Control/engine 日志同类敏感动作 grep 为空；目标 Mac 无
    `paid-call-*.json` marker；server 当前仅 `mdd-sim-gateway-control` 与 `engine-1` 运行，engine-1
    为 `0 active channels / 0 active calls`。

## 证据与复审记录规则

每项完成必须在对应条目下追加：

1. 预审会话结论与必须边界；
2. 实际修改文件和行为变化（不能只写“已修复”）；
3. 自动化测试命令与精确结果；
4. 实现后两条独立复审结论及已处理的误报；
5. 部署目标、产物/源码 hash、容器或 Agent generation；
6. 无资费实机证据；若需收费/人工动作，明确保留为未验收，绝不自动勾选。

## 下一步（唯一允许的继续入口）

1. 不得执行或重提 `same-address P-CSCF marker`、原 SIGTERM bootstrap B、或让 Engine 在线只靠
   pause+采样的 B2。不得再主张 bootstrap 必须无停机；两条评审链已证明在不注入旧进程代码的
   前提下该要求与 carrier inbound 安全不相容。
2. 上述 durable maintenance/admission/MESSAGE watcher/exact-generation/proxy/付费与取消原语均已
   实现、测试并双复审，现已冻结，禁止重复实施。唯一下一项是 PRE8 常驻 host supervisor：复用
   manifest→mode 锁序，证明 exact proxy/old-Control Docker generation、alternate socket owner、
   ready/health/process boot 一致；只由 supervisor CAS 授权 full，撤权等待 `deny_applied` 后才改变
   manifest。任何证明未知、超时或持久化失败都保持 deny/manual-required。
3. supervisor 实现完成后必须由原两条链双复审。部署时再次跑
   active-call/open-paid-lease 门禁，只允许 exact-generation、非 force、有界失败关闭，并做无资费
   UK/FR 稳定观察。
4. R1b 达到 `已部署/无资费实机验证` 后才进入 R5；不得重开 R1b 其他已通过评审，也不得跳回
   已闭环的多入口媒体方案。
