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
2. **数据、配置、容器三者隔离。** Docker 只挂载源码树外、权限受控且职责分离的宿主目录；配置经 Control 的
   配置服务/API 更新并原子写入；Engine 只收到按实例生成的只读运行配置，不自行成为配置权威。
   deploy records/build cache 不再与运行态数据库、锁、证书和实例配置混在同一 data root。
3. **Engine 常驻。** Control 更新仅重建 Control service（`docker compose up --no-deps -d control`）；
   Engine 只由 Control 的显式生命周期事务创建、停止或替换，普通配置/网页/Control reload 绝不
   重建或重启 Engine。
4. **镜像瘦身。** Control 用 Debian slim 多阶段镜像；Engine 移出 Fedora 44，采用 pinned Debian
   slim builder/runtime 多阶段，运行时只保留 Asterisk、PC/SC/必需 tunnel 二进制及 Python runtime。
   构建依赖、包缓存和源码不进入最终镜像。
5. **入口清理边界。** entrypoint 只清理容器自己的临时目录、socket/pid、过期 runtime cache；
   不能删除持久根中的配置、SQLite、证书、审计、呼叫/SMS 记录或任意跨代恢复证据。

## 实施顺序

1. 盘点当前 Dockerfile、Control `engine.start()`、install 生命周期和所有 data 写入点，写出
   目标 volume/config contract 和迁移清单。
2. 实现 Compose production manifest、受控数据根布局、source-sync guard 与 entrypoint cleanup
   contract；先在隔离 runner 做 build/volume/restart/negative data-loss 验证。
3. 将 Control lifecycle 调度接到 Compose/API，不改变 Engine replacement 的代际、通话挂断和
   付费 fence 语义；验证 Control-only update 不触碰 Engine container ID/restart count。
4. 迁移 Engine Dockerfile 到 slim 多阶段，比较镜像大小、启动契约和 Asterisk/PCSC/SWu 功能；
   仅在完整独立验收后作为新的 Engine revision 走显式替换。
5. 生产迁移必须创建新持久根/备份 manifest、先只迁移 Control、保留旧目录可回滚；最后恢复并
   验证本文件“冻结现场”的 card facts/Agent/VoWiFi 状态。

## 2026-08-28：第一批隔离契约已落地（未部署）

- 已删除 Engine/Control runtime overlay Dockerfile 与 `docker cp`/`docker commit` 路径。Engine
  只有完整指纹复用或正式 Dockerfile 重建两种来源；Control 更新只从正式 Dockerfile 构建。
- `compose.production.yaml` 已分开 config、state、artifact、runtime 四个宿主根；
  `deploy/mdd-compose.sh` 在任何构建前拒绝相同根、相对路径和源码树内路径，并只执行
  `compose build control` + `compose up --no-deps -d control`。
- Control 的 `config.yaml`/`auth.json` 已支持独立 `MDD_CONFIG_DIR`，SQLite、Engine run/logs、
  lifecycle fences 归 `MDD_STATE_DIR`；lpac 等可执行产物归 `MDD_ARTIFACT_DIR`。旧安装显式
  `MDD_DATA` 仍保持原路径，正式迁移前不自动搬动生产数据。
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

## 2026-08-28：data 根目录彻底收口（未部署）

- 新部署唯一契约为 `MDD_CONFIG_DIR`、`MDD_STATE_DIR`、`MDD_ARTIFACT_DIR`、
  `MDD_RUNTIME_DIR`；容器和 installer 均不再以 `/data` 或源码树 `./data` 为默认值。
  `MDD_DATA` 只保留为旧安装显式兼容输入，不能由新 Compose/installer 产生。
- 配置/TLS、SQLite/审计/恢复证据、lpac/发布与部署记录、socket 分属四根；Engine sibling
  mount 新增独立 host-config 映射，旧 `/data/certs/...` 会迁移映射到配置根。
- 新增 `deploy/migrate-data-layout.py`：目标必须为空且互异，拒绝 symlink/special file，逐文件
  SHA256/大小验证，失败仅回收本次新建目标，成功保留完整源目录并写 migration manifest。
  本机旧根 31 个文件已按该工具验证，并原样归档到外置盘；工作区内已不存在 `data/`。
- `agent/data`、`control/app/data` 中的只读内置数据库改名为 `resources`，避免再与运行数据混淆。
- 修复 Control entrypoint 越界删除宿主 `/run/pcscd/*` 并启动竞争 pcscd 的问题；现在仅校验
  宿主 socket 并作为客户端使用。
- 本地聚焦回归 `111 passed, 16 subtests passed`；项目 `.venv` 全量回归
  `2223 passed, 1 skipped, 1 warning, 144 subtests passed`。
  private runner D：Compose 真实解析、Control 完整镜像 build、分离挂载两次容器运行、完整
  Control 启动/重启、配置/SQLite/产物/socket 保持及 pcsc 负向不破坏验证均通过；测试容器已清。
  runner C 构建被其失效 Docker 代理阻断，未误报为代码失败。

`next_action`：提交本批 data layout 整改；随后回到 Engine Debian slim 多阶段镜像，生产仍不部署。

## 2026-08-28：Engine Debian slim 多阶段镜像完成（未部署）

- Engine 已从 Fedora 44 单阶段镜像迁移到同一固定
  `debian:trixie-20260824-slim` digest 的 builder/runtime 多阶段构建；apt 使用 Debian 官方
  2026-08-24 签名快照。sysmocom Asterisk/pjproject、所有本地补丁、PCSC 2.3.3 协议版本和既有
  Engine ABI labels 保持不变。
- builder 保留完整编译依赖，runtime 只复制 `/usr` 运行产物、Asterisk `/var/lib` 数据和固定
  Python venv；源码、头文件、静态库、pcscd、构建工具、包缓存和临时 run/log/spool 不进入运行
  镜像。Engine 只作为宿主 pcscd 的客户端，pcsc-lite 构建明确关闭镜像内 daemon 的
  udev/systemd/USB/serial 功能。
- Asterisk/Python ELF 依赖由构建期脚本扫描并映射到 Debian runtime 包；Debian diversion 元数据
  不再被误解析为包名。Python 直接与传递依赖全部固定版本，requirements 和依赖收集器都进入
  Engine base fingerprint。
- 构建过程发现旧 Dockerfile 的 `codec_opus` 请求会在缺少 `xmlstarlet` 时被 menuselect 静默
  保持禁用。新构建补齐上游声明依赖，并在编译前、镜像完成时分别硬校验启用状态和实际
  `codec_opus.so` 产物；不是通过删除检查规避。
- private runner D 从零构建成功。旧 Engine 约 2.24GB，新镜像实际 size
  `389,571,762` bytes（约 372MiB，缩小约 83%）；Asterisk 20.7.0、321 个模块，关键 IMS、AMR、
  Opus、WebSocket、admission、bridged-answer 模块齐全；Asterisk 与 venv 全部原生文件逐项 `ldd`
  无 `not found`，运行 Python 为 `/opt/mdd-venv/bin/python3`，runtime 无 gcc/git/dnf 和源码树。
- runner D `--network none` 无收费 E2E 全部通过：配置 Unix socket 往返、digest/0600 原子快照及
  Control 离线精确复用；Asterisk admission；REGISTER dispatch fence；浏览器出站 3 次双向 PCM
  echo、单次 fake SIP INVITE、DTMF、跨 Redirect 独占锁；浏览器入站精确双腿桥接、重复/错误
  owner 拒绝、接听/挂断结果。每项最终均为 0 active channels，并显式停止测试 Asterisk。
- 浏览器 E2E runner 已等待 Asterisk `core waitfullybooted` 和目标 dialplan context/extension；
  AMI 端口早于 `pbx_config`/PBX 完整就绪的启动竞态不再造成虚假的 “Extension does not exist”
  或 PCM echo 欠载。最终出站 E2E 连续 3 次通过，入站再次通过。
- 本机聚焦契约通过；外置盘短 TMPDIR 下全量回归
  `2227 passed, 1 skipped, 1 warning, 144 subtests passed`。第一次使用过长 TMPDIR 时仅有 5 个
  macOS AF_UNIX 路径超限失败，改用外置盘短路径后该 5 项和全量均通过。

`next_action`：整批复审并提交 Engine 镜像改造；随后审计本文件其余生命周期/Compose 目标是否
仍有未完成项。生产默认 Engine 和在线 Engine 均未替换，只有完成生产迁移预检与显式 Engine
replacement 事务后才能部署此 revision。
