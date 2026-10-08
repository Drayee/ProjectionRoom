package model

import (
	"runtime"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// TestWireBudgetRejectsAuditAmplificationFrame 是 S-1 的核心回归测试：
// 重放审计用的那种"单帧内存放大"报文，断言它**在解码之前**就被拒，
// 并且拒绝过程本身不分配与元素数量成比例的内存。
//
// 审计证据（修复前）：一帧 4,194,300 字节（field 24 重复 1,398,100 次）
// 让 RSS 从 22 MB 涨到 3,947 MB，关闭连接后不归还。
//
// 这里不能在单测里重放 4 MB 的帧并断言 RSS —— Go 的 GC 让 RSS 断言既慢又不稳。
// 退而求其次但更确定的做法是两层断言：
//  1. 语义：同构的放大帧必须被 *wireBudgetError 拒绝，且**元素计数**是拒绝的理由
//     （证明拒绝发生在读到第 N+1 个元素时，而不是遍历完整帧之后）；
//  2. 成本：构造 200,000 个元素的帧，测量 Alloc 差值必须远小于"展开后"的量级
//     （修复前 200,000 个 members 会展开成 ~3.2 MB 的指针数组 + 每个元素的
//     MemberInfo 结构体，实测 100 倍以上；这里把阈值放宽到 16 MB）。
func TestWireBudgetRejectsAuditAmplificationFrame(t *testing.T) {
	// 与审计同一形态：field 24（members）重复。每个元素是一个嵌套消息，
	// 里面塞一个 id 字段，保证它确实是"合法的嵌套消息"而不是畸形数据。
	const elements = 200_000

	// 每个成员元素是"合法的嵌套消息"（里面有一个 id 字段），
	// 与审计那种 3 字节/元素的形态同量级 —— 关键就在于元素个数，而不是总字节数。
	member := appendStringField(nil, 1, "x")
	frame := appendStringField(nil, fEnvelopeType, "join")
	for i := 0; i < elements; i++ {
		frame = protowire.AppendTag(frame, fEnvelopeMembers, protowire.BytesType)
		frame = protowire.AppendBytes(frame, member)
	}

	budget := WireBudget{
		MaxRepeatedElements:  16384,
		MaxMembersPerMessage: 256,
		MaxFieldBytes:        1 << 20,
		MaxSegmentsPerIndex:  8192,
		MaxNestedBytes:       4 << 20,
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	state, err := ScanWireBudgetWithState(frame, budget)

	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatalf("放大帧必须被拒绝（%d 个 members 元素），但它通过了扫描", state.Elements)
	}
	var budgetErr *wireBudgetError
	if !asWireBudgetError(err, &budgetErr) {
		t.Fatalf("拒绝理由应当是预算错误，实际 %T: %v", err, err)
	}
	if budgetErr.Field != "members" {
		t.Fatalf("应当因 members 超限被拒，实际字段是 %q（%v）", budgetErr.Field, err)
	}
	if budgetErr.Limit != 256 {
		t.Fatalf("members 的单帧上限应当是配置里的 256，实际 %d", budgetErr.Limit)
	}
	// 关键断言：扫描在读到第 257 个元素时就停了，而不是遍历完 200,000 个。
	if state.Elements > 300 {
		t.Fatalf("拒绝应当发生在超限的那一刻（应 ≈257 个元素），实际扫描了 %d 个元素", state.Elements)
	}

	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("帧大小 %d 字节 / %d 个成员元素；扫描在 %d 个元素处拒绝，期间累计分配 %d 字节",
		len(frame), elements, state.Elements, alloc)
	// 放宽到 16 MB：这条断言要证明的是"没有数量级增长"，不是精确值。
	if alloc > 16<<20 {
		t.Fatalf("拒绝放大帧的过程分配过多内存：%d 字节（应当在 O(1) 量级）", alloc)
	}
}

// TestWireBudgetAcceptsNormalFrames 锁定"闸门不能挡住正常帧"：
// 真实的 join / member-list / metrics / index 帧都必须原样通过。
func TestWireBudgetAcceptsNormalFrames(t *testing.T) {
	budget := WireBudget{} // 全用默认值

	// 1) 正常 join：members 空、media_index 空。
	join := appendStringField(nil, fEnvelopeType, "join")
	join = appendStringField(join, 8, "secret") // password
	assertAccepted(t, budget, join, "正常 join")

	// 2) 满员房间的成员表：256 个成员（默认房间硬上限只有 16，这是极端余量）。
	members := 256
	msg := appendStringField(nil, 1, "member-id")
	frame := appendStringField(nil, fEnvelopeType, "member-list")
	for i := 0; i < members; i++ {
		frame = protowire.AppendTag(frame, fEnvelopeMembers, protowire.BytesType)
		frame = protowire.AppendBytes(frame, msg)
	}
	assertAccepted(t, budget, frame, "256 个成员的成员表")

	// 3) 索引帧：把 8192 个分片（默认单字段上限）塞进 media_index.segments。
	seg := appendStringField(nil, 2, "pack-0001.bin")
	var idx []byte
	for i := 0; i < 8192; i++ {
		idx = protowire.AppendTag(idx, fMediaIndexSegments, protowire.BytesType)
		idx = protowire.AppendBytes(idx, seg)
	}
	indexFrame := appendStringField(nil, fEnvelopeType, "media-index")
	indexFrame = protowire.AppendTag(indexFrame, fEnvelopeMediaIndex, protowire.BytesType)
	indexFrame = protowire.AppendBytes(indexFrame, idx)
	assertAccepted(t, budget, indexFrame, "8192 片的索引帧")

	// 4) 指标帧（只有标量字段）。
	metrics := appendStringField(nil, fEnvelopeType, "metrics")
	metrics = protowire.AppendTag(metrics, 20, protowire.BytesType)
	metrics = protowire.AppendBytes(metrics, nil)
	assertAccepted(t, budget, metrics, "metrics 帧")
}

// TestWireBudgetRejectsOversizedHaveAndPassword 覆盖"非 repeated 的无界分配"：
// have 位图与超长字符串同样必须有上限。
func TestWireBudgetRejectsOversizedHaveAndPassword(t *testing.T) {
	budget := WireBudget{MaxFieldBytes: 1024}

	// have(29) 超过单字段上限。
	big := make([]byte, 2048)
	have := appendStringField(nil, fEnvelopeType, "chunks-report")
	have = protowire.AppendTag(have, fEnvelopeHave, protowire.BytesType)
	have = protowire.AppendBytes(have, big)
	if err := ScanWireBudget(have, budget); err == nil {
		t.Fatal("2 KiB 的 have 应当被 1 KiB 的单字段上限拒绝")
	}

	// payload(9) 同理。
	payload := appendStringField(nil, fEnvelopeType, "signal")
	payload = protowire.AppendTag(payload, fEnvelopePayload, protowire.BytesType)
	payload = protowire.AppendBytes(payload, big)
	if err := ScanWireBudget(payload, budget); err == nil {
		t.Fatal("2 KiB 的 payload 应当被拒绝")
	}

	// type(1) 有自己的 64 字节上限（未知类型名没有理由很长）。
	long := appendStringField(nil, fEnvelopeType, string(make([]byte, 65)))
	if err := ScanWireBudget(long, budget); err == nil {
		t.Fatal("65 字节的 type 应当被拒绝")
	}
}

// TestWireBudgetIgnoresUnknownFields 锁定"未知字段不递归、不计数"：
// 这样客户端比服务端新时仍然可用，而攻击者也拿不到放大效果。
func TestWireBudgetIgnoresUnknownFields(t *testing.T) {
	budget := WireBudget{MaxRepeatedElements: 4}

	frame := appendStringField(nil, fEnvelopeType, "join")
	// 未知字段编号 900，重复 100 次：既不该被计数，也不该被拒绝。
	for i := 0; i < 100; i++ {
		frame = protowire.AppendTag(frame, 900, protowire.BytesType)
		frame = protowire.AppendBytes(frame, []byte("unknown"))
	}
	if err := ScanWireBudget(frame, budget); err != nil {
		t.Fatalf("未知字段不应参与预算（它没有对应的结构体字段，不会放大）：%v", err)
	}

	// 对照：已知的 members 同样重复 10 次时，必须被 4 个元素的上限拒绝。
	msg := appendStringField(nil, 1, "id")
	frame2 := appendStringField(nil, fEnvelopeType, "member-list")
	for i := 0; i < 10; i++ {
		frame2 = protowire.AppendTag(frame2, fEnvelopeMembers, protowire.BytesType)
		frame2 = protowire.AppendBytes(frame2, msg)
	}
	if err := ScanWireBudget(frame2, budget); err == nil {
		t.Fatal("重复 10 次的 members 应当被 4 个元素的上限拒绝")
	}
}

// TestWireBudgetSingularFieldTwiceIsRejected 锁定"单值字段出现两次即畸形"。
func TestWireBudgetSingularFieldTwiceIsRejected(t *testing.T) {
	budget := WireBudget{}
	frame := appendStringField(nil, fEnvelopeType, "join")
	inner := appendStringField(nil, 1, "id")
	for i := 0; i < 2; i++ {
		frame = protowire.AppendTag(frame, fEnvelopeMember, protowire.BytesType)
		frame = protowire.AppendBytes(frame, inner)
	}
	if err := ScanWireBudget(frame, budget); err == nil {
		t.Fatal("member(23) 出现两次应当被拒")
	}
}

// TestWireBudgetDefaults 锁定默认值（它们是 S-1 的"上限依据"的机器可验证版本）。
func TestWireBudgetDefaults(t *testing.T) {
	b := WireBudget{}.withDefaults()
	if b.MaxRepeatedElements != 16384 {
		t.Fatalf("整帧元素预算默认应为 16384，实际 %d", b.MaxRepeatedElements)
	}
	if b.MaxMembersPerMessage != 256 {
		t.Fatalf("members 单字段上限默认应为 256，实际 %d", b.MaxMembersPerMessage)
	}
	if b.MaxFieldBytes != 1<<20 {
		t.Fatalf("单字段字节上限默认应为 1 MiB，实际 %d", b.MaxFieldBytes)
	}
	if b.MaxSegmentsPerIndex != 8192 {
		t.Fatalf("segments 上限默认应为 8192，实际 %d", b.MaxSegmentsPerIndex)
	}

	// 字段清单必须覆盖审计点名的那些 repeated 字段。
	fields := ActiveWireFields()
	for _, want := range []string{"24:members", "25:media_index", "29:have", "9:payload", "23:member", "27:topology"} {
		found := false
		for _, f := range fields {
			if f == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("字段清单缺少 %s：%v", want, fields)
		}
	}
	if len(RepeatedFields()) < 5 {
		t.Fatalf("repeated 字段清单至少应列出 5 项，实际 %d 项", len(RepeatedFields()))
	}
}

// —— 测试辅助 ——

func assertAccepted(t *testing.T, budget WireBudget, frame []byte, desc string) {
	t.Helper()
	if err := ScanWireBudget(frame, budget); err != nil {
		t.Fatalf("%s 不应被拒：%v（帧 %d 字节）", desc, err, len(frame))
	}
}

// appendStringField 追加一个 length-delimited 字段（string 与 bytes 的线上形态相同）。
func appendStringField(dst []byte, num protowire.Number, s string) []byte {
	dst = protowire.AppendTag(dst, num, protowire.BytesType)
	return protowire.AppendString(dst, s)
}

// appendField 是 appendStringField 的占位版本（构造测试数据时的中间步骤）。
func appendField(dst []byte, num protowire.Number, raw []byte) []byte {
	dst = protowire.AppendTag(dst, num, protowire.BytesType)
	return protowire.AppendBytes(dst, raw)
}

// asWireBudgetError 是 errors.As 的薄封装（避免测试文件多一个 import 的噪音）。
func asWireBudgetError(err error, target **wireBudgetError) bool {
	if e, ok := err.(*wireBudgetError); ok {
		*target = e
		return true
	}
	return false
}
