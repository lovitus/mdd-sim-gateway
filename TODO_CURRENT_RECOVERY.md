# 当前恢复任务：唯一执行游标

更新：2026-08-27。媒体容错 `57ada662fbc45d08ae12b075b7b88ceba69a7b1e` 已部署；勿重放部署。
不要把 Registered、能力旗标、模拟 PASS 或镜像哈希当成通话健康；也不要重放已完成部署。

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

### 下一批已完成复审，待构建部署

双预审通过Control-only最小恢复：仅已提交call、原浏览器/PCM仍活、原Agent/Modem身份一致，
才可在原lastHealthy+10秒内等待控制连接重连。新SID须经原lease真实续租及前后Attachment
CAS确认，不自动等价旧owner，不重拨或重开音频。挂断/终态查询复用身份门，禁止误挂同ICCID
的新设备或用其idle清旧租约；不升级Agent、不改DB表结构或前端。
同批修复已实证的恢复任务异常：真实Attachment没有online属性，改用is_online()；旧代码
3RED/1PASS，修后相关153项＋19子测试通过。最终16文件606项＋45子测试通过，独立复审
291项＋25子测试通过，无本批P0/P1阻断。关闭RAM owner被持久恢复循环绕过身份门的问题
已同批闭合；异设备不再收到其hangup或供给终态证据。上述仍不是线上57已部署的行为。
下一次实拨必须以新冻结源码和具体修复/诊断理由建立独立批次，保留旧失败，不重置旧标记。
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
`TODO_ACTIVE_RECOVERY.md`及历史提交只作历史，不执行其旧“下一步”。
Runner D持久代理／传输、E3/E4媒体、30c/716/E6部署已有记录，禁止因压缩再次重做。
