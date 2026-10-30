# ADR: Friend = mutual follow; retire friend requests

- **Status**: Accepted
- **Date**: 2026-10-30
- **Context**: Wayfinder ticket [锁定关注与好友关系模型（互关=好友）](https://github.com/Drayee/ProjectionRoom/issues/42) on map [自然体验…](https://github.com/Drayee/ProjectionRoom/issues/40). Legacy model was `friendships` pending/accepted/blocked with DM gated on accepted.
- **Decision**: Introduce unidirectional `follow` edges; **friendship is exactly mutual follow**; DM predicate is mutual follow and not blocked; retire friend-request workflow; remove「删除好友」; block removes blocker's follow, may keep reverse follow, forbids new follows both ways, still allows viewing public content; migrate accepted→both follows, pending→one follow.
- **Consequences**: Friends UI becomes 好友/关注/粉丝; implementation must add follows API and migration; message send path switches off `areFriends(accepted)` to `can_dm` above; inventory gap「关注/粉丝」becomes in-scope product.
- **Alternatives rejected**: Follow without auto-friend; keep explicit friend requests; accept status as dual source of truth; block that only hides UI.
