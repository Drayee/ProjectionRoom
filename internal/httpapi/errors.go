package httpapi

import (
	"errors"
	"net/http"

	"ProjectionRoom/internal/protocol"
	"ProjectionRoom/internal/room"
)

// roomErrorResponse 把 room 包的领域错误映射为 HTTP 状态码、协议错误码与用户可读消息。
// REST 与 WebSocket 两条入口共用它，保证同一错误在任何通道上的表述一致。
func roomErrorResponse(err error) (status int, code string, message string) {
	switch {
	case errors.Is(err, room.ErrNotFound):
		return http.StatusNotFound, protocol.CodeRoomNotFound, "房间不存在"
	case errors.Is(err, room.ErrBadPassword):
		return http.StatusForbidden, protocol.CodeBadPassword, "房间密码错误"
	case errors.Is(err, room.ErrFull):
		return http.StatusConflict, protocol.CodeRoomFull, "房间已满（受主播上行限制）"
	case errors.Is(err, room.ErrHostTaken):
		return http.StatusConflict, protocol.CodeHostTaken, "房间已有主播"
	case errors.Is(err, room.ErrNotReady):
		return http.StatusConflict, protocol.CodeRoomNotReady, "主播尚未进房"
	case errors.Is(err, room.ErrAlreadyJoined):
		return http.StatusConflict, protocol.CodeAlreadyJoin, "该连接已在房间中"
	case errors.Is(err, room.ErrNotJoined):
		return http.StatusBadRequest, protocol.CodeNotJoined, "尚未加入房间"
	case errors.Is(err, room.ErrNotHost):
		return http.StatusForbidden, protocol.CodeNotHost, "只有主播可以执行该操作"
	case errors.Is(err, room.ErrBadMediaIndex):
		return http.StatusBadRequest, protocol.CodeBadMediaIndex, "分片索引不合法"
	case errors.Is(err, room.ErrMediaLocked):
		return http.StatusConflict, protocol.CodeMediaLocked, "分片索引已锁定，换片需重开房间"
	case errors.Is(err, room.ErrBadName):
		return http.StatusBadRequest, protocol.CodeBadRequest, err.Error()
	case errors.Is(err, room.ErrBadInput):
		return http.StatusBadRequest, protocol.CodeBadRequest, "参数不合法"
	case errors.Is(err, room.ErrRoomExists):
		return http.StatusConflict, protocol.CodeBadRequest, "房间码已存在"
	default:
		return http.StatusInternalServerError, protocol.CodeInternalError, "服务端内部错误"
	}
}
