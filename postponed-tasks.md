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
