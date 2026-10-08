package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	// database/sql 的 pgx 驱动：只给 TestMain 的"整包串行化锁"用。
	_ "github.com/jackc/pgx/v5/stdlib"
)

// —— 测试库的连接与安全护栏 ————————————————————————————————————————————

// testDSNEnv 是测试库连接串的环境变量名。
//
// 约定：**未设置时 Skip**（无库环境保持绿，不假绿），设置后必须真实通过。
const testDSNEnv = "PR_TEST_DB_DSN"

// —— 整包并发串行化 ————————————————————————————————————————————————

// testDBSerializationKey 是"整包测试串行化"用的会话级咨询锁 key（"pstore" 的 ASCII）。
const testDBSerializationKey int64 = 0x7073_746F_7265

// testDBLockPatience 是等锁的总耐心：超过就放弃等待并大声提醒。
const testDBLockPatience = 15 * time.Minute

// TestMain 在跑用例之前先抢一把测试库的会话级咨询锁。
//
// 为什么需要它：本包所有用例共用**同一张**测试库，而同一份代码可能被并发执行
// （协调者跑 `go test ./...` 的同时，另一个任务在跑 `go test ./internal/store/`）。
// 两个进程会互相 TRUNCATE 对方的数据，表现出来是一堆"记录不存在"的假失败 ——
// 那是环境竞争，不是代码缺陷，但会把证据污染掉。拿锁把并发运行串行化，
// 比在每条断言里容忍脏数据诚实得多（锁在连接关闭时自动释放，进程被杀也不会泄漏）。
func TestMain(m *testing.M) {
	code, err := runSerialized(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "store 测试：%v\n", err)
	}
	os.Exit(code)
}

// brokenIsolation 是"隔离建立不起来"时的统一出口：不打任何用例，直接红。
//
// 为什么不是"照跑，让用例自己失败"：没有隔离的真库用例会互相清表，
// 失败的用例名与真实原因毫无关系（"记录不存在"），排查成本远高于直接红一次。
func brokenIsolation(what string, err error) (int, error) {
	fmt.Fprintf(os.Stderr, "store 测试：%s，拒绝在无隔离状态下运行真库用例\n", what)
	return 1, fmt.Errorf("%s：%w", what, err)
}

func runSerialized(m *testing.M) (int, error) {
	dsn := strings.TrimSpace(os.Getenv(testDSNEnv))
	name, nameErr := dbNameFromDSN(dsn)
	if dsn == "" || nameErr != nil || !strings.HasSuffix(name, "_test") {
		// 没有库、或者护栏不通过：直接开跑。需要库的用例会自己 Skip，
		// 护栏不通过时由 requireTestDB 给出更明确的 Fatal 信息。
		return m.Run(), nil
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		// 连"锁会话"都建不起来时**不跑**用例：无隔离的真库用例会产生一堆假失败，
		// 把真实缺陷淹在噪声里（这条路径就踩过：隧道掉了，42 条用例全红）。
		return brokenIsolation("打开测试库失败", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return brokenIsolation("取测试库连接失败", err)
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(testDBLockPatience)
	toldWaiting := false
	for {
		var locked bool
		// 注意占位符：这里走 database/sql 的 pgx 驱动（不是 GORM），必须是 $1。
		// 用 "?" 会被 PG 直接判语法错误 —— 那会让整把锁静默失效，
		// 于是两个进程照样互相清表（这坑踩过一次，见并发验证的说明）。
		const lockQ = `SELECT pg_try_advisory_lock($1)`
		if err := conn.QueryRowContext(ctx, lockQ, testDBSerializationKey).Scan(&locked); err != nil {
			return brokenIsolation("获取测试库隔离锁失败", err)
		}
		if locked {
			break
		}
		if !toldWaiting {
			fmt.Fprintf(os.Stdout, "store 测试：测试库正被另一份测试进程占用，等待其释放（最多 %s）…\n", testDBLockPatience)
			toldWaiting = true
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr,
				"（可用 `SELECT pg_advisory_unlock(%d);` 手动释放，或结束持有它的测试进程）\n",
				testDBSerializationKey)
			return brokenIsolation("等待测试库隔离锁超时", fmt.Errorf("超过 %s 仍被占用", testDBLockPatience))
		}
		time.Sleep(5 * time.Second)
	}

	code := m.Run()

	// 显式解锁（连接关闭时也会释放，但显式释放能让等待方立刻拿到）。
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, testDBSerializationKey); err != nil {
		return code, fmt.Errorf("释放测试库锁失败：%w", err)
	}
	return code, nil
}

// testDSN 取测试库 DSN，并施加一条硬护栏：库名必须以 _test 结尾。
//
// 为什么这是硬门槛而不是提示：本包用例会用
// `TRUNCATE ... RESTART IDENTITY CASCADE` 清表。只要有人把 PR_TEST_DB_DSN 指向
// 生产库 projectionroom，一次 `go test` 就能把真实用户数据清空。
// 所以"确认库名"失败时一律 Fatal（fail closed）—— 不 Skip、不警告、不猜测。
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(testDSNEnv))
	if dsn == "" {
		t.Skipf("未设置 %s，跳过需要真实 PostgreSQL 的用例", testDSNEnv)
	}
	name, err := dbNameFromDSN(dsn)
	if err != nil {
		t.Fatalf("无法从 %s 解析库名：%v（拒绝在确认不了库名的连接上跑会清表的用例）", testDSNEnv, err)
	}
	if !strings.HasSuffix(name, "_test") {
		t.Fatalf("%s 指向的库名 %q 不以 _test 结尾，拒绝执行会 TRUNCATE 的用例", testDSNEnv, name)
	}
	return dsn
}

// dbTestMu 是本包"真库用例"的进程内串行化闸门（防御性）。
//
// 当前用例全是串行的（没有任何 t.Parallel），所以它永远不会真的争用；
// 它的价值在于**加锁的代价是零、而忘记串行化的代价是整包假失败**：
// 将来有人给某个真库用例加上 t.Parallel()，或者用 `-parallel N` 跑，
// 持锁到用例结束就保证了两条 TRUNCATE 不会交错。
var dbTestMu sync.Mutex

// requireTestDB 打开测试库、补齐迁移、清空数据，返回可用的 Store。
//
// 两层隔离，缺一不可：
//  1. **跨进程**：TestMain 里的会话级咨询锁，防"同一份代码被并发跑"
//     （例如协调者跑 `go test ./...` 的同时另一个任务跑 `go test ./internal/store/`）；
//  2. **进程内**：dbTestMu 持锁到用例结束，防将来有人加 t.Parallel()。
func requireTestDB(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()

	// 先过 DSN 护栏（要 Skip 就不必抢锁）。
	dsn := testDSN(t)
	dbTestMu.Lock()
	// 注册顺序决定了执行顺序：本行先注册 → 最后执行 → 锁覆盖整个用例（含 Close）。
	t.Cleanup(dbTestMu.Unlock)

	// 通过 SSH 隧道连远程 PostgreSQL 时，隧道刚建立的头几秒可能瞬时失败，
	// 给 3 次机会再判失败（本轮不引入重试库，就 3 行）。
	var (
		s   *Store
		err error
	)
	for attempt := 1; attempt <= 3; attempt++ {
		s, err = Open(ctx, dsn)
		if err == nil {
			break
		}
		t.Logf("第 %d 次连接测试库失败：%v", attempt, err)
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("连接测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.Migrate(ctx); err != nil {
		t.Fatalf("迁移测试库失败：%v", err)
	}
	truncateAll(t, s)
	return s
}

// truncateAll 清空业务表。
//
// 保留 schema_migrations：它是迁移账本，清掉会让"已应用"这件事消失，
// 迁移用例的断言也就失去意义。
func truncateAll(t *testing.T, s *Store) {
	t.Helper()
	const stmt = `TRUNCATE TABLE admin_audit, rooms_meta, sessions, users RESTART IDENTITY CASCADE`
	if err := s.DB().Exec(stmt).Error; err != nil {
		t.Fatalf("清空测试库失败：%v", err)
	}
}

// dropPhaseOneTables 删掉本期建的全部表（含账本），仅供迁移用例回到"空库"。
// 只会跑在 _test 库上：requireTestDB 已经拦住了库名不以 _test 结尾的情况。
func dropPhaseOneTables(t *testing.T, s *Store) {
	t.Helper()
	const stmt = `DROP TABLE IF EXISTS admin_audit, rooms_meta, sessions, users, schema_migrations CASCADE`
	if err := s.DB().Exec(stmt).Error; err != nil {
		t.Fatalf("清空测试库结构失败：%v", err)
	}
}

// tableExists 用 to_regclass 判断表是否存在（参数绑定，不拼 SQL）。
func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var exists bool
	if err := s.DB().Raw(`SELECT to_regclass(?) IS NOT NULL`, "public."+name).Scan(&exists).Error; err != nil {
		t.Fatalf("查询表 %s 是否存在失败：%v", name, err)
	}
	return exists
}

// —— 小工具 ————————————————————————————————————————————————————————

// hexHash 造一个真实的 sha256 十六进制串（token_hash 列的合法形态）。
func hexHash(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// seedUser 建一个可用的测试用户。
func seedUser(t *testing.T, s *Store, username string) *User {
	t.Helper()
	u := &User{
		Username:     username,
		DisplayName:  "测试：" + username,
		PasswordHash: "bcrypt-placeholder-" + username,
	}
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("建测试用户 %s 失败：%v", username, err)
	}
	return u
}

// ensureTimeClose 断言两个时间点表示同一个瞬间（timestamptz 的分辨率是微秒，
// 所以允许 µs 级差异）。用差值比较，避免被时区/位置（Asia/Shanghai vs UTC）干扰。
func ensureTimeClose(t *testing.T, what string, want, got time.Time) {
	t.Helper()
	if diff := got.Sub(want); diff > time.Microsecond || diff < -time.Microsecond {
		t.Fatalf("%s 不是同一个瞬间：want=%s got=%s（差 %s）", what, want.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano), diff)
	}
}

// —— DSN 解析：护栏的核心 ——————————————————————————————————————————————

// TestDBNameFromDSN 覆盖两种 DSN 形态与失败路径。
// 这个函数是"不许清生产库"这条护栏的判定依据，所以失败路径比成功路径更重要。
func TestDBNameFromDSN(t *testing.T) {
	cases := []struct {
		name    string
		dsn     string
		want    string
		wantErr bool
	}{
		{
			name: "关键字形态（本项目测试库的真实形态）",
			dsn:  "host=127.0.0.1 port=5433 user=pr_app password=secret dbname=projectionroom_test sslmode=disable TimeZone=Asia/Shanghai",
			want: "projectionroom_test",
		},
		{name: "关键字形态_允许 database 别名", dsn: "host=h database=projectionroom_test", want: "projectionroom_test"},
		{name: "关键字形态_键名大小写不敏感", dsn: "host=h DBNAME=projectionroom_test", want: "projectionroom_test"},
		{name: "关键字形态_单引号值里的空格", dsn: "host=h dbname='a b_test'", want: "a b_test"},
		{name: "URI 形态", dsn: "postgres://pr_app:secret@127.0.0.1:5433/projectionroom_test?sslmode=disable", want: "projectionroom_test"},
		{name: "URI 形态_postgresql 前缀", dsn: "postgresql://h:5433/projectionroom_test", want: "projectionroom_test"},
		{name: "缺少 dbname", dsn: "host=127.0.0.1 port=5433 user=pr_app", wantErr: true},
		{name: "dbname 为空", dsn: "host=127.0.0.1 dbname=", wantErr: true},
		{name: "URI 缺少库名", dsn: "postgres://127.0.0.1:5433/", wantErr: true},
		{name: "空串", dsn: "", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := dbNameFromDSN(c.dsn)
			if c.wantErr {
				if err == nil {
					t.Fatalf("应该报错，实际拿到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if got != c.want {
				t.Fatalf("库名解析错误：want=%q got=%q", c.want, got)
			}
		})
	}
}

// —— Open / Ping / Close ——————————————————————————————————————————————

// TestOpenRejectsEmptyDSN：PR_DB_DSN 为空 = 账号能力关闭（T1 的默认形态），
// 必须是一个可判定的哨兵错误，而不是"连接到一个空地址然后超时"。
func TestOpenRejectsEmptyDSN(t *testing.T) {
	for _, dsn := range []string{"", "   ", "\t\n"} {
		s, err := Open(context.Background(), dsn)
		if !errors.Is(err, ErrNoDSN) {
			t.Fatalf("dsn=%q 应该返回 ErrNoDSN，实际 %v", dsn, err)
		}
		if s != nil {
			t.Fatalf("dsn=%q 失败时不该返回 Store", dsn)
		}
	}
}

// TestOpenFailsOnUnreachableDatabase：连不上时错误必须来自连接本身
// （不能被误判成 ErrNoDSN —— 那会让"配置了但连不上"变成静默关闭账号）。
func TestOpenFailsOnUnreachableDatabase(t *testing.T) {
	dsn := "host=127.0.0.1 port=1 user=nobody password=none dbname=nothing_test sslmode=disable connect_timeout=2"
	s, err := Open(context.Background(), dsn)
	if err == nil {
		_ = s.Close()
		t.Fatalf("连不上的库不该 Open 成功")
	}
	if errors.Is(err, ErrNoDSN) {
		t.Fatalf("错误不该是 ErrNoDSN，实际 %v", err)
	}
	if s != nil {
		t.Fatalf("失败时不该返回 Store")
	}
}

// TestOpenHonorsCanceledContext：Open 必须尊重传入的 ctx（gorm 自带的自动 ping
// 用的是 Background，所以这里刻意关掉它、自己 ping）。
func TestOpenHonorsCanceledContext(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s, err := Open(ctx, dsn)
	if err == nil {
		_ = s.Close()
		t.Fatalf("ctx 已取消时不该连接成功")
	}
}

// TestOpenPingClose：真实库上的连接、探活与关闭。
func TestOpenPingClose(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping 失败：%v", err)
	}
	if s.DB() == nil {
		t.Fatalf("DB() 不该为 nil")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败：%v", err)
	}
	if err := s.Ping(ctx); err == nil {
		t.Fatalf("关闭之后 ping 必须失败")
	}
}

// TestStoreWriterAttachment：Store 持有事件写入器的挂载点（§9 的装配面）。
func TestStoreWriterAttachment(t *testing.T) {
	s := requireTestDB(t)
	if s.EventWriter() != nil {
		t.Fatalf("未挂载时 EventWriter() 应该是 nil")
	}
	w := NewWriter(s.DB(), WriterOptions{QueueSize: 4, MaxRetries: 0})
	t.Cleanup(func() { _ = w.Close(time.Second) })

	s.AttachWriter(w)
	if s.EventWriter() != w {
		t.Fatalf("挂载后 EventWriter() 应该返回同一个写入器")
	}
	s.AttachWriter(nil)
	if s.EventWriter() != nil {
		t.Fatalf("挂载 nil 表示本进程不写事件")
	}
}

// TestPackagePrivateHelpers 覆盖几个纯函数工具，避免"靠用例间接验证"。
func TestPackagePrivateHelpers(t *testing.T) {
	if got := canonicalUsername("  Alice  "); got != "alice" {
		t.Fatalf("用户名归一化错误：%q", got)
	}
	if got := likePattern("  "); got != "" {
		t.Fatalf("空白搜索应该退化成不过滤，实际 %q", got)
	}
	if got := likePattern("a_b%"); got != `%a\_b\%%` {
		t.Fatalf("通配符转义错误：%q", got)
	}
	if got := trimControl("  1.2.3.4\n"); got != "1.2.3.4" {
		t.Fatalf("trimControl 错误：%q", got)
	}
	if _, err := canonicalTokenHash(strings.Repeat("z", 64)); err == nil {
		t.Fatalf("非十六进制字符应该被拒绝")
	}
	if _, err := canonicalTokenHash("short"); err == nil {
		t.Fatalf("长度不对应该被拒绝")
	}
}

// —— fmt/unicode/url 的用途 ————————————————————————————————————————————
// 三个包只服务于 dbNameFromDSN（见文件末尾）：fmt 造错误、net/url 解析 URI 形态、
// unicode 在 splitDSNFields 里判空白。

// —— DSN 解析：护栏的判定依据 ——————————————————————————————————————————

// dbNameFromDSN 从 DSN 里取出库名，支持 PostgreSQL 的两种合法形态：
// 关键字/值形态（host=... dbname=...）与 URI 形态（postgres://...）。
//
// 它只为测试护栏服务（所以放在 _test.go 里，不进二进制）。判定必须"宁可判不出来"：
// 解析不出库名时返回错误，调用方据此 Fatal —— 绝不猜"大概是个测试库"。
func dbNameFromDSN(dsn string) (string, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "", errors.New("DSN 为空")
	}

	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("解析 URI 形态 DSN 失败：%w", err)
		}
		name := strings.TrimPrefix(u.Path, "/")
		if name == "" {
			return "", errors.New("URI 形态 DSN 里没有库名")
		}
		return name, nil
	}

	for _, field := range splitDSNFields(dsn) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		if !strings.EqualFold(key, "dbname") && !strings.EqualFold(key, "database") {
			continue
		}
		value = strings.Trim(value, `'"`)
		if value == "" {
			return "", errors.New("关键字形态 DSN 的 dbname 为空")
		}
		return value, nil
	}
	return "", errors.New("DSN 里找不到 dbname")
}

// splitDSNFields 按空白切分关键字形态 DSN，但尊重单引号包裹的值
// （PostgreSQL 允许 dbname='a b'）。不支持引号内的反斜杠转义：那属于
// libpq 的边缘语法，这里判错的方向是"取不到库名 → Fatal"，是安全的一侧。
func splitDSNFields(dsn string) []string {
	var (
		fields  []string
		cur     strings.Builder
		inQuote bool
	)
	for _, r := range dsn {
		switch {
		case r == '\'':
			inQuote = !inQuote
			cur.WriteRune(r)
		case unicode.IsSpace(r) && !inQuote:
			if cur.Len() > 0 {
				fields = append(fields, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		fields = append(fields, cur.String())
	}
	return fields
}
