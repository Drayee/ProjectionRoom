package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// 关注事件的推送判据（票 #67；决议 #53 §5）。
//
// 三条，缺一不可：
//
//  1. 真的新建了边才推 —— 这个端点是幂等的，双击「关注」会走两次 handler，
//     第二次没有新边却会再推一帧「有人关注了你」。那让提示变成
//     "点得越多、对方被通知越多"，而这件事的语义是"发生了一次"。
//  2. 未互关时推 follow-created（收件人 = 被关注方）。
//  3. 互关时推 mutual-follow（收件人 = 双方），**不叠加**一条 follow-created ——
//     对收件人来说这是同一时刻发生的同一件事，推两帧会让界面上叠两条提示。

// fakeFollowPusher 记录被推送的事件（顺序敏感）。
type fakeFollowPusher struct {
	created [][2]int64
	mutual  [][2]int64
}

func (f *fakeFollowPusher) PushFollowCreated(_ context.Context, followerID, followeeID int64) bool {
	f.created = append(f.created, [2]int64{followerID, followeeID})
	return true
}

func (f *fakeFollowPusher) PushMutualFollow(_ context.Context, userA, userB int64) bool {
	f.mutual = append(f.mutual, [2]int64{userA, userB})
	return true
}

// followOne 走真实端点关注一个人，断言 201。
func followOne(t *testing.T, env *followEnv, token string, targetID int64) {
	t.Helper()
	body, _ := json.Marshal(map[string]int64{"userId": targetID})
	w := env.doFollow(t, http.MethodPost, "/api/follows", token, string(body), nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("关注应当 201，实得 %d：%s", w.Code, w.Body.String())
	}
}

// TestFollowCreatedPushesToFollowee 覆盖第 2 条。
func TestFollowCreatedPushesToFollowee(t *testing.T) {
	pusher := &fakeFollowPusher{}
	env := newFollowEnvWithPusher(t, pusher)

	followOne(t, env, env.tokA, env.bob.ID)

	if len(pusher.created) != 1 {
		t.Fatalf("应当推一次 follow-created，实得 %d 次：%+v", len(pusher.created), pusher.created)
	}
	got := pusher.created[0]
	if got[0] != env.alice.ID || got[1] != env.bob.ID {
		t.Fatalf("follow-created 的方向不对（应当 follower=alice followee=bob）：%+v", got)
	}
	if len(pusher.mutual) != 0 {
		t.Fatalf("未互关时不该推 mutual-follow：%+v", pusher.mutual)
	}
}

// TestIdempotentFollowDoesNotRepush 覆盖第 1 条。
//
// 这是本票最容易漏的一条：`CreateFollow` 返回 `created=false` 表示
// "这条边早就在了"，而那时推一帧提示等于把幂等变成了"每点一次通知一次"。
func TestIdempotentFollowDoesNotRepush(t *testing.T) {
	pusher := &fakeFollowPusher{}
	env := newFollowEnvWithPusher(t, pusher)

	followOne(t, env, env.tokA, env.bob.ID)
	followOne(t, env, env.tokA, env.bob.ID)
	followOne(t, env, env.tokA, env.bob.ID)

	if len(pusher.created) != 1 {
		t.Fatalf("重复关注只该推第一次，实得 %d 次：%+v", len(pusher.created), pusher.created)
	}
}

// TestMutualFollowPushesBothSides 覆盖第 3 条。
//
// 回关那一刻两边同时成立：只推给"回关的那一方"会让先关注的人
// 永远收不到"对方回关了你"，而那正是他等的那条消息。
func TestMutualFollowPushesBothSides(t *testing.T) {
	pusher := &fakeFollowPusher{}
	env := newFollowEnvWithPusher(t, pusher)

	// alice 先关注 bob（未互关 → follow-created）。
	followOne(t, env, env.tokA, env.bob.ID)
	if len(pusher.created) != 1 {
		t.Fatalf("前置：alice 关注 bob 应当推一次 follow-created，实得 %d", len(pusher.created))
	}

	// bob 回关 alice → 互关。
	followOne(t, env, env.tokB, env.alice.ID)

	if len(pusher.mutual) != 1 {
		t.Fatalf("互关时应当推一次 mutual-follow，实得 %d 次：%+v", len(pusher.mutual), pusher.mutual)
	}
	got := pusher.mutual[0]
	// 双方都在：两个 id 的集合必须等于 {alice, bob}。
	if !(got[0] == env.alice.ID && got[1] == env.bob.ID) && !(got[0] == env.bob.ID && got[1] == env.alice.ID) {
		t.Fatalf("mutual-follow 的双方不对：%+v", got)
	}
	// **不叠加** follow-created：回关这一帧不该再单独推一条"有人关注了你"。
	if len(pusher.created) != 1 {
		t.Fatalf("互关时不该再叠加 follow-created（应当仍是 1 次），实得 %d 次：%+v",
			len(pusher.created), pusher.created)
	}
}

// TestNilPusherIsSafe 钉住"没有推送能力不是错误"（§7.2 的降级路径）。
func TestNilPusherIsSafe(t *testing.T) {
	env := newFollowEnv(t) // pusher = nil

	followOne(t, env, env.tokA, env.bob.ID)

	// 边真的落了。
	w := env.doFollow(t, http.MethodGet, "/api/follows?tab=following", env.tokA, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("关注名单应当 200，实得 %d", w.Code)
	}
	var resp followerListResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].UserID != env.bob.ID {
		t.Fatalf("关注名单应当有 bob：%+v", resp.Items)
	}
}
