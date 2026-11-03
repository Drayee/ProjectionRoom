package service

// 头像覆盖层（决议 #54 / 票 #74）在服务层的证据。
//
// 服务层这一层要守的是**三条规则**，不是"把一列写进去"：
//
//  1. URL 必须属于本桶 —— 这是"外链直填"的唯一拦截点（决议 #54 §8）；
//  2. 对象存储未配置时整条路径拒绝（503），而不是静默存一个死链；
//  3. 换掉旧图之后，旧对象要**事后**删掉（I4），且删除失败不能影响这次响应。
//
// 第 3 条最容易写错：删旧图放在写库之前会留下"库里指着一张已删的图"的坏窗口，
// 放在请求路径上又违反 I4。所以单独两条用例钉住顺序与失败容忍。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ProjectionRoom/internal/store"
)

// fakeAvatarBackend 是 AvatarBackend 的内存替身。
//
// owned 是"我们认为属于本桶的前缀"：用前缀判而不做形态校验，因为形态校验
// 已经被 storage 包的单测逐变体钉住了（minio_test.go）—— 这里要测的是
// **服务层有没有调用它**，不是它本身对不对。
type fakeAvatarBackend struct {
	mu      sync.Mutex
	prefix  string
	removed []string
	// removeErr 非 nil 时 Remove 返回它（测"删失败不影响响应"）。
	removeErr error
	// removedCh 在每次 Remove 后收到一个信号（测异步性用）。
	removedCh chan string
}

func newFakeAvatarBackend() *fakeAvatarBackend {
	return &fakeAvatarBackend{
		prefix:    "https://cdn.example.test/pr-files/",
		removedCh: make(chan string, 8),
	}
}

func (f *fakeAvatarBackend) OwnsURL(u string) bool {
	return strings.HasPrefix(u, f.prefix)
}

func (f *fakeAvatarBackend) Remove(_ context.Context, publicURL string) error {
	f.mu.Lock()
	f.removed = append(f.removed, publicURL)
	err := f.removeErr
	f.mu.Unlock()
	select {
	case f.removedCh <- publicURL:
	default:
	}
	return err
}

func (f *fakeAvatarBackend) removedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.removed)
}

// waitRemoved 等一次异步删除落地（submit 在没有 EventWriter 时走 goroutine）。
func (f *fakeAvatarBackend) waitRemoved(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-f.removedCh:
		if got != want {
			t.Fatalf("被删的对象不对：got %q want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("等不到异步删除（%q）—— 旧对象没有进异步通道", want)
	}
}

// avatarFixture 造一个账号 + 一个可用的头像后端。
func avatarFixture(t *testing.T) (*AccountService, *fakeStore, *fakeAvatarBackend, int64) {
	t.Helper()
	f := newFakeStore()
	be := newFakeAvatarBackend()
	svc := newTestService(t, f, AccountOptions{Avatar: be})
	id := seedAccount(t, f, "avatar-user", "abcd1234")
	return svc, f, be, id
}

// avatarURLFor 造一个"本桶的"头像 URL。
func avatarURLFor(n string) string {
	return "https://cdn.example.test/pr-files/2026/11/" + strings.Repeat(n, 32) + ".jpg"
}

// TestUpdateMyAvatarURLSetsAndClears 是正常路径：设值 → 清除。
func TestUpdateMyAvatarURLSetsAndClears(t *testing.T) {
	svc, f, be, id := avatarFixture(t)
	ctx := context.Background()
	url := avatarURLFor("a")

	p, err := svc.UpdateMyAvatarURL(ctx, id, &url)
	if err != nil {
		t.Fatalf("设头像：%v", err)
	}
	if p.AvatarURL == nil || *p.AvatarURL != url {
		t.Fatalf("返回的档案应带新头像：got %v want %q", p.AvatarURL, url)
	}

	f.mu.Lock()
	stored := f.users[id].AvatarURL
	f.mu.Unlock()
	if stored == nil || *stored != url {
		t.Fatalf("库里应已落值：got %v", stored)
	}
	if be.removedCount() != 0 {
		t.Fatal("第一次设头像不该删任何对象")
	}

	// 清除：nil → 回落字母头。
	p, err = svc.UpdateMyAvatarURL(ctx, id, nil)
	if err != nil {
		t.Fatalf("清除头像：%v", err)
	}
	if p.AvatarURL != nil {
		t.Fatalf("清除后档案里不该有头像：got %v", *p.AvatarURL)
	}
	f.mu.Lock()
	stored = f.users[id].AvatarURL
	f.mu.Unlock()
	if stored != nil {
		t.Fatalf("库里应已置空：got %v", *stored)
	}
}

// TestUpdateMyAvatarURLRejectsForeignURL 是安全核心（决议 #54 §8）。
func TestUpdateMyAvatarURLRejectsForeignURL(t *testing.T) {
	svc, f, _, id := avatarFixture(t)
	ctx := context.Background()

	for _, bad := range []string{
		"https://evil.example.com/pr-files/2026/11/" + strings.Repeat("a", 32) + ".jpg",
		"http://cdn.example.test/pr-files/2026/11/" + strings.Repeat("a", 32) + ".jpg",
		"not a url",
	} {
		bad := bad
		if _, err := svc.UpdateMyAvatarURL(ctx, id, &bad); !errors.Is(err, ErrAvatarURLInvalid) {
			t.Errorf("外链应被拒（%q）：got %v", bad, err)
		}
	}

	// 被拒之后库里必须还是空的 —— "校验失败"与"没改动"要一致。
	f.mu.Lock()
	stored := f.users[id].AvatarURL
	f.mu.Unlock()
	if stored != nil {
		t.Fatalf("被拒的请求不该改库：got %v", *stored)
	}
}

// TestUpdateMyAvatarURLUnavailableWithoutBackend 钉住"存储没配 → 503 的领域错误"。
//
// 这条判据的失败形态很隐蔽：若把 nil 后端当成"空操作"处理，接口会返回 200 并把
// URL 写进库 —— 于是用户看到"头像已保存"，但图片永远 404，而库里那条 URL 指向
// 一个不存在的桶。决议 #54 §9 要求这时显示 CapabilityNotice。
func TestUpdateMyAvatarURLUnavailableWithoutBackend(t *testing.T) {
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{}) // 不给 Avatar
	id := seedAccount(t, f, "no-store", "abcd1234")
	url := avatarURLFor("b")

	if _, err := svc.UpdateMyAvatarURL(context.Background(), id, &url); !errors.Is(err, ErrAvatarUnavailable) {
		t.Fatalf("未配置对象存储时应 ErrAvatarUnavailable：got %v", err)
	}

	f.mu.Lock()
	stored := f.users[id].AvatarURL
	f.mu.Unlock()
	if stored != nil {
		t.Fatalf("不可用时不该写库：got %v", *stored)
	}
}

// TestUpdateMyAvatarURLDeletesOldObject 钉住"换图后旧对象被删"。
func TestUpdateMyAvatarURLDeletesOldObject(t *testing.T) {
	svc, _, be, id := avatarFixture(t)
	ctx := context.Background()
	first, second := avatarURLFor("c"), avatarURLFor("d")

	if _, err := svc.UpdateMyAvatarURL(ctx, id, &first); err != nil {
		t.Fatalf("设第一张：%v", err)
	}
	if _, err := svc.UpdateMyAvatarURL(ctx, id, &second); err != nil {
		t.Fatalf("设第二张：%v", err)
	}
	be.waitRemoved(t, first)

	// 只删了旧的这一张（新的还在用）。
	if n := be.removedCount(); n != 1 {
		t.Fatalf("应恰好删 1 个对象：got %d", n)
	}
}

// TestUpdateMyAvatarURLRemoveFailureDoesNotFailRequest 是"删失败只记日志"。
//
// 用户的动作（换头像）已经成功了；因为清理旧垃圾失败而回 500，
// 会让他重试、而重试会再换一张图 —— 越重试垃圾越多。
func TestUpdateMyAvatarURLRemoveFailureDoesNotFailRequest(t *testing.T) {
	svc, _, be, id := avatarFixture(t)
	be.removeErr = errors.New("S3 挂了")
	ctx := context.Background()
	first, second := avatarURLFor("e"), avatarURLFor("f")

	if _, err := svc.UpdateMyAvatarURL(ctx, id, &first); err != nil {
		t.Fatalf("设第一张：%v", err)
	}
	p, err := svc.UpdateMyAvatarURL(ctx, id, &second)
	if err != nil {
		t.Fatalf("删旧图失败不该让换头像失败：%v", err)
	}
	if p.AvatarURL == nil || *p.AvatarURL != second {
		t.Fatalf("响应应带新头像：got %v", p.AvatarURL)
	}
	be.waitRemoved(t, first)
}

// TestUpdateMyAvatarURLIsIdempotent 钉住"同一个 URL 再设一次"：
// 不写库、更**不能**把刚设的那张图删掉。
func TestUpdateMyAvatarURLIsIdempotent(t *testing.T) {
	svc, _, be, id := avatarFixture(t)
	ctx := context.Background()
	url := avatarURLFor("1")

	if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
		t.Fatalf("设头像：%v", err)
	}
	if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
		t.Fatalf("重复设同一个 URL：%v", err)
	}

	// 给异步通道一点时间：若实现漏了幂等判断，这里会收到一次删除。
	select {
	case got := <-be.removedCh:
		t.Fatalf("同一个 URL 不该触发删除，却删了 %q", got)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestUpdateMyAvatarURLEmptyStringClears 钉住"空串等价于 nil"。
//
// 前端清空输入框后可能提交 `""` 而不是 `null`；两种都必须是"清除"，
// 否则库里会出现一个空串 URL —— 它既不是 NULL（显示层判 `avatarUrl != null`
// 会以为有图），又指不到任何对象（`<img src="">` 会去请求当前页面）。
func TestUpdateMyAvatarURLEmptyStringClears(t *testing.T) {
	svc, f, _, id := avatarFixture(t)
	ctx := context.Background()
	url := avatarURLFor("2")

	if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
		t.Fatalf("设头像：%v", err)
	}

	empty := "   "
	p, err := svc.UpdateMyAvatarURL(ctx, id, &empty)
	if err != nil {
		t.Fatalf("空串清除：%v", err)
	}
	if p.AvatarURL != nil {
		t.Fatalf("空串应被归一成 nil：got %q", *p.AvatarURL)
	}
	f.mu.Lock()
	stored := f.users[id].AvatarURL
	f.mu.Unlock()
	if stored != nil {
		t.Fatalf("库里应是 NULL 而不是空串：got %q", *stored)
	}
}

// TestUpdateMyAvatarURLRejectsBanned 与改昵称同一条门：被封禁的账号不能改资料。
func TestUpdateMyAvatarURLRejectsBanned(t *testing.T) {
	svc, f, _, id := avatarFixture(t)
	f.mu.Lock()
	f.users[id].Status = store.StatusBanned
	f.mu.Unlock()

	url := avatarURLFor("3")
	if _, err := svc.UpdateMyAvatarURL(context.Background(), id, &url); !errors.Is(err, ErrUserForbidden) {
		t.Fatalf("被封禁应 ErrUserForbidden：got %v", err)
	}
}

// TestUpdateMyAvatarURLUnknownUser 覆盖 id 不存在与非法 id 两种。
func TestUpdateMyAvatarURLUnknownUser(t *testing.T) {
	svc, _, _, _ := avatarFixture(t)
	ctx := context.Background()
	url := avatarURLFor("4")

	if _, err := svc.UpdateMyAvatarURL(ctx, 0, &url); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("id=0 应 ErrUserNotFound：got %v", err)
	}
	if _, err := svc.UpdateMyAvatarURL(ctx, 999999, &url); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("不存在的 id 应 ErrUserNotFound：got %v", err)
	}
}

// TestProfileCarriesAvatarURL 钉住 Profile / PublicProfile 都带这一列。
//
// 两个出口都要带：`/api/auth/me` 走 Profile（设置区要显示当前头像），
// `/api/users/:id` 走 PublicProfile（档案页要看别人的头像）。
func TestProfileCarriesAvatarURL(t *testing.T) {
	svc, f, _, id := avatarFixture(t)
	ctx := context.Background()
	url := avatarURLFor("5")

	if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
		t.Fatalf("设头像：%v", err)
	}

	f.mu.Lock()
	u := *f.users[id]
	f.mu.Unlock()

	if p := ProfileOf(&u); p.AvatarURL == nil || *p.AvatarURL != url {
		t.Errorf("Profile 应带头像：got %v", p.AvatarURL)
	}
	if p := PublicProfileOf(&u); p.AvatarURL == nil || *p.AvatarURL != url {
		t.Errorf("PublicProfile 应带头像：got %v", p.AvatarURL)
	}
}
