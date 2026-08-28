# 当前恢复任务：唯一执行游标

## 2026-08-28：Go 分层运行时重构（当前主任務，第三批已实现、未部署）

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

目标架构和分批验收记录在 `GO_REWRITE.md`。当前未部署、未拨号、未发短信、未改变任何生产
容器。旧 EC20/APDU 和 Control `reg_unanswered` 的未提交修改仍保留在工作树，尚未混入本批提交。

`next_action`：实现 Go Core 的原子 NDJSON event journal + snapshot 恢复（仅本地测试数据），把
recorder、reducer 和只读 API 组合成第一个可运行 `mdd-core` 进程；随后才接 legacy event bridge。
原生 Go VoWiFi provider 在 AGPL 依赖选择确认前只定义接口和用户态 netstack 边界，不引入该依赖。

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
