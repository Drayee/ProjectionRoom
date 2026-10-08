package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
)

// shutdownTimeout 是收到终止信号后留给在途请求的关闭时间。
const shutdownTimeout = 5 * time.Second

// HTTP 服务器的超时参数（S-15）。
//
// 取舍逐条说明 —— 这些值都在"挡住慢连接攻击"与"不打断长传输"之间取平衡：
//
//	ReadHeaderTimeout = 10s
//	  已有值。只覆盖请求头，最慢的攻击形态（慢速发头）被它挡住。
//
//	IdleTimeout = 120s
//	  Keep-Alive 空闲连接上限。没有它时，一个连接只要偶尔发一个字节就能常驻，
//	  而 Go 默认（0）表示"不限制"。120s 的依据：页面是"打开就一直开着"，
//	  但浏览器在空闲时会主动复用/关闭；两分钟足够覆盖"看视频时页面不请求"的空档，
//	  又不会让闲置连接长期占住 fd。
//
//	MaxHeaderBytes = 16 KiB
//	  默认 1 MiB 是"给巨型 Cookie/令牌"留的余量，而本服务根本不用 Cookie。
//	  16 KiB 覆盖正常请求头（含 ICE 的长 UA、Referer）一个数量级。
//	  注意：gin 的引擎自己不会用到这个值，它由 http.Server 在读头时执行。
//
//	WriteTimeout = 0（关闭），理由：
//	  这条服务上有**两类**响应，它们的合理时长差三个数量级：
//	    1. 信令 / REST / 静态资源：毫秒级；
//	    2. 流式产物下载（GET /api/v1/segment/jobs/:id/artifacts/...）与
//	       切片作业的打包下载：单个产物上限 1 GiB（Segment.SingleResponseMaxBytes），
//	       在慢链路上**几分钟**都是正常的。
//	  给一个统一的 WriteTimeout 只有两种结果：要么大到对第 1 类毫无保护
//	  （例如 10 分钟），要么小到会切断第 2 类（例如 30s，用户在弱网下载几百 MB 时
//	  必然断流 —— 那是比"缺一个超时"更严重的可用性问题）。
//	  所以这里的取舍是：**不设全局写超时，改为逐路由收紧** ——
//	  短响应的保护由 gin 侧自然的写失败与 IdleTimeout 承担，
//	  长响应由它自己的读取/写出超时与请求上下文（客户端断开即取消）承担。
//	  比"设一个够大的全局值"更精确的地方在于：它不会误伤长下载。
//	  如果将来要为短响应补一层硬超时，正确做法是给 /api 与 /ws 各挂一个
//	  http.ResponseController（见 web/server.go 的写法），而不是把全局值调小。
const (
	httpIdleTimeout    = 120 * time.Second
	httpMaxHeaderBytes = 16 << 10
)

// Server 是 HTTP 服务的生命周期封装。
// 它把"起服务 / 收信号 / 优雅关闭"从 main 里搬出来，
// main 因此只剩"加载配置 → 组装 → 运行"三行。
type Server struct {
	cfg  *config.Config
	http *http.Server
}

// NewServer 是 wire 的提供者：吃配置与路由，吐出可运行的服务。
func NewServer(cfg *config.Config, engine *gin.Engine) (*Server, error) {
	if cfg == nil || engine == nil {
		return nil, errors.New("service: NewServer 需要非空的配置与路由引擎")
	}

	// S-1 的兜底闸：进程软内存上限。
	//
	// 这不是替代"解码前的帧预算"，而是它漏掉的那些路径的最后一道防线：
	// 帧预算知道"这一帧会展开成什么"，SetMemoryLimit 只知道"总量到顶了"。
	// Go 的语义是"接近上限时提高 GC 频率（可超过上限）"，所以它的表现是变慢，
	// 而不是被 OOM killer 杀掉 —— 对一个信令服务来说，变慢远好于被打死。
	applyMemoryLimit(cfg)

	return &Server{
		cfg: cfg,
		http: &http.Server{
			Addr:              cfg.Addr,
			Handler:           engine,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       httpIdleTimeout,
			MaxHeaderBytes:    httpMaxHeaderBytes,
			// WriteTimeout 有意留 0（关闭）：见上面常量块的取舍说明。
			WriteTimeout: 0,
		},
	}, nil
}

// applyMemoryLimit 设置 debug.SetMemoryLimit。
//
// 单独一个函数只为一件事：让"配置为 0 表示不限制"这条语义集中在一处，
// 并且能在单测里直接调用（不必起 HTTP 服务）。
func applyMemoryLimit(cfg *config.Config) {
	if cfg.MemoryLimitBytes <= 0 {
		return
	}
	debug.SetMemoryLimit(cfg.MemoryLimitBytes)
	log.Printf("已设置进程软内存上限 GOMEMLIMIT=%s（兜底闸：帧预算之外的路径靠它兜住）",
		humanBytes(cfg.MemoryLimitBytes))
}

// humanBytes 把字节数写成 MiB/GiB，仅用于日志。
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/float64(1<<20))
	default:
		return fmt.Sprintf("%d 字节", n)
	}
}

// Addr 返回监听地址（日志用）。
func (s *Server) Addr() string { return s.cfg.Addr }

// Start 在后台启动监听。
// 启动期错误没有恢复手段，直接记日志并退出进程 —— 掩盖它只会让问题更难查。
func (s *Server) Start() {
	go func() {
		log.Printf("ProjectionRoom 已启动: http://%s（健康检查 /healthz，信令 /ws）", s.cfg.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// Run 启动服务并阻塞到收到中断信号，随后优雅关闭。
//
// 这是 cmd 唯一需要调用的启动入口：收信号、超时关闭属于进程生命周期而非业务，
// 所以留在 service 层，让 main 保持"加载配置 → 组装 → 运行"三行。
func (s *Server) Run() error {
	s.Start()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := s.Shutdown(ctx); err != nil {
		return fmt.Errorf("service: 优雅关闭超时: %w", err)
	}
	log.Println("ProjectionRoom 已停止")

	return nil
}
