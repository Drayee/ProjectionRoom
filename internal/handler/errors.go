package handler

import (
	"errors"
	"net/http"

	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/usecase"
)

// roomErrorResponse 把 room 包的领域错误映射为 HTTP 状态码、协议错误码与用户可读消息。
// REST 与 WebSocket 两条入口共用它，保证同一错误在任何通道上的表述一致。
func roomErrorResponse(err error) (status int, code string, message string) {
	switch {
	case errors.Is(err, usecase.ErrNotFound):
		return http.StatusNotFound, model.CodeRoomNotFound, "房间不存在"
	case errors.Is(err, usecase.ErrBadPassword):
		return http.StatusForbidden, model.CodeBadPassword, "房间密码错误"
	case errors.Is(err, usecase.ErrFull):
		return http.StatusConflict, model.CodeRoomFull, "房间已满（受主播上行限制）"
	case errors.Is(err, usecase.ErrHostTaken):
		return http.StatusConflict, model.CodeHostTaken, "房间已有主播"
	case errors.Is(err, usecase.ErrNotReady):
		return http.StatusConflict, model.CodeRoomNotReady, "主播尚未进房"
	case errors.Is(err, usecase.ErrAlreadyJoined):
		return http.StatusConflict, model.CodeAlreadyJoin, "该连接已在房间中"
	case errors.Is(err, usecase.ErrNotJoined):
		return http.StatusBadRequest, model.CodeNotJoined, "尚未加入房间"
	case errors.Is(err, usecase.ErrNotHost):
		return http.StatusForbidden, model.CodeNotHost, "只有主播可以执行该操作"
	case errors.Is(err, usecase.ErrBadMediaIndex):
		return http.StatusBadRequest, model.CodeBadMediaIndex, "分片索引不合法"
	case errors.Is(err, usecase.ErrMediaLocked):
		return http.StatusConflict, model.CodeMediaLocked, "分片索引已锁定，换片需重开房间"
	case errors.Is(err, usecase.ErrBadName):
		return http.StatusBadRequest, model.CodeBadRequest, err.Error()
	case errors.Is(err, usecase.ErrBadInput):
		return http.StatusBadRequest, model.CodeBadRequest, "参数不合法"
	case errors.Is(err, usecase.ErrRoomExists):
		return http.StatusConflict, model.CodeBadRequest, "房间码已存在"
	// —— 以下三条是 S-3 / S-7 / S-11 新增闸门的对外表述。
	// 状态码选择：400（参数不合白名单）/ 503（服务端在册房间到顶，不是客户端的错）/ 403（令牌缺失或错误）。
	case errors.Is(err, usecase.ErrBadRoomCode):
		return http.StatusBadRequest, model.CodeBadRequest, err.Error()
	case errors.Is(err, usecase.ErrBadPasswordPolicy):
		return http.StatusBadRequest, model.CodeBadRequest, err.Error()
	case errors.Is(err, usecase.ErrTooManyRooms):
		return http.StatusServiceUnavailable, model.CodeTooManyRooms, err.Error()
	case errors.Is(err, usecase.ErrHostTokenRequired):
		return http.StatusForbidden, model.CodeHostTokenRequired, err.Error()
	case errors.Is(err, usecase.ErrJoinRateLimited):
		return http.StatusTooManyRequests, model.CodeRateLimited, err.Error()
	default:
		return http.StatusInternalServerError, model.CodeInternalError, "服务端内部错误"
	}
}
