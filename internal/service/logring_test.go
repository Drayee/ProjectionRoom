package service

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
)

func TestLogRingKeepsNewestAndCountsDropped(t *testing.T) {
	r := NewLogRing(3)
	for i := 1; i <= 5; i++ {
		if _, err := r.Write([]byte(fmt.Sprintf("line%d\n", i))); err != nil {
			t.Fatalf("Write 报错：%v", err)
		}
	}
	if r.Len() != 3 {
		t.Fatalf("容量 3 应只保留 3 条，实际 %d", r.Len())
	}
	got, total := r.Snapshot(10, 0)
	if total != 3 {
		t.Fatalf("total 应为 3，实际 %d", total)
	}
	// 从最新往旧：line5, line4, line3
	want := []string{"line5", "line4", "line3"}
	if len(got) != len(want) {
		t.Fatalf("期望 %d 条，实际 %d（%v）", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条期望 %q，实际 %q（全部：%v）", i, want[i], got[i], got)
		}
	}
	all, dropped := r.Stats()
	if all != 5 || dropped != 2 {
		t.Fatalf("累计应为 5、丢弃应为 2，实际 %d/%d", all, dropped)
	}
}

func TestLogRingSplitsLinesAcrossWrites(t *testing.T) {
	r := NewLogRing(10)
	// 一行被拆成两次写：必须拼成 "ab" 而不是两条
	_, _ = r.Write([]byte("a"))
	_, _ = r.Write([]byte("b\nc\n"))
	got, _ := r.Snapshot(10, 0)
	if len(got) != 2 || got[1] != "ab" || got[0] != "c" {
		t.Fatalf("期望 [c, ab]，实际 %v", got)
	}
}

func TestLogRingSanitizesControlChars(t *testing.T) {
	r := NewLogRing(10)
	// \r 能覆盖终端/页面里前面的内容（日志伪造），\x00 与 \x1b 同理；\t 保留
	_, _ = r.Write([]byte("ok\rhidden\nhas\x00nul\x1besc\nkeep\ttab\n"))
	got, _ := r.Snapshot(10, 0)
	if len(got) != 3 {
		t.Fatalf("期望 3 条，实际 %d（%v）", len(got), got)
	}
	if got[2] != "okhidden" {
		t.Errorf("\\r 应被剥掉（拼接而非覆盖），实际 %q", got[2])
	}
	if got[1] != "hasnulesc" {
		t.Errorf("控制字符应被丢弃，实际 %q", got[1])
	}
	if got[0] != "keep\ttab" {
		t.Errorf("制表符应保留，实际 %q", got[0])
	}
}

func TestLogRingTruncatesVeryLongLine(t *testing.T) {
	r := NewLogRing(10)
	long := strings.Repeat("x", LogRingMaxLineBytes+500)
	if _, err := r.Write([]byte(long + "\n")); err != nil {
		t.Fatalf("Write 报错：%v", err)
	}
	got, _ := r.Snapshot(1, 0)
	if len(got) != 1 {
		t.Fatalf("期望 1 条，实际 %d", len(got))
	}
	if !strings.HasSuffix(got[0], logRingTruncatedSuffix) {
		t.Fatalf("超长行应带截断标记，实际尾部 %q", got[0][len(got[0])-16:])
	}
	if len(got[0]) > LogRingMaxLineBytes+len(logRingTruncatedSuffix) {
		t.Fatalf("截断后长度仍超限：%d", len(got[0]))
	}
}

func TestLogRingDoesNotGrowOnNeverEndingLine(t *testing.T) {
	r := NewLogRing(4)
	// 一条永不换行的输出来源：必须被切成上限长度，不能无限吃内存
	for i := 0; i < 8; i++ {
		_, _ = r.Write([]byte(strings.Repeat("y", LogRingMaxLineBytes)))
	}
	if r.Len() == 0 {
		t.Fatal("超长无换行输出应当被切成行存入，实际一条都没有")
	}
	if got, _ := r.Snapshot(100, 0); len(got) > 4 {
		t.Fatalf("保留条数不应超过容量，实际 %d", len(got))
	}
}

func TestLogRingSnapshotPagination(t *testing.T) {
	r := NewLogRing(100)
	for i := 1; i <= 10; i++ {
		_, _ = r.Write([]byte(fmt.Sprintf("l%02d\n", i)))
	}
	page1, total := r.Snapshot(3, 0)
	if total != 10 || len(page1) != 3 || page1[0] != "l10" || page1[2] != "l08" {
		t.Fatalf("第一页应为 l10,l09,l08，实际 %v（total=%d）", page1, total)
	}
	page2, _ := r.Snapshot(3, 3)
	if len(page2) != 3 || page2[0] != "l07" {
		t.Fatalf("第二页应从 l07 开始，实际 %v", page2)
	}
	// 越界 offset 返回空切片而不是报错（日志在持续写入，页码过期是常态）
	empty, _ := r.Snapshot(3, 99)
	if len(empty) != 0 {
		t.Fatalf("越界 offset 应返回空，实际 %v", empty)
	}
	// 非正 limit 用默认值，不该返回空
	def, _ := r.Snapshot(0, 0)
	if len(def) != 10 {
		t.Fatalf("limit<=0 应取默认值并把 10 条都返回，实际 %d", len(def))
	}
}

func TestLogRingConcurrentWritesAreSafe(t *testing.T) {
	r := NewLogRing(64)
	const writers, perWriter = 16, 200
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_, _ = r.Write([]byte(fmt.Sprintf("w%d-%d\n", id, i)))
			}
		}(w)
	}
	wg.Wait()

	total, dropped := r.Stats()
	if total != writers*perWriter {
		t.Fatalf("累计写入应为 %d，实际 %d", writers*perWriter, total)
	}
	if r.Len() != 64 {
		t.Fatalf("应刚好填满容量 64，实际 %d", r.Len())
	}
	if dropped != int64(writers*perWriter-64) {
		t.Fatalf("丢弃数应为 %d，实际 %d", writers*perWriter-64, dropped)
	}
	// 并发下也要能安全快照
	got, _ := r.Snapshot(1000, 0)
	if len(got) != 64 {
		t.Fatalf("快照条数应为 64，实际 %d", len(got))
	}
}

func TestInstallLogRingKeepsStderrAndRestores(t *testing.T) {
	before := log.Writer()
	r := NewLogRing(10)
	restore := InstallLogRing(r)
	defer restore()

	log.Print("hello-ring")
	got, _ := r.Snapshot(10, 0)
	if len(got) != 1 || !strings.Contains(got[0], "hello-ring") {
		t.Fatalf("log.Print 应进入环形缓冲，实际 %v", got)
	}
	restore()
	if log.Writer() != before {
		t.Fatal("restore 必须把 log 输出恢复原样，否则测试会污染全局状态")
	}
	// 幂等：nil 环形缓冲不该 panic（账号能力关闭时管理端仍可启动）
	InstallLogRing(nil)()
}
