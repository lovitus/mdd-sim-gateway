# 当前恢复任务：唯一执行游标

更新：2026-08-26。E6 已完成正式部署；英国和香港已有实际拨号／音频／挂断证据。
不要把 Registered、能力旗标、模拟 PASS 或镜像哈希当成通话健康；也不要重放已完成部署。

## 唯一源码与现场

工作树：`/Volumes/micron512g/tmp-project/codex-audit-tmp/mdd-forward-runtime-20260824`
分支：`codex/forward-runtime-20260824`。用户原始工作树未动，没有 push。
运行源码：`e6e9379f014d621ad2107f5bd2fd0c06a8d9d6e1`；后续任务板提交仅是记录，不需要重新构建。

| 组件 | 运行产物 | 容器 |
| --- | --- | --- |
| Control | OCI `a048d01063f46bbaa22ff030b1f21de21d52000f98c47437e3142e6d529e1763` | `14708309b453adadfe2ab342289b3ff661d299ddb89abaa154385c0ff2c93d55` |
| Engine | OCI `e778b32c2f9a88b2a85207114797bcd00663f83be24e90a887d35646357b77eb` | UK1 `f79848b9b198608f78e1cde3940cc97292fd39e12c13269e43dec8a2ab5c0359`；FR7 `eeed6ca300be96ddf7b1021e3dfe664e96f83b9bdbd55f969d7fdb2d3580511e` |
| WebUI | 沿用入口修复 `index-D96mFEH8.js`，7文件清单 SHA `3986e11d…` | 未因文档或 PCM 修复改包 |

三容器回读 restart=0、unless-stopped。Control 的实际 call_media.py SHA 为
`0e0df5ab0fec9f14cb5ed30adbd526154989628e345e5934609ea7bbbce0fe43`。

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

这是正常鉴权API／WSS与真实运营商／设备的测试，不是浏览器页面点击或用户麦克风／扬声器验收，
也未证明对端人耳听见或识别语音内容。原始录音、控制帧、失败与独立复核均在私有记录。
E6后另有英国无收费WSS/Asterisk回声通过并清理，未为补统计重复收费拨号。

HK前次队列失败、媒体瞬态失败、一次客户端CSRF头拼写错误造成的403都保留，不能改写PASS。
403发生在创建通话前，0提交／无call_id；已修的是测试请求头，未把它归咎产品。

## 凭据与测试授权

管理员凭据及最新指定测试号码在Git外的私有目录，目录0700、文件0600；全局AGENTS有使用规则。
用户已长期授权这两类指定短时测试，不再重复索要；号码替换以私有授权文件最新值为准，
停用号码不再作为测试配置。每次必须控制次数、独立看门狗、实际语音数据及物理终止证据。
正常登入的CSRF头是 `X-MDD-CSRF-Token`，不得臆造；凭据不进Git、镜像或公开记录。

## E6 首轮换钥观察：已完成，不重放

2026-08-26 UTC：英国12:22:46、法国12:24:07安装新CHILD SA；分别12:22:46、12:24:08
收到经认证的旧SA DELETE ACK。法国随后仍回答对端DPD。收尾读回三容器仍为原代际、
restart=0，两个Asterisk均0通道／0通话；无新增IKE初始化、未见旧NoResponse→Control重建链。
这只证明本次空闲周期没有重建，**不证明长通话媒体连续性**。

英国12:23:14与12:28:50各有 `PJSIP transport 'volte_ims' failed.`。固定上游源码复审确认，
断开回调清VoLTE子状态但可保留通用Registered；随后几条Missing Security-Server是候选检查，
不能单独当成整体注册失败。本次没有新2xx原文，也没有足够前后计时证据，故不宣称两次重连
已完全验证，不凭告警猜测新rekey缺陷。未改代码／配置、重启或追加收费通话。
一次被动SSH观察exit255中断，已补取同代际完整日志；没有将观察工具中断当成服务端重启。

## 剩余边界与后续入口

1. 指定号码短通话和首轮空闲换钥观察均已完成，不为补统计重拨，也不重复部署。
   英国两次transport告警仍是未闭合观察项；后续必须取得新2xx或对应连接证据再判断，
   不能反复采集同一个Registered、猜根因，或因短测通过宣称全部故障链已解决。
2. 浏览器工具明确URL安全策略拒绝页面操作；禁止换浏览器、CDP、代理等绕过。
   实际逐页点击、当前用户标签所载JS、麦克风／扬声器及真实多端呼入仍未验。
   服务器新资源正确不证明旧活动标签已刷新；不得把缓存当作已确认根因。
3. 已知P2：PC/SC原生调用没有硬超时；Agent12秒租约加故障挂断预算不等于最坏10秒停止计费。
   本次实际挂断成功不等于所有异常时限成熟；不能用强杀／提前释放锁伪装安全超时。
4. 延期UI小修：首次softphone GET失败后同iid数据刷新不重试，可能停在“能力检查中”。
   它不能解释精确“后端未就绪”截图，不混作本次根因。
5. 初始IKE_AUTH完整MAC、独立HTTPS通知hook证书校验、完整macOS私有4G/5G、Linux统一Agent、
   旧研究树封存与流程整理仍按后续清单处理。旧Windows未更新；旧协议设备未伪称全部可用。

当前私有索引：`/Users/fanli/.codex/private/mdd-reliability-20260826/RECOVERY_INDEX.md`。
实际通话：`/Users/fanli/.codex/private/mdd-authorized-calls-20260826`。
`TODO_ACTIVE_RECOVERY.md`及历史提交只作历史，不执行其旧“下一步”。
Runner D持久代理／传输、E3/E4媒体、30c/716/E6部署已有记录，禁止因压缩再次重做。
