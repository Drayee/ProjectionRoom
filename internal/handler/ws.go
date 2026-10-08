package handler

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/limiters"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/usecase"
)

// joinedOriginTodo 记录一条部署待办（S-5 的收尾项，docs/SPEC.md 同步）。
//
// 非浏览器客户端（curl/自动化脚本）不发 Origin，coder/websocket 对这种情况一律放行 ——
// 这是我们**有意保留**的现状（本地工具链、verify-*.mjs 都依赖它）。
// 但它意味着"任何能连到本端口的人都能发 join 指令"，
// 因此账号层上线后这条放行必须改成"要求令牌"，否则信令端点是不设防的。
const joinedOriginTodo = "TODO(账号层)：非浏览器客户端（无 Origin）目前按设计放行，" +
	"账号层上线后必须改为要求令牌（见 docs/SPEC.md 的安全待办）。"

// preAuthWriteTimeout 是"连接尚未注册进 Hub"时写单条帧的超时上限。
//
// 为什么单独设上限而不是直接用 cfg.Signal.WriteTimeout（默认 10s）：
// 此时还没有写协程/发送队列，写操作就压在这次握手请求上；
// 客户端若已跑掉，10s 会拖住这个请求协程。2s 足够覆盖一次本地/局域网投递。
const preAuthWriteTimeout = 2 * time.Second

// wsHandler 是唯一的 WebSocket 入口：/ws?roomId=..&clientId=..
//
// 连接建立时只把连接登记进 Hub（用于信令投递），
// 真正的"进入房间"由第一条 join 消息完成 —— 那时才校验密码与成员上限（SPEC §5.1）。
//
// joinLimiter 是 S-11 的第二半：join **失败**按 IP+房间码限速（成功不消耗令牌）。
// 它放在 handler 层而不是 usecase：usecase 不认识 HTTP 语义（IP、状态码），
// 而这条限速是"防在线猜房间密码"的外围闸门。
func wsHandler(cfg *config.Config, hub *service.Hub, rooms *usecase.Manager, joinLimiter *limiters.Keyed) gin.HandlerFunc {
	return func(c *gin.Context) {
		roomID := strings.ToUpper(strings.TrimSpace(c.Query("roomId")))
		clientID := strings.TrimSpace(c.Query("clientId"))
		// clientIP 在升级之前取出：限速键要在整条连接生命周期里保持稳定。
		clientIP := c.ClientIP()
		if roomID == "" || clientID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "roomId 与 clientId 为必填参数"})
			return
		}
		// 房间码白名单（S-3）：/ws 是另一条进入房间的入口，必须与 REST 用同一把尺子，
		// 否则"REST 校验、WS 不校验"就等于校验白做了。
		if !usecase.ValidRoomCode(roomID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": createRoomCodeHint})
			return
		}

		// Origin 校验：**自己做，只认显式白名单**。
		//
		// S-5 修复记录（两道都要）：库自带的 authenticateOrigin 会先做
		// "strings.EqualFold(r.Host, originHost) 则放行" —— 也就是**等于允许
		// 任何 Origin 都带一个与它一致的 Host**。实测 `Origin: http://evil.com` +
		// `Host: evil.com` → 101：跨站页面（DNS rebinding 的经典形态）能连上 /ws。
		// 因此这里关掉库的校验（InsecureSkipVerify），改用自己的白名单，
		// 判据只有一条：Origin 为空（非浏览器客户端，见 joinedOriginTodo）
		// 或 Origin 的 host 命中显式配置。绝不把请求自身的 Host 当来源。
		if err := checkOrigin(c.Request, cfg.Signal.AllowedOrigins); err != nil {
			log.Printf("ws: 来源被拒 room=%s client=%s origin=%q host=%q: %v",
				roomID, clientID, c.GetHeader("Origin"), c.Request.Host, err)
			c.JSON(http.StatusForbidden, gin.H{
				"error": "来源不被允许（请把页面所在域名加入 PR_ALLOWED_ORIGINS）",
				"code":  model.CodeBadRequest,
			})
			return
		}

		conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
			// 库的 Origin 校验已在上面被取代（它的同源判据可以被伪造的 Host 满足）。
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionDisabled,
		})
		if err != nil {
			log.Printf("ws: 升级失败 room=%s client=%s: %v", roomID, clientID, err)
			return
		}
		// 第一道闸：单条消息的字节长度上限。
		conn.SetReadLimit(cfg.Signal.MaxMessageBytes)

		client, err := hub.Register(clientID, roomID, conn)
		if err != nil {
			// 在拒绝之前先把原因告诉客户端：否则它只会看到一次策略违规关闭，
			// 除了拿着同一个 clientId 空转重试别无选择（旧连接可能是半开连接，
			// 最多约 30s 才被 ping/写超时收尸）。客户端收到 CLIENT_ID_TAKEN 后应换一个 clientId 重连。
			// 注意：此刻连接还没注册进 Hub，也就不是 *service.Client，只能自己写这一帧。
			writePreAuthError(cfg, conn, model.ErrorEnvelope(model.CodeClientIDTaken, "该 clientId 已有活跃连接，请换一个 clientId 重试"))
			log.Printf("ws: %s 被拒绝（room=%s）: %v，已下发 %s 并关闭连接",
				clientID, roomID, err, model.CodeClientIDTaken)
			_ = conn.Close(websocket.StatusPolicyViolation, "clientId 已存在活跃连接")
			return
		}

		// 第二道闸（S-1）：**每连接的字节速率配额**。
		//
		// 它与后面的"帧预算"分工不同：帧预算卡"一帧会展开成什么"，
		// 速率配额卡"总体上你发得太多了"。两者都不能少 ——
		// 一个每秒发 1000 条合规小帧的连接，任何单帧检查都拦不住。
		quota := limiters.NewBucket(cfg.Signal.MaxRateBytesPerSec, cfg.Signal.RateBucketBytes)

		// 帧预算（配置 → 扫描器）：单帧内 repeated 元素与单字段字节的硬上限。
		budget := model.WireBudget{
			MaxRepeatedElements:  cfg.Signal.MaxRepeatedElements,
			MaxMembersPerMessage: cfg.Signal.MaxMembersPerMessage,
			MaxSegmentsPerIndex:  cfg.Signal.MaxSegmentsPerIndex,
			MaxFieldBytes:        cfg.Signal.MaxFieldBytes,
		}

		ctx := c.Request.Context()
		log.Printf("ws: %s 已连接（room=%s）", clientID, roomID)

		defer func() {
			hub.Unregister(clientID)
			rooms.Leave(roomID, clientID)
			log.Printf("ws: %s 已断开（room=%s）", clientID, roomID)
		}()

		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				// 断线原因必须记下来，否则无法分辨"隧道掐连接 / 浏览器关连接 /
				// 服务端重启 / 半开连接被超时收尸"——而这决定了要不要保留宽限期。
				log.Printf("ws: %s 读循环结束（room=%s）: %v（关闭码=%v）",
					clientID, roomID, err, websocket.CloseStatus(err))
				return
			}
			if typ != websocket.MessageBinary {
				// 信令已经是 protobuf 二进制帧；文本帧不再处理。
				// 注意：这里处理的是"控制报文"，视频分片走 WebRTC DataChannel，不经过服务器。
				continue
			}

			// 速率配额：超限直接以 1008（策略违规）关闭。
			// 为什么不是"丢掉这一帧继续"：超限意味着这条连接不是在正常通信，
			// 继续服务它只是把资源浪费在它身上；而正常客户端永远碰不到这个上限。
			if !quota.Allow(len(data)) {
				log.Printf("ws: %s（room=%s）超出信令速率配额（%d 字节/秒，本帧 %d 字节），以 1008 关闭",
					clientID, roomID, cfg.Signal.MaxRateBytesPerSec, len(data))
				_ = conn.Close(websocket.StatusPolicyViolation, "信令速率超限")
				return
			}

			// 帧预算：**在 Unmarshal 之前**拒绝放大帧（S-1）。
			//
			// 为什么必须先于解码：审计实测一帧 4,194,300 字节（members 重复 1,398,100 次）
			// 让 RSS 从 22 MB 涨到 3,947 MB，而 SetReadLimit 完全拦不住它（4 MiB 之内）。
			// 扫描器在读到第 MaxMembersPerMessage+1 个元素时就返回，因此拒绝时
			// 唯一的内存成本是这一帧自己的读缓冲。
			state, scanErr := model.ScanWireBudgetWithState(data, budget)
			if scanErr != nil {
				log.Printf("ws: %s（room=%s）的帧被预算拒绝（%d 字节 → %d 个元素）：%v",
					clientID, roomID, len(data), state.Elements, scanErr)
				_ = conn.Close(websocket.StatusMessageTooBig, "报文超出服务端预算")
				return
			}

			env, err := model.Unmarshal(data)
			if err != nil {
				_ = client.Send(model.ErrorEnvelope(model.CodeBadRequest, "报文不是合法的 protobuf 信封"))
				continue
			}
			handleMessage(hub, rooms, client, roomID, clientID, clientIP, *env, joinLimiter)
		}
	}
}

// checkOrigin 是 /ws 的来源白名单校验（S-5）。
//
// 判据只有两条：
//
//  1. 没有 Origin 头 → 放行。这是**有意保留**的现状：非浏览器客户端
//     （curl、verify-*.mjs）不发 Origin，本地工具链依赖它。
//     代价与收尾计划见 joinedOriginTodo / docs/SPEC.md 的安全待办。
//  2. 有 Origin → 它的 host[:port] 必须命中显式配置的某个模式
//     （支持 *.example.com 通配，形如 path.Match）。
//
// **绝不**接受"Origin 的 host 等于请求自身的 Host"这种判据：
// 那是库的默认行为，而 Host 与 Origin 都是请求方可控的，
// 于是任何域名都能自证同源（DNS rebinding）。
func checkOrigin(r *http.Request, allowed []string) error {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return nil
	}

	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("Origin 头无法解析: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("Origin 头里没有 host")
	}

	target := u.Host
	// 配置项可以写成带 scheme 的形式（http://example.com:8080），
	// 那就按"scheme://host"整体匹配，避免 http 与 https 互相通过。
	schemeTarget := u.Scheme + "://" + u.Host

	for _, pattern := range allowed {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		candidate := target
		if strings.Contains(pattern, "://") {
			candidate = schemeTarget
		}
		matched, err := path.Match(strings.ToLower(pattern), strings.ToLower(candidate))
		if err != nil {
			return fmt.Errorf("来源模式 %q 非法: %w", pattern, err)
		}
		if matched {
			return nil
		}
	}

	return fmt.Errorf("Origin %q 不在 PR_ALLOWED_ORIGINS 白名单里", origin)
}

// writePreAuthError 在连接尚未注册进 Hub 时写一条错误信封。
// 这条路径上没有 *service.Client（没有发送队列与写协程），所以直接写裸连接：
// 超时取 min(cfg.Signal.WriteTimeout, preAuthWriteTimeout)；写失败不 panic、也不重试 ——
// 客户端可能早已离开，为一条"解释"阻塞或崩溃都不值得。
func writePreAuthError(cfg *config.Config, conn *websocket.Conn, msg []byte) {
	timeout := preAuthWriteTimeout
	if cfg != nil && cfg.Signal.WriteTimeout > 0 && cfg.Signal.WriteTimeout < timeout {
		timeout = cfg.Signal.WriteTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 信令是 protobuf：与正常链路一致走二进制帧。
	_ = conn.Write(ctx, websocket.MessageBinary, msg)
}

func handleMessage(hub *service.Hub, rooms *usecase.Manager, client *service.Client, roomID, clientID, clientIP string, env model.Envelope, joinLimiter *limiters.Keyed) {
	switch env.Type {
	case model.TypeJoin:
		// S-11：join 失败按 IP+房间码限速。
		//
		// 为什么只对失败计数：正常重连/刷新应当永远畅通（它们会成功，不消耗令牌），
		// 而在线猜房间密码全都是失败 —— 30 次/分钟的失败上限把 6 位密码的暴力猜测
		// 压到需要几天时间，同时留给正常人"打错三次"的余量。
		//
		// 键用 ClientIP 而不是 c.Request.RemoteAddr：后者带上随机端口，
		// 会让每次重连都变成"新键"，限速形同虚设。
		joinKey := clientIP + "|" + roomID
		err := rooms.Join(usecase.JoinRequest{
			RoomID:      roomID,
			ClientID:    clientID,
			DisplayName: env.DisplayName,
			Role:        env.Role,
			Password:    env.Password,
			HostToken:   env.HostToken,
		})
		if err == nil {
			return
		}
		if !joinLimiter.Allow(joinKey) {
			_ = client.Send(model.ErrorEnvelope(model.CodeRateLimited, usecase.ErrJoinRateLimited.Error()))
			log.Printf("ws: %s 的 join 失败次数超限（room=%s），已限速", clientID, roomID)
			return
		}
		_, code, message := roomErrorResponse(err)
		_ = client.Send(model.ErrorEnvelope(code, message))
		log.Printf("ws: %s 加入 %s 被拒绝: %s", clientID, roomID, message)

	case model.TypeChat:
		if err := rooms.HandleChat(roomID, clientID, env.Text); err != nil {
			_, code, message := roomErrorResponse(err)
			_ = client.Send(model.ErrorEnvelope(code, message))
		}

	case model.TypeRoomControl:
		if err := rooms.HandleControl(roomID, clientID, env); err != nil {
			_, code, message := roomErrorResponse(err)
			_ = client.Send(model.ErrorEnvelope(code, message))
		}

	case model.TypeSignal:
		forwardSignal(hub, rooms, client, roomID, clientID, env)

	case model.TypeMediaIndex:
		if err := rooms.SetMediaIndex(roomID, clientID, env.MediaIndex); err != nil {
			_, code, message := roomErrorResponse(err)
			_ = client.Send(model.ErrorEnvelope(code, message))
		}

	case model.TypeMetrics:
		if env.Metrics == nil {
			_ = client.Send(model.ErrorEnvelope(model.CodeBadRequest, "metrics 缺少 metrics 字段"))
			break
		}
		if err := rooms.UpdateMetrics(roomID, clientID, *env.Metrics); err != nil {
			_, code, message := roomErrorResponse(err)
			_ = client.Send(model.ErrorEnvelope(code, message))
		}

	case model.TypeChunksReport:
		if err := rooms.SetChunkReport(roomID, clientID, env.Have, env.Complete); err != nil {
			_, code, message := roomErrorResponse(err)
			_ = client.Send(model.ErrorEnvelope(code, message))
		}

	case model.TypeTopologyRequest:
		if err := rooms.SendTopology(roomID, clientID); err != nil {
			_, code, message := roomErrorResponse(err)
			_ = client.Send(model.ErrorEnvelope(code, message))
		}

	case model.TypeLeave:
		rooms.Leave(roomID, clientID)

	default:
		_ = client.Send(model.ErrorEnvelope(model.CodeBadRequest, "未知消息类型: "+env.Type))
	}
}

// forwardSignal 把 SDP/ICE 原样转发给同房间的目标连接。
// 两道校验：发送者必须是房间成员，目标也必须是同一房间成员 —— 防止跨房注入（SPEC §5.1）。
func forwardSignal(hub *service.Hub, rooms *usecase.Manager, client *service.Client, roomID, clientID string, env model.Envelope) {
	if env.To == "" || env.To == clientID {
		_ = client.Send(model.ErrorEnvelope(model.CodeBadRequest, "signal 需要合法的 to"))
		return
	}

	r, ok := rooms.Get(roomID)
	if !ok || !r.IsMember(clientID) {
		_ = client.Send(model.ErrorEnvelope(model.CodeNotJoined, "尚未加入房间"))
		return
	}
	if !r.IsMember(env.To) {
		_ = client.Send(model.ErrorEnvelope(model.CodeCrossRoom, "目标不在同一房间"))
		return
	}

	out := model.Envelope{
		Type:    model.TypeSignal,
		RoomID:  roomID,
		From:    clientID,
		To:      env.To,
		Payload: env.Payload,
	}
	if err := hub.SendTo(env.To, model.MustEnvelope(out)); err != nil {
		_ = client.Send(model.ErrorEnvelope(model.CodeInternalError, "目标已离线"))
	}
}
