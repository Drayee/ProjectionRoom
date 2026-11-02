# 文档总索引

> 这是 `docs/` 的入口。**读什么取决于你要动哪一块** —— 别从根目录开始遍历。
> 项目本身的入口仍是根目录 [`README.md`](../README.md)（用户可见的门面）与 [`CLAUDE.md`](../CLAUDE.md)（AI 会话的唯一入口地图）。
>
> 最后整理：**2026-11-02**。新增/移动文档时**顺手更新本页**——过时的索引比没有索引更糟。

---

## 现行规范（以这里为准）

当前有效的设计与规格。与代码冲突时，先怀疑文档过时，再怀疑代码。

| 文档 | 一句话 |
| :--- | :--- |
| [`SPEC.md`](SPEC.md) | 房间与信令协议的设计规格：不变量 I1（服务端不经手视频字节）、DataChannel 信封、房间码与主播宽限期。 |
| [`ALGORITHM.md`](ALGORITHM.md) | 算法说明：视频数据如何从主播流到最深叶子节点，以及所有节点的播放进度如何对齐。 |
| [`SEGMENT.md`](SEGMENT.md) | 服务端视频切片（一次性预处理）：用户没有 ffmpeg 时把源视频传上去切好、再一次性下载回本地。 |
| [`ACCOUNTS.md`](ACCOUNTS.md) | 账号 / 社交 / 管理端 / 数据库持久化这一层的设计规格（混合式会话、幂等、验证码、好友与关注）。 |
| [`DEPLOY-SINGLE-PORT.md`](DEPLOY-SINGLE-PORT.md) | 生产部署形态：对外只有 **80** 一个入口，nginx 反代到 `127.0.0.1:8080`，前端/API/WS 同源。 |
| [`STORAGE-MINIO.md`](STORAGE-MINIO.md) | 对象存储（MinIO）：图片等静态附件的部署形态、配置、运维与排障。 |
| [`GLOSSARY.md`](GLOSSARY.md) | 术语表：社交图谱等自然体验决议里的词汇在此统一口径，写文档前先查这里。 |

### 给 AI 技能用的仓库约定

这三份是工程技能（issue tracker / triage / domain 探索）消费的配置，不是产品文档，但同样属于"现行"。

| 文档 | 一句话 |
| :--- | :--- |
| [`agents/issue-tracker.md`](agents/issue-tracker.md) | Issue tracker 是 GitHub Issues；票与规格怎么建、怎么查。 |
| [`agents/triage-labels.md`](agents/triage-labels.md) | 五个规范 triage 角色到本仓库实际 label 字符串的映射，外加五个 `wayfinder:*`。 |
| [`agents/domain.md`](agents/domain.md) | 技能探索代码库时该如何消费本仓库的领域文档（multi-context）。 |

---

## 决策记录（ADR）

做过就定下来的取舍，**改主意要新写一篇而不是改旧的**。

| 文档 | 一句话 |
| :--- | :--- |
| [`adr/0001-friend-equals-mutual-follow.md`](adr/0001-friend-equals-mutual-follow.md) | 好友 = 互相关注；「好友请求」流水线整体退役（票 #59 的决策依据）。 |

---

## 实施计划与报告

历史交付物：计划写"当时打算怎么做"，报告写"实际做成了什么"。**都不代表现状**。

### 实施计划

| 文档 | 一句话 |
| :--- | :--- |
| [`plans/2026-10-08-accounts-phase1.md`](plans/2026-10-08-accounts-phase1.md) | 账号层一期实施计划：数据库表与账号骨架（文中前端路径已因月夜故障重写而过时）。 |
| [`plans/2026-10-08-accounts-phase2.md`](plans/2026-10-08-accounts-phase2.md) | 账号层二期实施计划：公开房间列表、管理端前端、标题/公开性、强关/下架。 |

### 报告

| 文档 | 一句话 |
| :--- | :--- |
| [`reports/T11-verify-report.md`](reports/T11-verify-report.md) | T11 验收脚本的实跑报告（2026-10-08），含做不到的部分与原因。 |
| [`reports/2026-10-14-resource-fonts-icons.md`](reports/2026-10-14-resource-fonts-icons.md) | 调研 `E:\Resource` 字体与图标的可入仓形态（票 #9）。 |
| [`reports/2026-10-14-frontend-structure-spec.md`](reports/2026-10-14-frontend-structure-spec.md) | 新目录结构与逻辑下沉的边界（票 #11）。 |
| [`reports/2026-10-14-contract-migration-spec.md`](reports/2026-10-14-contract-migration-spec.md) | 契约平移的落地清单与悬空项处置（票 #12）。 |
| [`reports/2026-10-14-glitch-motion-spec.md`](reports/2026-10-14-glitch-motion-spec.md) | 「月夜故障 · B（数字切片）」效果规格与降级策略（票 #13）。 |
| [`reports/MIGRATION-2026-10-19.md`](reports/MIGRATION-2026-10-19.md) | yachiyo → ProjectionRoom 迁移完成报告（686 用户、18 帖子，MD5 登录即升级 bcrypt）。 |

---

## 笔记

探索期的思考与原则沉淀。**结论可能已被后续票推翻**，读的时候看文首的票号与日期。

| 文档 | 一句话 |
| :--- | :--- |
| [`notes/2026-10-28-natural-ux-capability-inventory.md`](notes/2026-10-28-natural-ux-capability-inventory.md) | 盘点现有体验面与档案/互动 API 的缺口（票 #41）。 |
| [`notes/2026-10-28-natural-ux-wayfinder-draft.md`](notes/2026-10-28-natural-ux-wayfinder-draft.md) | 自然体验 wayfinder 地图草稿；**真源是 GitHub issue #40**，冲突时以 issue 为准。 |
| [`notes/2026-10-30-natural-layer-visual-principles.md`](notes/2026-10-30-natural-layer-visual-principles.md) | 月夜故障 DNA 之上的自然层视觉原则（票 #43）。 |
| [`notes/2026-10-30-route-transition-navigation-principles.md`](notes/2026-10-30-route-transition-navigation-principles.md) | 路由叙事过渡与导航当前态原则（票 #45）。 |

另有 [`prototypes/`](prototypes/) 目录存放一次性原型（故障方向 demo 与其构建脚本），**不是**产品代码，不参与构建。

---

## 归档（已过时，仅供追溯）

这里的东西**描述的都是历史流程，照做会出错**。每篇顶部都有横幅说明被什么取代、以及该看哪篇现行文档。

| 文档 | 一句话 |
| :--- | :--- |
| [`archive/2026-10-19-deployment-summary.md`](archive/2026-10-19-deployment-summary.md) | 旧项目迁移的"最终部署方案总结"：systemd + 手工 docker 时代（**已被 Docker Compose 双容器取代**）。 |
| [`archive/2026-10-19-manual-deployment.md`](archive/2026-10-19-manual-deployment.md) | 快速手动部署方案：手工建库、`docker build` 上传、`scp` 二进制 + `systemctl restart`（**已作废**）。 |
| [`archive/2026-10-19-yachiyo-migration-guide.md`](archive/2026-10-19-yachiyo-migration-guide.md) | yachiyo 数据迁移指南（**迁移已完成**，完成报告见 `reports/MIGRATION-2026-10-19.md`）。 |
| [`archive/2026-10-19-gh-actions-token.md`](archive/2026-10-19-gh-actions-token.md) | GHCR Token 设置指南；**Secret 名 `GITHUB_REGISTRY_TOKEN` 已过时**，现行工作流读 `REGISTRY_TOKEN`。 |

> 归档文件里的凭据已脱敏为 `<REDACTED>`。真实值只在两处：服务器 `/opt/projectionroom/.env`（root:600）与 GitHub 仓库 Secrets。**不要把真实值写回仓库**。

---

## 本页维护约定

1. **新增文档**：放进上面五个组之一，补一行"一句话"，链接必须是真实存在的相对路径（相对本文件所在目录）。
2. **文档过时**：不要直接删。移到 `archive/` 并在顶部加横幅，写明**被什么取代**、**日期**、**现行文档链接**。
3. **相对链接层级**：本页在 `docs/` 下，指向 `docs/` 内文件用 `XXX.md` / `子目录/XXX.md`；指向仓库其它位置用 `../XXX`。
