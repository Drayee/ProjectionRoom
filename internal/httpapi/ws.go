package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/protocol"
	"ProjectionRoom/internal/room"
	"ProjectionRoom/internal/signal"
)

// wsHandler 是唯一的 WebSocket 入口：/ws?roomId=..&clientId=..
//
// 连接建立时只把连接登记进 Hub（用于信令投递），
// 真正的"进入房间"由第一条 join 消息完成 —— 那时才校验密码与成员上限（SPEC §5.1）。
func wsHandler(cfg *config.Config, hub *signal.Hub, rooms *room.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		roomID := strings.ToUpper(strings.TrimSpace(c.Query("roomId")))
		clientID := strings.TrimSpace(c.Query("clientId"))
		if roomID == "" || clientID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "roomId 与 clientId 为必填参数"})
			return
		}

		conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
			// 本机开发：Vite dev server 与 Go 不同端口，放开来源校验（SPEC §1.3）。
			// 部署到公网时必须改为 OriginPatterns 白名单 + WSS。
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionDisabled,
		})
		if err != nil {
			log.Printf("ws: 升级失败 room=%s client=%s: %v", roomID, clientID, err)
			return
		}
		conn.SetReadLimit(cfg.Signal.MaxMessageBytes)

		client, err := hub.Register(clientID, roomID, conn)
		if err != nil {
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
				return
			}
			if typ != websocket.MessageText {
				// 二进制帧留给后续阶段的兜底转发；当前架构下服务端不传输视频字节。
				continue
			}

			var env protocol.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				_ = client.Send(protocol.ErrorEnvelope(protocol.CodeBadRequest, "报文不是合法 JSON"))
				continue
			}
			handleMessage(hub, rooms, client, roomID, clientID, env)
		}
	}
}

func handleMessage(hub *signal.Hub, rooms *room.Manager, client *signal.Client, roomID, clientID string, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeJoin:
		if err := rooms.Join(roomID, clientID, env.DisplayName, env.Role, env.Password); err != nil {
			_, code, message := roomErrorResponse(err)
			_ = client.Send(protocol.ErrorEnvelope(code, message))
			log.Printf("ws: %s 加入 %s 被拒绝: %s", clientID, roomID, message)
		}

	case protocol.TypeChat:
		if err := rooms.HandleChat(roomID, clientID, env.Text); err != nil {
			_, code, message := roomErrorResponse(err)
			_ = client.Send(protocol.ErrorEnvelope(code, message))
		}

	case protocol.TypeRoomControl:
		if err := rooms.HandleControl(roomID, clientID, env); err != nil {
			_, code, message := roomErrorResponse(err)
			_ = client.Send(protocol.ErrorEnvelope(code, message))
		}

	case protocol.TypeSignal:
		forwardSignal(hub, rooms, client, roomID, clientID, env)

	case protocol.TypeChunksReport, protocol.TypeMetrics, protocol.TypeTopologyRequest:
		// M2/M3 接入：分片拥有情况 bitset、实测度量、拓扑分配请求（SPEC §5.1、§6.3）。
		// M1 阶段静默忽略，避免把连接打挂。

	case protocol.TypeLeave:
		rooms.Leave(roomID, clientID)

	default:
		_ = client.Send(protocol.ErrorEnvelope(protocol.CodeBadRequest, "未知消息类型: "+env.Type))
	}
}

// forwardSignal 把 SDP/ICE 原样转发给同房间的目标连接。
// 两道校验：发送者必须是房间成员，目标也必须是同一房间成员 —— 防止跨房注入（SPEC §5.1）。
func forwardSignal(hub *signal.Hub, rooms *room.Manager, client *signal.Client, roomID, clientID string, env protocol.Envelope) {
	if env.To == "" || env.To == clientID {
		_ = client.Send(protocol.ErrorEnvelope(protocol.CodeBadRequest, "signal 需要合法的 to"))
		return
	}

	r, ok := rooms.Get(roomID)
	if !ok || !r.IsMember(clientID) {
		_ = client.Send(protocol.ErrorEnvelope(protocol.CodeNotJoined, "尚未加入房间"))
		return
	}
	if !r.IsMember(env.To) {
		_ = client.Send(protocol.ErrorEnvelope(protocol.CodeCrossRoom, "目标不在同一房间"))
		return
	}

	out := protocol.Envelope{
		Type:    protocol.TypeSignal,
		RoomID:  roomID,
		From:    clientID,
		To:      env.To,
		Payload: env.Payload,
	}
	if err := hub.SendTo(env.To, protocol.MustEnvelope(out)); err != nil {
		_ = client.Send(protocol.ErrorEnvelope(protocol.CodeInternalError, "目标已离线"))
	}
}
