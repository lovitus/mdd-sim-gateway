# 已识别但不并入当前通话修复的边界

## 2026-09-17 分支与工作区处置

- 本批产品修复a10d970已进入main，正式CI35197412063成功。Core/helper/Linux Agent已部署；
  Provider仍3ff3866，未为标签对齐重启。EC20恢复AT ready、真实MM bearer connected及guard protected。
  单次精确bearer断线后自动恢复为新的MM对象/承载，Agent及业务PID不变、用户desired不变，
  宿主主路由表无WWAN路由；不是只看缓存的connected。未发送通话、SMS或测试业务流量。
  后述forced-close/缓存connected故障已在本批处理，不再当未修条目；最初HUP触发者仍无历史证据。
- 本地和远端开发分支已收敛为main，无stash；22提交旧incident和遗留未提交文件保留可验证归档。
  另有5759个被ignore的旧Python环境/字节码、Docker镜像和旧Agent构建产物，约1.32GB，
  已逐文件哈希核对后移出工作区。当前node_modules、AGENTS与仅本地游标保留，不是待合并代码。
- 本批尚缺实际浏览器页面验收：浏览器工具因无法验证管理员策略拒绝访问，两次均未获准。
  未绕过安全检查、未把CI适配器测试或API读回冒充浏览器点击；只读preview已全部关闭。

- 已审查review分支的修复随PR #1进入开发分支；旧forward-runtime分支已被包含。
- incident/vpcd-multislot-2633d7e的22个旧提交按变更文件和行为核对，处置矩阵见
  webui/src/mdd/UPSTREAM.md。旧Python/VPCD架构不再合并；Android统一协议适配仍按下文未完成项保留。
- 旧macOS Python构建脚本的缓存优化已由go-runtime/release/build-macos-agent.sh覆盖，
  冗余脚本退役。ML307独立脚本、Claude.md和本地Mach-O产物归档后移出工作区；
  ML307脚本未经硬件验收，其默认采集会写CMGF/CSCS，不作为当前Agent旁路工具发布。
- 唯一恢复游标保留本地并停止Git跟踪；私有Git bundle/原始遗留文件归档及哈希由该游标引用。
  此处置不声称所有硬件功能已验收，也不取消原有Android产品范围。

## 当前有效边界（2026-09-16）

- 2026-09-17 PR #1合入3ff3866，正式CI35178365134（要求macOS原Team签名）成功，
  Core/provider-apply/7Provider已部署；原20反例配对通过属于模拟证据，不是付费硬件复测。
  现场验收新增未闭环：服务器EC20的MDD API曾报connected/guard protected，但直接MM读回
  Bearer141 Connected=no、9/16 21:36已断开（esm-sync-up-with-nw）；dataFact缓存仍报connected
  是代码缺陷，不是数据在线证据。MM Command(AT)另报ttyUSB2 forced-close，Provider升级前已有。
  MM stderr被丢弃，未取得最初强制关闭事件，不能归咎物理损坏；未改开关或重启掩盖故障。
  用户所指headless Mac为旧.171→10.44.0.30，leaf-mac-shadow-b54本次在线，版本不变。
  此前把另一历史Agent(.25/.162)的缺席称为headless故障是归属错误，撤销该故障结论。
  下方历史headless称呼必须结合账号/Agent ID辨认，不覆盖用户最新指代；具体证据见唯一游标。

- 复审发现嵌套upstream的SWu/IKE测试曾未纳入CI，旧CI成功不证明这些包自身测试通过。
  f8a8c33/CI35107884599已直接执行engine/swu/...、runtimehost/...的race/count=1与vet并通过，
  修正了旧AES-256拒绝测试与现有128/256提议不一致的输入，生产算法/校验未改，未再部署。
  AUTH/换钥回归现在有直接PASS证据；但持续真实流量中丢失换钥/退休响应的集成场景仍未验。
  保持生产双周期0，不以此追加付费测试。原生不可取消调用的资源所有权限制仍按下文保留。

- 80e2642已接初始IKE_AUTH同密文重传，完整认证模拟每轮首次响应丢失仍仅一次SIM AKA；
  CI35097269019通过并上线Core/provider-apply/Provider。此证据不是历史FreeFR超时的唯一归因。
- 宿主启动策略：MM active/disabled，Agent active/linked且无multi-user启动链接；9/9验证曾
  显式恢复原disabled策略，guard仍enabled并在MM/NM/Agent前。页面已精确解释而未消除告警；
  未启用服务，也没有新reboot授权，不能宣称开机自动恢复已验收。
- 本轮关闭了真实部署遗漏：旧provider-apply解不出新catalog字段，provider-config曾503。
  已协调升级并核验接口/进程hash。今后Core目录契约变更须同批升级helper，不放宽字段校验。

- 58bf4ef事务匹配与ce941f5 peer INFORMATIONAL均已CI/部署，不再列为未接线。
  ce941f5复用上游加解密/解析，搬入旧DPD/原文缓存/设备身份/CFG_REPLY及空闲后P-CSCF重绑/
  已安装SPI删除处理。真实运营商主动事件尚未捕获，不能据正常注册声称这些硬件事件已验。
  2026-09-16来源纠正：旧state_epdg_create_sa并未实现对端IKE原位接纳；ESP接纳受默认关闭的
  SWU_ACCEPT_EPDG_ESP_REKEY门控，不能把实验分支误记为默认生产能力。8e15cb7已恢复旧默认
  分类：额外承载NO_ADDITIONAL_SAS，未知ESP SPI为INVALID_SPI，已知ESP/IKE为NO_PROPOSAL_CHOSEN；
  不更新密钥，不干扰现有SA。CI35082827628成功并部署Provider。
  主动CHILD的PFS/精确重传/安装后删除/五秒宽限，与主动IKE新密钥/旧会话退休/CHILD继承/
  计数重置/独立定时器已在66220c6组合上线，CI35091880670成功。页面独立保存两个周期，
  当前均0，不自行启用；正常注册不是硬件换钥验收。peer SKF分片和网络backoff仍未闭环。
  新现场证据：FreeFR本批首次SWu超时后自动恢复（无手动重启）；设置页services_mismatch
  对应MM运行但自启disabled，需核对持久隔离策略及回执，不能靠enable或重启掩盖。
  可复用来源：原MDD ec620942；VoCat最新39b8a53（已fetch，仅参考，其ike目录无完整rekey）；
  VoHive35ba2a2与固定vowifi-go1e9c6e6a。实验ESP选项是否有旧用户启用证据仍未知。

- 2026-09-16 review复核与关闭：报告基于60ff4e2，069a2c9时确认七项仍存在；
  现由11fdd72整批修复，CI35070070711成功并部署Core、7Provider和5Agent，不再当未修队列。
  优先级P1：agentlink/server.go在TokenForAgent和add之间未与DisconnectAgent协调撤销；
  adminauth/manager.go Login哈希计算后未核对密码版本，并无计算前并发额度；
  providers/vowifi-go/internal/service/operation_store.go paid SMS仍以进程generation作去重命名空间。
  P2：Backend.Register未计入BeginDrain的in-flight；agentlink/client.go断连仍等待未取消的worker；
  Backend.Start未拒绝failed但仍保有runtime（Core清理路径有缓解，不能说所有自动恢复都会触发）。
  已补真实HTTP撤销握手、受控凭据交错/哈希限额、跨generation重开Bolt（成功/失败/未知/
  旧记录/换卡/改正文）、注册维护互斥和失败清理所有权回归；未在生产复现攻击或重复计费。
  旧无SIM身份SMS记录保留原文并转未知tombstone，不允许以新op绕过去重重发。
  有效残余：断连已取消context-aware worker；真正不理会context的原生驱动仍须等待退出，
  不能直接抛弃goroutine/释放其硬件来伪造重连。若现场证明该路径阻断，再处理其独立生命周期。

- Linux IP/接口/实际APN及接口累计计数已在d20e25a接线，生产读回已验；未知不作0。
  原下方“尚未上报”已关闭，不能据旧条目重复开发。
- 原.171历史topology_invalid具体字段仍未知；已有错误保留、局部隔离及有效观测恢复，
  没有证明历史根因根治，也没有自动强制重启卡片/进程。后续必须保护活跃业务与隔离意图。
- Windows .211与headless Mac新版本元数据已覆盖；headless在9986b4d纳入同用户launchd
  CLI托管并网页软重启实测通过，配置不变；不是root系统daemon，仍需要用户登录域。
  自动元数据重读最多3次已通过CI并部署两Mac；不等于自动强制硬件/整机重启。
- 借用流量端到端实测由用户暂缓；不主动发送收费流量。音质/余额等保持既定用户限制。
- 两Windows磁盘warning87/88%是实际阈值状态，显示剩余容量，不自动删除用户文件。
- 其它早期未验边界（独立物理Linux、完整恢复演练等）继续保留，以唯一游标/后续证据判定。

## 历史条目（非当前执行队列）

以下保留决策沿革，可能已被后文或唯一游标覆盖，不得仅凭历史“未完成”重跑验收。

- 2026-09-11现场恢复后的明确残余：Linux已真实连接且连接中APN读回通过，但IP/接口
  尚未跨Agent协议上报，页面仍显示等待；后续协议展示批次接入真实值，不能伪造。
  .171两卡此前同时消失已定位至本机topology_invalid阻断WSS上报，物理reader/ATR仍在。
  旧进程具体非法字段未被保存，换签名进程后恢复；已补受认证诊断固定规则detail。
  复发应先读取该detail，不重复盲重启、不改PIN/profile；不宣称历史触发字段已彻底定位。

- 2026-09-10安装器边界：releasebundle.copyFile使用OpenFile(mode)但未显式恢复清单权限，
  调用方umask077可让复制后的可执行文件权限收窄，导致完整性校验拒绝（未切换current）。
  本次私有备份保持077，工件安装采用022后同工件成功；后续完整安装器批次补权限独立性
  回归，不为此再发一个微版本，不能删除或放宽manifest权限校验。证据见唯一游标。

- 用户最新决定：跳过CN SIM的VoWiFi验收，CN SIM不开放VoWiFi；其他国家SIM仍可使用
  CN出口。后文“缺CN出口导致该CN测试卡无法验收”只作历史，不能继续作为当前阻塞。
  Linux服务器现已接入原.171 EC20：同机、跨机中继WSS及断链/Agent停止隔离已验，不能
  再称Linux缺modem。独立物理远端Linux主机、整机重启及开启蜂窝数据不在本轮已验证范围。

- 2026-09-11已关闭（ed5f702，CI与生产浏览器/API通过）：下述能力元数据矛盾已移除，
  订阅原值未变，禁用事件显示原因。此前问题：go-runtime/internal/notifications/http.go的configView已将
  line_unrecoverable列入SupportedEvents，但UnsupportedReasons仍声称不存在terminal状态，
  与已接入的恢复outbox矛盾。实际原通知页按supported_events渲染，该开关可配置，未受此
  旧字段阻断；现已随运行事实展示整批完成互斥契约回归，不为一行单独发布，
  不自动开启用户未选中的事件，不触发真实通知来证明这个元数据修正。

- 2026-09-10新modem首次自动配卡：已真实通过自动claim/EF_AD/readback/provision，非手工
  草稿；Provider/IMS上线另有条件，当前无CN出口，候选无新增项，被单条新增门禁拒绝，
  不得把配卡通过当成VoWiFi已上线。浏览器只显示runtime_intent_uninitialized，没有把
  缺国家出口与default_apply失败解释给用户；后续同批状态展示收口应接入已有操作回执和
  出口事实，不重做配卡、不为文案单独发版，不伪造CN节点来通过验收。证据见唯一游标。

- 2026-09-10官方测试profile兼容性：所选空白eUICC向Google测试SM-DP+认证实返8.8.2/3.1，
  对方不支持该卡CI公钥，未安装profile。等待兼容此卡的测试源或GSMA测试证书卡；不重复
  同源激活码、不绕过证书，也不得删除现有业务或名称含TEST的旧profile。证据见唯一游标。

- 2026-09-09实测恢复迟缓：直接SS入口故障切换在KpAECM已自动A→B并IMS ready；恢复原
  配置虽runtime_confirmed，giffgaff随后仍需一次正式runtime/start才恢复。另两次Core升级
  后FreeFR短暂userspace_stack_failed/SWu失败，现有恢复器最终自行恢复，未重启Provider。
  保留上述区别，不把切换成功等同所有恢复路径无人工介入。不再通过重复DROP/拨号制造
  证据；后续稳定性批次沿既有日志/失败身份定位，不能据此扩展新前端或再造恢复控制器。

- 用户最新决定：目前没有新的SIM用于下载，eSIM实际下载验收延后，保留实现，不作为
  当前主线阻塞。giffgaff已获准用于启停和故障切换验收；英国仅一个真实节点，可使用
  明确标记的故障注入封装，不能冒称两个独立英国出口或真实冗余能力。测试后恢复配置。

- 2026-09-09用户决定：自动通话验收只保证连通性，访问端/服务端采样大致相符即可。
  波形破音、削波、抖动、播放欠载及主观音质留到开发完成后由用户手工验证，不再自动
  拨号、分析或扩展工具，不阻断原功能开发。已有U8Bdua音频/时序证据保留；近满幅与
  到达间隔仅为观测，不作已确认产品故障，也不为此自动更改音频增益/缓冲算法。

- 2026-09-08客户决定：余额与套餐流量余量查询不纳入当前开发/主动验收。已有实现保留，
  不移除、不继续补运营商规则或主动验证；仅客户明确要求手动验证时再做。此前此项缺口
  记录留作历史，不作为前端主任务的阻塞或完成门禁。

- 2026-09-08最新goal要求前端批量适配优先：暂停继续扩展未提交的Agent新安装标志、发现记录、
  模板签发/下发及模式选择等通用机制。代码原样保留，不因暂停盲退；只有原前端实际操作确需的
  最小后端补全才继续接线。优先完成原页面现有动作，非阻断边界留本清单，不再堆基础机制。

- 2026-09-08用户再次收紧顺序：原魔改MDD全部功能/页面优先。新平台能力扩展、未适配modem
  自动穿透等若不是恢复ec620942原主流程所必需，均排在原功能闭环之后；此前Windows/macOS/
  Linux/Android范围说明保留为产品目标，不当作现在扩展的指令。现有接管/隔离机制必须保持。
  当前未提交模式选择/发现/默认意图代码保留，不盲目回退；仅将原新设备默认与自动配卡真正
  需要的部分纳入原流程恢复，不继续借此扩展跨平台体系。不得把延期等同取消或已经完成。

- 2026-09-08升级联网搬运：原auto候选可能借用cellular_sim出口下载大型发布包。当前卡均漫游，
  此路径不自动继承为收费授权；暂不纳入升级自动回退，也不自动建立数据会话。
  非计费代理路径先完整接线，流量SIM升级须明确费用策略后再开放，不把这项限制算作原功能全量完成。

- 交接安全提醒：早先一次生产网络页无障碍快照含代理认证连接串。未写入提交或在交接中复述，
  但应由用户轮换相关代理凭据；本任务未获授权自动修改代理服务器凭据。

- 2026-09-08：现有Windows安装器Wait-State/Wait-AgentExit仍使用250ms轮询，本次服务切换沿用了
  该入口；与当前全局低频等待约束不一致。后续部署不得继续沿用高频实现，应在同批安装器验证中
  改成有界阻塞/低频等待；不为工具整改重复已完成升级。Mac安装器同类1秒循环亦须一并处理。

- 2026-09-08更正：eSIM默认SM-DP+/剩余NVM的Go字段、只读查询和原页面适配已在6aca739交付；
  Windows读卡器与.25/.162 headless Mac已在生产页面读回399.26 KiB和465.7 KiB。
  这项代码/上述设备读回不再列为缺失，不重复搬运；.171签名App仍未升级，真实下载等写入验收仍未完成。
  具体部署及证据沿用TODO_CURRENT_RECOVERY.md引用，不恢复Python、不为补证据操作profile。

- 2026-09-07依赖审查：npm audit报告browserslist<=4.28.6的GHSA-c83g-rgw3-j3cx与
  GHSA-73wf-gq98-2v4g。npm ls --omit=dev browserslist为空，当前Go+静态资源产物不包含该
  构建期依赖；未按运行时漏洞阻断已验证产物部署。后续依赖批次升级并复跑CI，不盲目audit fix。

- 2026-09-07全功能审计：系统告警已有Go采集与durable transition通知，不得重写；但页面未显示
  alerts、旧确认/持续恢复门限及部分旧指标尚缺。已提升为当前完整迁移对照批次，具体来源和差异
  见唯一游标，不制造生产故障或发送通知来验证。

- 2026-09-07：备份活跃bbolt直接拷贝遗漏已修为事务快照；真实events快照超过32MiB导致的503
  经1600b83预算/诊断修复关闭。真实ZIP下载及8源哈希通过；该生产副本的bbolt重开与完整恢复演练
  仍未验，不上传私有数据库到CI或本地绕过构建约束。完整证据/哈希在唯一游标，不重复下载。

- 2026-09-07全功能核对：网页系统更新入口未挂载，App仅剩不可直接使用的legacy UpdateModal；
  Go updater/check/progress/apply已实现但不等同网页主流程可用。通话历史另有全局100条后前端筛线
  的语义退化（旧版每线100条），两项已升为当前完整收口批次；证据和安全边界见唯一游标。

- 2026-09-07关闭：线路历史可用性与短信全会话/稳定分页漏迁移已在75cf20a交付，移动内容裁切
  在a1f32d9修复并真实390px/1280px复验通过。两次完整CI/生产版本与证据见唯一游标；
  少量生产短信不冒充大量历史分页验收。既有浏览器语音标题在390px仍偏窄，留统一视觉收口，
  不为单个标题微发布，不阻断这两条历史主流程。

- 2026-09-07全功能审计：短信会话列表当前由最近100条line+transport记录聚合，旧版list_threads
  则查询整条线路所有peer；较早会话会消失，属实际迁移缺口，不是仅UI分页优化。需整批恢复
  全会话列表、按peer历史查询/稳定翻页及既有删除门禁，不能只提高limit。证据见当前唯一游标。

- 2026-09-07全功能审计：发现旧线路历史可用性真实漏迁移（旧store.py timeline/summary及
  VowifiHistory组件有完整实现，当前页面仅提示未来恢复）。已提升为当前主任务，不再作为
  不阻断收尾的P2延期。浏览器录音核对为旧callCoordinator false/null stub，不伪称丢失实现。

- 2026-09-07 用户最新顺序：eSIM profile 删除定制放到全部其他功能重构与移植完成之后，
  届时必须与用户交互式开发和验证。当前只保留来源研究，不自动实现或验卡；旧条目中的
  “收尾后实施”不得解释为现在就开始。先完成全功能有限清单核对，不能用源码清退代替确认。

- 2026-09-07 来源核对修正：此前“portable未找到关键词/实现”不准确。已实读
  `mdd-portable/portable/store/soft_delete.go:22` 的 `[deleted]` 应用显示名标记、完整归档/发送收据/
  ACK/重发/恢复存储状态机及对应测试。它不是卡上nickname定制的证明，且文档明确真实
  RegistrarDeleteAdapter仍未完成；不能声称发现完整旧业务链。三重确认在旧实现中属于恢复，
  本项目重发仍须按用户新要求逐次多重确认。来源哈希与迁移边界已固定在当前唯一游标。

- 2026-09-07：用户已明确授权清退备份中的 78 个退役 Python 文件，包括受保护的 Python dirty
  改动。删除前逐文件 SHA-256 与私有备份一致；备份引用见 AGENTS.md。下文历史“不得删除”和
  “待授权”记录仅保留作证据，不再阻塞此范围清退。其他受保护文件不变；真实 HIL 与 eSIM
  定制仍未验收，源码删除不代表这些工作完成。

- 2026-09-07（用户明确要求最终重做，当前全量迁移收尾后实施）：保留 MDD 私有 eSIM registrar-ACK
  soft-delete 语义。MDD 永不调用 ES10c.DeleteProfile，profile 物理保留在 eUICC；用户若确需物理删除须用
  其他项目。soft-delete 必须持久保存完整可重发的 delete notification/event 与每次发送结果；只允许用户
  每次多重明确确认后重发，成功、失败或结果不明均不得自动重试、自动移除或丢失可重发材料。被软删除的
  profile 在 MDD 内禁止 enable/switch；页面提示先手工从 profile nickname 删除旧实现约定的关键词，
  MDD 不自动改名或启用。已查当前/全 Git 历史、原始 2026-09-05 会话、清理前 patches、`mdd-portable`
  与公开 MDD：只找到 `mdd-portable/control/app/lpa.py:339-350` 和 `main.py:4682-4698` 明示物理删除禁令及
  “registrar-ACK adapter 尚未实现”，以及当前旧 MDD `main.py:13206-13247` 的 confirmed replay、默认
  autoremove=false、成功/失败记账；未找到 soft-delete nickname 关键词及完整适配器实现，不能猜。当前
  Go delivery 在 receiver HTTP 204 后会移除 card notification，与本定制不一致；最终实现时须拆为“仅发送
  并保留”与另行确认 remove，新增 Core durable ledger，且线路永久删除不得清除该 eSIM 恢复材料。验收只
  用隔离 fixture；真实 soft-delete/replay/rename/enable 均需用户届时明确确认，不用生产 profile 制造证据。

- 2026-09-04（2026-09-06代码闭合，HIL待真实新卡）：Go 已完成 modem `/v1/provision`、`/v1/reprovision`、
  独立readback precondition、unknown reconcile及完整operation ledger；精确Agent/process/attachment/
  equipment/CardID/SIM session、SMSC/IMEI和call/media/data/raw门禁均由Agent写前重检。2026-09-06又补齐
  reader专用首配：exact reader readback与catalog revision CAS成功后才把匹配disabled draft原子提升为
  `provisioned`，仍不启用、不Apply Provider、不启动runtime；只读结果不明会留证但不永久阻塞新会话重试。
  同批后续已补PC/SC USIM只读identity：EF_IMSI、EF_AD MNC长度和EF_SMSP进入typed reader fact及candidate，
  PIN受保护时只报`pin_required`且不自动尝试；reader provision会把fresh IMSI/MCC/MNC与saved draft精确比较。
  尚未在新插入的未配置实体SIM上走完整claim→补齐identity→reader/modem provision→enable→Apply→runtime
  HIL，不得把单元测试或已有线路状态当作该验收。

- 2026-09-04（2026-09-06安全子集闭合）：Go `/v1/sim-pin` 已有无凭据status、剩余次数>2的单次proof、
  exact Agent/process/reader或modem/CardID/SIM session门禁、secret digest operation ledger及不明结果不重试；
  页面仅暴露status→verify。PC/SC内部仍保留change/enable primitive供受控维护，但主动页面契约禁止暴露，
  因用户明确没有PUK，错误PIN会跨会话递减计数；modem同样只允许verify。自动PIN recovery继续由Agent本地
  0600配置和bbolt attempt fence拥有，PIN不进入Core catalog。尚未对FreeFR执行PIN操作，本批也不得为验收
  消耗尝试次数。

- 2026-09-02（2026-09-06代码闭合，HIL待人工）：内建 Windows／Linux／macOS Modem Prober 已统一使用 Agent 本地 SIM insertion
  generation，覆盖权威 absent/non-ready、换卡、设备/USB generation变化、AT owner重开和 probe
  unknown 后保守换代；不同卡或重新建立的设备所有权不能沿用旧 policy/profile/data请求。仍有一个
  纯轮询无法判别的真实边界：同一张 SIM 在两次采样之间拔出并插回，同时 USB、Equipment ID、ICCID
  和 AT owner均未变化。后续应分别接入 Windows MBN SIM状态通知、Linux ModemManager D-Bus SIM对象/
  状态代际及 macOS cellular helper 的 SIM hotplug epoch，再把该 epoch交给现有 tracker；不得用 TTL
  定时轮换 generation，避免长期在线卡无故失效。当前三端代码/CI已接入上述事件源，`.171`已证明
  QSIMSTAT source启用且稳定无误换代；尚未人工执行真实remove→insert，Windows/Linux也无目标，因此
  不能宣称三端采样间窗口已完成HIL，待下一次可接触硬件时每平台一次验收。

- 2026-09-02：BYE 明确返回 481 已按 RFC 3261 §15.1.1 作为 dialog 已终止处理；408 和 transport
  timeout 虽也有标准状态机语义，但当前调用边界还不能证明请求已交给 SIP client transaction，直接把所有
  timeout 当成功可能掩盖 BYE 根本未发出的停止计费风险。后续只在取得明确 transaction-stage 证据后定点
  评审；当前继续保留失败、exact guard 与有界重试，不与已实证的 481 修复混合。

- 2026-09-01：批次147蜂窝主动事件以 Agent 侧 fresh `CLCC/CMGL` 有界协调扫描作为正确性来源，
  不在同一批替换已经实机稳定的 AT transaction。`warthog618/modem` MIT v0.4.0 的单 reader／
  indication demux 可在后续仅作为低延迟 wakeup 参考；采用前必须补 `context.Context`、取消后重新同步、
  有界 indication queue/worker，并保留当前 SMS possibly-sent 与精确来电/挂断边界。URC 丢失、Agent重启
  或串口重开时仍必须靠 reconciliation 补齐，绝不能让 URC 成为唯一事实源。当前约2秒高优先级CLCC、
  全局paid-lease避让和每5秒round-robin单个3秒CMGL已覆盖主流程，不为降低几秒提示延迟冒险重写底层。

- 2026-09-01：Go Notifications 当前按每渠道固定 worker 每 500ms 扫描 delivery bucket，Coordinator
  每秒检查 catalog/allowance 和最多 500 条 reminder delivery；当前 9 条线路和有界历史足够，且没有动态
  goroutine。若线路或通知历史显著增长，改为 pending/not-before 索引、revision/event wake 和下一个日历
  deadline；不能为了性能重引入第二套调度状态机或放宽 source→destination→ack 顺序。
- 2026-09-01：通知的 test operation、source receipt 与来电 ack tombstone为防止结果不明时重复外发而
  长期保留。后续可把终态 test/event payload 压缩成最小 identity receipt，并把 call pending 与 ack receipt
  拆 bucket；只有保留相同幂等语义和旧 Core messages.db schema=1 回滚兼容后才能做。当前普通终态 delivery
  可由页面清理，敏感 event payload 在终态即清空。
- 2026-09-01：Notifications 配置 CAS 当前使用请求体 `expected_revision`，原子性等价但未与 catalog 的
  ETag/If-Match 风格统一；旧非 ISO `valid_until` 会安全跳过 reminder producer，但页面没有独立机器状态；
  uncertain 渠道测试会永久复用原 operation ID，尚无显式“放弃旧身份并创建全新测试”的危险操作入口；
  `Coordinator.Start` 也依赖生产只调用一次。以上均不阻断本批主流程，后续统一管理 API/诊断/生命周期时处理。

- 2026-08-31：Linux 受控数据借用当前只接受 ModemManager Bearer 明确返回的 static IPv4，已经覆盖
  EC20/QMI 主纵切。ModemManager 官方契约说明 DHCP bearer 还需要 DHCP client、PPP bearer 还需要 PPP
  会话，IPv6 通常还需要 SLAAC/DHCPv6；这些不能把空地址伪装成可用。后续在出现对应真实 Modem 前，
  优先复用成熟 Go DHCP/PPP/IPv6 组件，并继续保持 socket mark、非 main 路由表、先撤 permit 后断 bearer
  的同一防漏边界；当前三种方法 typed fail-closed，不阻断 static IPv4 whole-Modem 里程碑。

- 2026-08-31（2026-09-06 生产`:8443`关闭）：当前 AgentLink 的 `TokenResolver` 接口已经支持按 Agent ID 返回凭据，但现有单机
  bootstrap 配置仍把同一个 `agent_token` 发给全部受信 Agent；因此 Agent ID 只是部署身份，不是彼此
  隔离的密码学身份。raw Modem 每条 USB/IP 流已有独立、一次性、分角色 token，不能串流，但持有全局
  Agent token 的恶意终端仍可能在合法终端离线时冒充其 Agent ID。当前私人受控 Agent 范围不阻断
  Windows/Linux raw Modem 功能纵切。Go 当前批次已加入 transition→scoped 两阶段迁移、每 Agent 随机
  token、撤销 tombstone、transition-only unenroll回退、一次性秘密回显及控制/媒体/数据/USB-IP 活动会话
  失效；root helper在不放宽`/etc/mdd`权限的前提下原子持久化，未知 ID 在 scoped 模式 fail closed，
  没有使用 Agent ID 哈希或共享密钥派生。生产`:8443`三台已逐台迁移并关闭fallback，旧共享token对未知
  ID实测401。独立`:9443`validation Core的15.211 raw环境未动；它若长期保留，应在下一次raw验证批次按
  自己的信任域迁移，禁止跨用生产token。旧Core回滚会恢复共享认证语义，仍是必须明示的安全降级。

- Go VoWiFi 的用户态 IMS Security-Agree 当前只接受 UDP 和无 IPv6 extension header 的精确
  transport selector；TCP/TLS 本地绑定、IPv6 extension-header walker 以及 ESP auth/replay drop
  诊断计数，待真实运营商或诊断页面出现明确需求时再单独实现。当前均 fail closed，不回落宿主网络，
  不阻断已经覆盖的 UDP Security-Agree 主路径。

- 2026-09-06已关闭：当前Go cellular call source已持久保存Agent/process/attachment/equipment/card/SIM
  session/occurrence/native index，不再是“历史只有ICCID”。VoWiFi active call和browser media由Provider
  exact session拥有；Core重启断开代理后Provider十秒guard按exact call ID执行BYE并有失败重试，snapshot
  把durable history收敛为ended。Core不能重建旧browser subject/resume ticket，因此跨重启不恢复owner是
  安全契约，不再新增第二套durable media lease ledger。
- EC20 双向音频已移除 Control 额外 20ms pacing。若部署后的真实 50 秒通话仍出现持续破音，
  再单独评审跨浏览器/USB 音频硬件时钟的自适应 jitter/resample；本批不预先实现该复杂机制。
- 已知远程卡离线、且没有活动RAM owner时，部分旧状态入口仍可能误查本地ModemManager。
  后续统一离线设备路由识别；不得以无响应伪造idle，或用模糊历史映射指挥另一台设备。
- 2026-08-27：新增 `reconcile_orphaned_usim_recovery_fence`（治好了 iid7 Free FR 卡在
  "本地 VoWiFi Engine 未继续推进注册" 的一种具体成因：Engine 侧裸 fence 从未被
  Control 的 campaign 认领就跨代际存活）之后，怀疑这是一类更广的问题：`run/` 目录下
  还有其它"只按文件是否存在判断、不核对 engine_run_id"的产物（例如 admission 相关
  标记），理论上都可能被同一根因（Docker 自愈重启 vs Control 生命周期的双重所有权）
  绊住。本次只补了这一个已经实锤复现的具体案例，没有做全量审计。后续应通读
  `run/` 目录所有产物的读取点，逐个确认是否已按 run_id 校验新鲜度，而不是等下一次
  具体线路卡住才发现下一个实例。
- 2026-08-27：远程 VPCD 读卡器（`card_agent.py` 经 WebSocket 桥接）的 WS 链路一旦
  断开，`control/app/main.py:api_vpcd_ws` 的 `finally: vpcd_registry.release(claim)`
  会立即无条件释放本地 vpcd↔pcscd 会话，即使 Agent 几秒内就重连、物理卡从未离开。
  这会让 pcscd 把纯粹的网络抖动报告成"卡被移除"，进而在恰好撞上 IKE/EAP-AKA 或 SIP
  REGISTER 的窗口时制造一次可避免的重新认证。真正的修法是让本地 vpcd 会话在一个有
  界的重连宽限期内保持存活并支持"续接"（类似 `call_media.py` 里浏览器媒体已有的
  `browser_reconnect_deadline`/resume ticket 模式），但这会直接触碰 `vpcd_slots.py`
  的 claim/release/`current_identity` 状态机——这个模块已经因为类似的细节问题反复
  出过事故，不适合顺手改。当前已用一个小得多的办法先吸收掉这类抖动：把
  `"Card was removed."` 归入 `pcsc_card_reset` 分类（`engine/ami_usim.py`），让它复用
  既有的一次性有界重注册与孤儿 fence 回收管线；未来如果这类抖动的影响面扩大到需要
  真正的会话续接，应作为一个独立、经过复审的 VPCD 会话续接批次实现，不要顺带塞进
  别的修复里。
- 2026-08-29：Linux deb/rpm/apk 包装延后。已核对 nFPM v2.47；后续包只应携带当前
  versioned release directory，并调用同一个 Go `install-release` 契约，不能在 package lifecycle
  shell hook 中复制账户、权限、链接切换、回滚或服务启停逻辑。当前可重复安装、升级和回滚已由
  纯 Go 安装器及 root-only receipt 覆盖，增加发行版包不阻断下一批 PC/SC shadow 验收。
- 2026-08-29：生产 release/receipt/Core SHA 均可精确追溯，但 `/v1/system/runtime` 的
  `build_version` 仍显示 `(devel)`。后续在统一 release 构建入口用 Go ldflags 注入提交和 release ID，
  并增加安装后契约测试；这只是展示／追溯冗余缺陷，不阻断已由 digest 与 receipt 证明的当前运行
  版本，也不应插队打断蜂窝短信主纵切。
- 2026-08-29：后续蜂窝流量借用必须先实现 Agent 对数据面的独占接管，并采用持久化、默认拒绝的
  宿主转发／出口策略；即使 Agent 进程退出或崩溃，曾接管模块的漫游流量也不能回落给宿主、VPN、
  打洞软件或其它进程。服务端同样不得把蜂窝链路设为宿主默认出口。当前 Windows MBN 只读观测
  不具备这种独占保证，因此不得把现状宣称为防泄漏，也不得在该保护完成前启用流量借用。
- 2026-09-06 已关闭：旧独立`agent/go-agent`不参与任何build/install/workflow/runtime，且重复TOFU、
  WebSocket、identity与raw APDU边界，已连同自己的module/build脚本整包删除；CI禁止恢复。当前统一
  `go-runtime/cmd/mdd-agent`已有严格server URL/SPKI pin和IPv4/IPv6地址处理，不再延期修补旧入口。
- 2026-08-29：eUICC 通知只有在当前 delivery 明确得到服务器确认、但卡内移除失败时，页面才提供
  一次纯移除恢复；若此时浏览器/Control 同时丢失结果，当前选择保留卡内通知，不凭猜测删除或重发。
  只有真实现场反复出现这种双重故障时，再单独评审不含激活码/凭据的 durable acknowledgement
  ledger；现在没有可信触发频率，不为假设场景增加持久状态机。
- 2026-09-06 已关闭：旧`agent/android`只连接已退役VPCD API且从未进入当前workflow/release，缺失
  wrapper不是应修复的当前发布问题。该Android工程已与旧Card Agent分发面整包删除。
  2026-09-08用户纠正：Android读卡器接入仍是必须完成的产品范围，并非可选未来功能；
  旧入口清退已关闭，但统一Agent协议的Android适配与真实验收仍未完成。优先搬运现有成熟
  读卡器模块并适配统一协议，不得恢复共享token/TOFU/raw APDU旧安全边界。
- 2026-09-05：发现一批未纳入当前 Go P0 游标的 legacy Python/旧前端 dirty 工作区改动，文件
  mtime 集中在 2026-08-27 至 2026-08-28，涉及 `agent/modem_agent.py`、`agent/modem_providers.py`、
  `control/app/main.py`、`control/app/modem_registry.py` 及对应测试、macOS 构建脚本和
  `webui/dist`。`.pytest_cache/v/cache/lastfailed` 在 2026-09-05 09:44:03 记录了 13 组
  legacy pytest 失败，跨越 engine、pcscf、maintenance、agent management、wifi watchdog、
  VPCD、modem 和 remote modem devices；其中包含 `ModemAgentSafetyTests`。失败来源和触发者
  无法仅由 pytest cache 确定，也没有证据表明这些失败属于当前 Go migration。处理决定：原样保留，
  不提交、不删除、不 stash、不部署，不把失败结果伪装成 Go 回归；后续若继续处理，必须作为独立
  legacy compatibility/cutover 批次，先审查完整 diff、重现失败并完成调用链和真实 Windows 设备验收。
  在该批次开始前，任何 agent 不得修改这些文件。
  2026-09-06只读映射已完成且原文件仍未改：共享macOS build cache已由当前Go builder覆盖；禁止
  `reg_unanswered`快速替换进程已由Go Provider原位retry契约覆盖；剩余Windows data-off后按需准备
  SIM APDU/VoWiFi intent行为已迁入Go并有独立测试。该dirty diff已无未吸收产品行为，后续只剩用户明确
  授权后的历史源码归档/物理删除边界，不能再以legacy pytest失败阻断Go主流程。
