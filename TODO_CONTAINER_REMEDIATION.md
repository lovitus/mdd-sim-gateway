# 容器、生命周期与数据整改：唯一执行游标

## 2026-08-28：重构前冻结现场

本文件从过大的 `TODO_CURRENT_RECOVERY.md` 摘出。本轮容器整改完成前，**不再扩大**以下遗留
问题的自动修复范围；现场与证据保留，后续从本文件继续。

- 生产 Control 已部署状态事实/设置验证工具提交 `ef9ec7b`、`e93c4cf`、`61cc8a4`、`ea9303f`、
  `94714d4`、`62fb8e8`。Control image 会在本轮 Dockerfile/Compose 重构时重新构建；Engine
  image 维持当前版本，除非新的显式 Engine revision 通过替换事务。
- 生产 `data/config.yaml` 曾被错误的全目录 rsync 覆盖为空实例配置。已从最新私有快照恢复
  iid1–9，并按 Engine runtime 同步 reader fields；候选、被覆盖文件和恢复摘要均留在私有生产
  deploy record。**不得再次用全目录 rsync 覆盖 `data/`。**
- 管理员初始化会轮换 Agent token；已将新 token 通过 Agent 的受控配置接口同步到在线 Mac/Windows
  Agent，VPCD websocket 403 已恢复为 accepted。不得在命令行、Git、镜像或记录中写 token。
- 运行中的 remote VPCD 卡已读取到 identity：iid7（ICCID 后四位 7733）、iid9（0371）、iid1
  （1522）；槽位可在重连时重新分配。显式检测现在会投影身份到 VPCD/card facts，且不会自动
  启动 Engine。
- 暂存未关闭项：iid7 的 VoWiFi/IMS 仍为 rejected；浏览器 PCM/收费稳定测试尚需用户实际浏览器
  验收。当前任何 Engine 均须保持零活动通话后才能进行必要维护。
- 事故根因：人工全目录 rsync 同步源码时没有排除 `data/`，覆盖了生产实例配置并保留 UID1000
  的 runtime lock roots；Control 以 root 运行时按安全契约拒绝卡探测。生产已从私有快照原子恢复
  配置，并将 `instances`/`orchestrator` 生命周期根修正为 root-owned。新的
  `tools/mdd-source-sync.sh` 已强制排除 data 且不支持 delete；生产同步已读回 config hash 不变。

## 已定架构方向

1. **仅 Dockerfile/Compose 部署。** 生产代码封在不可变镜像；禁止 bind mount 源码、禁止用
   runtime overlay 或全目录 rsync 修改运行代码。Control 和每个 Engine 都由同一个 Compose
   project/Control lifecycle API 管理。
2. **数据、配置、容器三者隔离。** Docker named volumes 仅保存运行数据；配置经 Control 的
   配置服务/API 更新并原子写入；Engine 只收到按实例生成的只读运行配置，不自行成为配置权威。
   deploy records/build cache 不再与运行态数据库、锁、证书和实例配置混在同一 data root。
3. **Engine 常驻。** Control 更新仅重建 Control service（`docker compose up --no-deps -d control`）；
   Engine 只由 Control 的显式生命周期事务创建、停止或替换，普通配置/网页/Control reload 绝不
   重建或重启 Engine。
4. **镜像瘦身。** Control 用 Debian slim 多阶段镜像；Engine 移出 Fedora 44，采用 pinned Debian
   slim builder/runtime 多阶段，运行时只保留 Asterisk、PC/SC/必需 tunnel 二进制及 Python runtime。
   构建依赖、包缓存和源码不进入最终镜像。
5. **入口清理边界。** entrypoint 只清理容器自己的临时目录、socket/pid、过期 runtime cache；
   不能删除 named volume 中的配置、SQLite、证书、审计、呼叫/SMS 记录或任意跨代恢复证据。

## 实施顺序

1. 盘点当前 Dockerfile、Control `engine.start()`、install 生命周期和所有 data 写入点，写出
   目标 volume/config contract 和迁移清单。
2. 实现 Compose production manifest、受控数据根布局、source-sync guard 与 entrypoint cleanup
   contract；先在隔离 runner 做 build/volume/restart/negative data-loss 验证。
3. 将 Control lifecycle 调度接到 Compose/API，不改变 Engine replacement 的代际、通话挂断和
   付费 fence 语义；验证 Control-only update 不触碰 Engine container ID/restart count。
4. 迁移 Engine Dockerfile 到 slim 多阶段，比较镜像大小、启动契约和 Asterisk/PCSC/SWu 功能；
   仅在完整独立验收后作为新的 Engine revision 走显式替换。
5. 生产迁移必须创建新 volume/备份 manifest、先只迁移 Control、保留旧目录可回滚；最后恢复并
   验证本文件“冻结现场”的 card facts/Agent/VoWiFi 状态。

## 2026-08-28：第一批隔离契约已落地（未部署）

- 已删除 Engine/Control runtime overlay Dockerfile 与 `docker cp`/`docker commit` 路径。Engine
  只有完整指纹复用或正式 Dockerfile 重建两种来源；Control 更新只从正式 Dockerfile 构建。
- `compose.production.yaml` 已分开 config、state、artifact、runtime 四个宿主根；
  `deploy/mdd-compose.sh` 在任何构建前拒绝相同根、相对路径和源码树内路径，并只执行
  `compose build control` + `compose up --no-deps -d control`。
- Control 的 `config.yaml`/`auth.json` 已支持独立 `MDD_CONFIG_DIR`，SQLite、Engine run/logs、
  lifecycle fences 继续归 `MDD_DATA` state；lpac 等可执行产物归 `MDD_ARTIFACT_DIR`。旧安装未设
  新环境变量时保持原路径，正式迁移前不自动搬动生产数据。
- 新 Engine 不再 bind 宿主 `instances/<iid>/instance.json`。Control 以 root-only Unix socket
  提供按 iid + canonical digest 绑定的 HMAC 配置快照；Engine 验证 digest 后只写容器本地
  `/config/instance.json`。同容器重启可复用精确快照，Control 重启后可重新服务同一 generation；
  旧版 frozen create-spec 仍可只用于精确回滚。
- 配置 socket/任一必要 bind 不可见时，在删除旧 Engine 前 fail closed。entrypoint 只清理自己
  的 `engine-config.sock`，不碰配置、SQLite、证书、历史、fence 或 persistent state。
- 本批聚焦验证：`271 passed, 29 subtests passed`；真实 Unix socket 往返、错误 proof 拒绝、
  container-local snapshot 复用、Engine lifecycle/replacement/create-spec、Compose/data contract
  均通过。尚未构建新 Engine 镜像、未部署、未操作生产 Engine。

## 调研依据

- Docker 官方建议生产部署不 bind mount 应用源码，并用 Compose 的 `up --no-deps -d service`
  仅更新目标服务；持久态以 volumes/configs/secrets 声明管理。
- Docker 官方建议多阶段构建、最小 build context、`.dockerignore`、`--no-install-recommends`
  与 BuildKit cache mounts，使构建工具与包缓存不进入最终镜像。

`next_action`：将 Engine 从 Fedora 44 迁移到 pinned Debian slim builder/runtime 多阶段镜像，
在 private runner C 完成干净构建、rootfs/依赖/启动/config-socket 合约和镜像大小验证；通过前不部署。
