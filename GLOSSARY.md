# Glossary

## Social graph（自然体验 map 决议）

Terms locked by [锁定关注与好友关系模型（互关=好友）](https://github.com/Drayee/ProjectionRoom/issues/42) (2026-10-30).

| Term | 中文 | Definition |
| --- | --- | --- |
| **Follow** | 关注 | One-way edge A→B. No consent required. Stored in `follows`. |
| **Follower** | 粉丝 | Someone who follows me (reverse list of follow edges). |
| **Mutual follow** | 互关 | Both A→B and B→A exist. |
| **Friend** | 好友 | Product synonym for **mutual follow**. Not a separate request workflow. |
| **Block** | 拉黑 | DM kill-switch between two users; while active neither may create **new** follows to the other; blocker's follow to target is removed; target's follow to blocker may remain; public posts/profile still visible. |
| **can_dm** | 可私聊 | `mutual_follow(A,B) AND NOT blocked_either(A,B)`. Server is source of truth (I3). |
| **Friend request** | 好友请求 | **Retired.** No `pending`→accept path for friendship. |

### Retired / do not use for friendship

- `friendships.status = pending | accepted` as friend source of truth.
- UI: 待处理请求、加好友请求、删除好友（use 取关 / 拉黑 only).

### Related

- Parent map: [自然体验：过渡反馈、对象组件、档案双视角与社交轻互动](https://github.com/Drayee/ProjectionRoom/issues/40)
- Capability inventory: [docs/notes/2026-10-28-natural-ux-capability-inventory.md](docs/notes/2026-10-28-natural-ux-capability-inventory.md) (pre-decision facts)
