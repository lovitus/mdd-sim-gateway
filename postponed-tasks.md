# 已识别但不并入当前通话修复的边界

- 持久租约在Control重启丢失RAM owner之后，历史记录仍仅有ICCID，没有原Agent/Modem身份。
  本批只保护仍有确切RAM owner的续接、终止及后台恢复，不宣称覆盖跨硬件迁移后的旧孤儿记录。
  后续单独评审身份持久化/旧数据兼容与迁移测试；当前Agent本地付费租约保护不撤销。
- 浏览器媒体 WS 已支持同一 owner/session 在固定 10 秒内重接；Control 进程重启会丢失 RAM
  session，仍只能依靠 Asterisk/Agent 的停费租约收敛，不能跨 Control 重启恢复旧媒体通话。
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
