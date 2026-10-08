package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/usecase"
)

// TestClientIPIgnoresForgedXFFFromUntrustedSource 锁定 S-3/S-11 限速的信任边界。
//
// 背景：gin 默认 TrustedProxies 是"全信任"，此时 c.ClientIP() 取 X-Forwarded-For 的
// 最左值 —— 而 XFF 是请求方随便写的。任何能直连本端口的路径都能用
// `X-Forwarded-For: <随机 IP>` 让每次请求都算成"新 IP"，把每 IP 建房限速与
// 每 IP+房间码 join 失败退避全部绕过（令牌桶永远是满的）。
//
// 收紧后的语义（NewRouter 里的 SetTrustedProxies(["127.0.0.1","::1"])）：
//   - 直连来源是回环（本机反代）时采信 XFF —— 公网 → nginx → 本服务 能拿到真实客户端 IP；
//   - 直连来源不是回环时**忽略** XFF，用 TCP 对端地址 —— 直连伪造 XFF 拿不到好处。
func TestClientIPIgnoresForgedXFFFromUntrustedSource(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"127.0.0.1", "::1"}); err != nil {
		t.Fatalf("配置信任代理失败: %v", err)
	}

	var got string
	r.GET("/probe", func(c *gin.Context) { got = c.ClientIP() })

	const forged = "1.2.3.4"

	t.Run("未受信来源的 XFF 被忽略", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.RemoteAddr = "8.8.8.8:54321"
		req.Header.Set("X-Forwarded-For", forged)
		r.ServeHTTP(httptest.NewRecorder(), req)

		if got == forged {
			t.Fatal("伪造成功：限速可被一行 X-Forwarded-For 绕过")
		}
		if got != "8.8.8.8" {
			t.Fatalf("未受信来源应使用 TCP 对端地址 8.8.8.8，实际 %q", got)
		}
	})

	t.Run("受信本机代理的 XFF 被采信", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("X-Forwarded-For", forged)
		r.ServeHTTP(httptest.NewRecorder(), req)

		if got != forged {
			t.Fatalf("本机反代传来的 XFF 应被采信（否则反代后所有请求算作同一 IP），实际 %q", got)
		}
	})

	t.Run("IPv6 回环也算受信", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.RemoteAddr = "[::1]:54321"
		req.Header.Set("X-Forwarded-For", forged)
		r.ServeHTTP(httptest.NewRecorder(), req)

		if got != forged {
			t.Fatalf("::1 回环来自本机代理，XFF 应被采信，实际 %q", got)
		}
	})

	t.Run("无 XFF 时用 TCP 对端", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.RemoteAddr = "203.0.113.9:12345"
		r.ServeHTTP(httptest.NewRecorder(), req)

		if got != "203.0.113.9" {
			t.Fatalf("无 XFF 时应使用 TCP 对端地址，实际 %q", got)
		}
	})
}

// TestCreateRoomLimitersResistForgedXFF 是这条修复真正要证明的事：
// **从不受信来源直连时，一行伪造的 X-Forwarded-For 不能把每 IP 建房限速刷掉。**
//
// 用真实 NewRouter 组装的引擎（因此信任代理配置就是生产配置），但用
// httptest.NewRequest 精确控制 TCP 对端地址（RemoteAddr）—— 这样才能同时演
// "直连"（8.8.8.8）与"本机反代"（127.0.0.1）两种形态。
// （本包的 TestMain 已把 ICE 探测器替换为离线实现，所以这里不会碰网络。）
func TestCreateRoomLimitersResistForgedXFF(t *testing.T) {
	cfg := config.Default()
	cfg.IPC.CreatePerMinute = 1
	cfg.IPC.CreateBurst = 1
	cfg.Room.MaxRooms = 100

	engine := newRouterForTest(t, cfg)

	post := func(remoteAddr, xff string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/rooms", nil)
		req.RemoteAddr = remoteAddr
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code
	}

	// 直连来源（8.8.8.8）连续 3 次，每次换一个伪造 XFF。
	// XFF 被忽略 → 它们落在**同一个桶**：第 1 次 200，之后必然 429。
	for i, forged := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
		want := http.StatusTooManyRequests
		if i == 0 {
			want = http.StatusOK
		}
		if got := post("8.8.8.8:1234", forged); got != want {
			t.Fatalf("直连来源伪造 XFF=%s 时第 %d 次应当 %d，实际 %d —— "+
				"说明伪造的 XFF 参与了分桶，限速被绕过", forged, i+1, want, got)
		}
	}

	// 对照：换一个**真正的**来源 IP（同样是直连）应当有独立令牌桶。
	if got := post("9.9.9.9:1234", ""); got != http.StatusOK {
		t.Fatalf("不同真实来源应各有独立桶（第 1 次应 200），实际 %d", got)
	}
}

// newRouterForTest 用生产装配路径起一个引擎（含 SetTrustedProxies 与限速中间件）。
func newRouterForTest(t *testing.T, cfg *config.Config) *gin.Engine {
	t.Helper()

	hub, cleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := usecase.NewManager(cfg, hub)
	engine := NewRouter(cfg, hub, rooms, nil)
	t.Cleanup(func() {
		rooms.Stop()
		cleanup()
	})
	return engine
}

// TestIsLoopbackAddr 锁定"启动警告"的触发条件：
// 只有回环监听才算"限速边界完整"，其余形态都要打印警告。
func TestIsLoopbackAddr(t *testing.T) {
	loopback := []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "127.0.0.1", "::1", "[::1]"}
	for _, addr := range loopback {
		if !isLoopbackAddr(addr) {
			t.Fatalf("%q 应被判定为回环监听", addr)
		}
	}

	// ":8080" 等于监听所有网卡 —— 这不是回环（必须警告）。
	notLoopback := []string{":8080", "0.0.0.0:8080", "192.168.1.10:8080", "example.com:8080", "[::]:8080"}
	for _, addr := range notLoopback {
		if isLoopbackAddr(addr) {
			t.Fatalf("%q 不应被判定为回环监听（会漏掉暴露警告）", addr)
		}
	}
}
