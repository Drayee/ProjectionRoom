package handler

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/usecase"
)

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
func wsHandler(cfg *config.Config, hub *service.Hub, rooms *usecase.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		roomID := strings.ToUpper(strings.TrimSpace(c.Query("roomId")))
		clientID := strings.TrimSpace(c.Query("clientId"))
		if roomID == "" || clientID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "roomId 与 clientId 为必填参数"})
			return
		}

		conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
			// Origin 校验：单端口部署后页面与 /ws 同源，所以把**请求自身的 Host** 也加进
			// 白名单 —— 隧道给的域名（http 或 https）零配置即可用，而跨站页面会被拒。
			// 本机开发时页面在 Vite 5173、/ws 在 Go 8080，属于跨源，由 AllowedOrigins 覆盖。
			// 注意：非浏览器客户端（curl/自动化）不发 Origin，coder/websocket 对这种情况放行。
			OriginPatterns:  append(append([]string{}, cfg.Signal.AllowedOrigins...), c.Request.Host),
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			log.Printf("ws: 升级失败 room=%s client=%s: %v", roomID, clientID, err)
			return
		}
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

			env, err := model.Unmarshal(data)
			if err != nil {
				_ = client.Send(model.ErrorEnvelope(model.CodeBadRequest, "报文不是合法的 protobuf 信封"))
				continue
			}
			handleMessage(hub, rooms, client, roomID, clientID, *env)
		}
	}
}

// writePreAuthError 在连接尚未注册进 Hub 时写一条错误信封。
//
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

func handleMessage(hub *service.Hub, rooms *usecase.Manager, client *service.Client, roomID, clientID string, env model.Envelope) {
	switch env.Type {
	case model.TypeJoin:
		if err := rooms.Join(roomID, clientID, env.DisplayName, env.Role, env.Password); err != nil {
			_, code, message := roomErrorResponse(err)
			_ = client.Send(model.ErrorEnvelope(code, message))
			log.Printf("ws: %s 加入 %s 被拒绝: %s", clientID, roomID, message)
		}

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
