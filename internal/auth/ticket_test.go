package auth

import (
	"encoding/base64"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestTicketStore 用 1 小时的 TTL：这些用例靠"显式的 now"来制造过期，
// 而不是靠 sleep，因此 TTL 取值只要足够长就不会干扰断言。
func newTestTicketStore(t *testing.T) *TicketStore {
	t.Helper()
	return NewTicketStore(time.Hour)
}

func TestTicketIssueAndConsumeOnce(t *testing.T) {
	ts := newTestTicketStore(t)

	ticket := ts.Issue(42)
	if ticket == "" {
		t.Fatal("Issue 不应返回空票据")
	}
	if ts.Len() != 1 {
		t.Fatalf("发一张票据后 Len 应为 1，实际 %d", ts.Len())
	}

	userID, ok := ts.Consume(ticket)
	if !ok || userID != 42 {
		t.Fatalf("首次消费应当命中并返回 42，实际 (%d, %v)", userID, ok)
	}
	if ts.Len() != 0 {
		t.Errorf("消费后条目应当被删除，Len=%d", ts.Len())
	}

	// 单次使用：任何后续尝试都必须失败（重复投递同一个 URL、浏览器重试都会走到这里）。
	if userID, ok := ts.Consume(ticket); ok || userID != 0 {
		t.Fatalf("第二次消费必须失败，实际 (%d, %v)", userID, ok)
	}
}

// 票据是不透明随机串：不含身份信息，且随机段足够长（≥32 字节）。
func TestTicketIsOpaqueAndRandom(t *testing.T) {
	ts := newTestTicketStore(t)

	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		ticket := ts.Issue(7)
		if seen[ticket] {
			t.Fatalf("票据重复：%q", ticket)
		}
		seen[ticket] = true

		if strings.ContainsAny(ticket, "+/=") {
			t.Fatalf("票据必须是 URL 安全的 base64url（无 + / =）：%q", ticket)
		}
		raw, err := base64.RawURLEncoding.DecodeString(ticket)
		if err != nil {
			t.Fatalf("票据不是合法的 base64url：%q（%v）", ticket, err)
		}
		if len(raw) != ticketBytes {
			t.Fatalf("随机段应当恰好 %d 字节，实际 %d", ticketBytes, len(raw))
		}
	}
	// 同一用户的票据之间也不能有可推导关系（否则"猜出别人的票据"只要知道 userID）。
	sameUser := map[string]bool{}
	for i := 0; i < 10; i++ {
		ticket := ts.Issue(12345)
		if sameUser[ticket] {
			t.Fatalf("同一用户的两张票据重复：%q", ticket)
		}
		sameUser[ticket] = true
	}
}

func TestTicketExpiresAndCannotBeConsumed(t *testing.T) {
	ts := NewTicketStore(40 * time.Millisecond)
	ticket := ts.Issue(42)

	time.Sleep(80 * time.Millisecond)

	if userID, ok := ts.Consume(ticket); ok || userID != 0 {
		t.Fatalf("过期票据必须拒绝，实际 (%d, %v)", userID, ok)
	}
	// 过期项被顺手删掉，不留在表里慢慢涨。
	if ts.Len() != 0 {
		t.Errorf("过期条目应当被删除，Len=%d", ts.Len())
	}
}

// 一张票据只能被消费一次，且这件事必须在并发下成立。
//
// 为什么要专门测并发：WS 客户端在"连接失败重试"或"多个标签页抢同一 URL"时
// 会真的并发打同一个票据。如果 Consume 实现成"先查后删"，100 个请求里会有
// 多个拿到 ok=true —— 单次使用的承诺就此失效，而且单线程用例永远发现不了。
func TestTicketConsumeIsAtomicUnderConcurrency(t *testing.T) {
	const goroutines = 100

	ts := newTestTicketStore(t)
	ticket := ts.Issue(99)

	var success atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start // 尽量让 100 个 goroutine 同时冲进 Consume
			if userID, ok := ts.Consume(ticket); ok && userID == 99 {
				success.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := success.Load(); got != 1 {
		t.Fatalf("100 个并发消费者里应当恰好 1 个成功，实际 %d", got)
	}
	if ts.Len() != 0 {
		t.Errorf("抢完之后表应为空，实际 %d", ts.Len())
	}
}

func TestTicketConcurrentIssueIsSafe(t *testing.T) {
	const goroutines = 100

	ts := newTestTicketStore(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[string]bool)
	dup := 0

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(userID int64) {
			defer wg.Done()
			ticket := ts.Issue(userID)
			mu.Lock()
			defer mu.Unlock()
			if seen[ticket] {
				dup++
			}
			seen[ticket] = true
		}(int64(i + 1))
	}
	wg.Wait()

	if dup != 0 {
		t.Fatalf("并发签发的票据出现 %d 个重复", dup)
	}
	if ts.Len() != goroutines {
		t.Fatalf("并发签发后 Len 应为 %d，实际 %d", goroutines, ts.Len())
	}
}

func TestTicketSweep(t *testing.T) {
	t.Run("空表返回 0", func(t *testing.T) {
		if n := newTestTicketStore(t).Sweep(time.Now()); n != 0 {
			t.Fatalf("空表的清理数应为 0，实际 %d", n)
		}
	})

	// 未过期的票据绝不能被清掉：那是"已取票、还没握手"的正常中间态。
	t.Run("不清理未过期项", func(t *testing.T) {
		ts := newTestTicketStore(t)
		ticket := ts.Issue(1)

		if n := ts.Sweep(time.Now()); n != 0 {
			t.Fatalf("未过期时清理数应为 0，实际 %d", n)
		}
		if _, ok := ts.Consume(ticket); !ok {
			t.Error("清理之后票据仍应可用")
		}
	})

	// 用显式的 now 制造过期（TTL=1h，now 推到 2h 之后），不依赖 sleep。
	t.Run("清理数等于过期条数", func(t *testing.T) {
		ts := newTestTicketStore(t)
		for i := 0; i < 3; i++ {
			ts.Issue(int64(i + 1))
		}

		if n := ts.Sweep(time.Now().Add(2 * time.Hour)); n != 3 {
			t.Fatalf("清理数应为 3，实际 %d", n)
		}
		if ts.Len() != 0 {
			t.Errorf("清理后应为空，实际 %d", ts.Len())
		}
	})

	// 混合场景：过期的被清掉、未过期的留下，且留下的仍然能消费。
	t.Run("混合场景", func(t *testing.T) {
		ts := NewTicketStore(50 * time.Millisecond)
		stale1 := ts.Issue(1)
		stale2 := ts.Issue(2)
		time.Sleep(80 * time.Millisecond)
		fresh := ts.Issue(3)

		if n := ts.Sweep(time.Now()); n != 2 {
			t.Fatalf("应当清掉 2 张过期票据，实际 %d", n)
		}
		if ts.Len() != 1 {
			t.Fatalf("应当留下 1 张未过期票据，实际 %d", ts.Len())
		}
		if _, ok := ts.Consume(stale1); ok {
			t.Error("被清掉的票据不能复活")
		}
		if _, ok := ts.Consume(stale2); ok {
			t.Error("被清掉的票据不能复活")
		}
		if userID, ok := ts.Consume(fresh); !ok || userID != 3 {
			t.Fatalf("未过期票据应仍可消费，实际 (%d, %v)", userID, ok)
		}
	})
}

// 票据之间互不影响：一张被消费/被清理，不能波及别人的票据。
func TestTicketIndependence(t *testing.T) {
	ts := newTestTicketStore(t)
	a := ts.Issue(1)
	b := ts.Issue(2)

	if userID, ok := ts.Consume(a); !ok || userID != 1 {
		t.Fatalf("a 应当命中 1，实际 (%d, %v)", userID, ok)
	}
	if userID, ok := ts.Consume(b); !ok || userID != 2 {
		t.Fatalf("b 应当命中 2，实际 (%d, %v)", userID, ok)
	}
}

func TestTicketConsumeRejectsGarbage(t *testing.T) {
	ts := newTestTicketStore(t)
	ts.Issue(1)

	cases := []struct {
		name   string
		ticket string
	}{
		{"空串", ""},
		{"未知票据", strings.Repeat("A", 43)},
		{"超长票据", strings.Repeat("a", maxTicketLen+1)},
		{"携带身份信息的明文", "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if userID, ok := ts.Consume(tc.ticket); ok || userID != 0 {
				t.Fatalf("垃圾票据必须拒绝，实际 (%d, %v)", userID, ok)
			}
		})
	}
	if ts.Len() != 1 {
		t.Errorf("失败的消费不应影响已有票据，Len=%d", ts.Len())
	}
}

// TTL 非正时退回默认值：否则 WS 鉴权会静默全灭（症状比配置错误更难查）。
func TestNewTicketStoreFallsBackToDefaultTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second} {
		ts := NewTicketStore(ttl)
		if ts.ttl != DefaultTicketTTL {
			t.Fatalf("TTL=%s 应退回 %s，实际 %s", ttl, DefaultTicketTTL, ts.ttl)
		}
		if ticket := ts.Issue(5); ticket == "" {
			t.Fatal("退回默认 TTL 后应当能正常签发")
		}
	}
}
