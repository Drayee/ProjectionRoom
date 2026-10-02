// Package service 实现 WebSocket 信令层：连接注册、定向转发与房间广播。
// 它只转发元数据，不传输任何视频字节（SPEC §1.1 目标 7）。
package service

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/coder/websocket"

	"ProjectionRoom/internal/config"
)

var (
	// ErrClientOffline 表示目标客户端不在线。
	ErrClientOffline = errors.New("signal: 客户端不在线")
	// ErrDuplicateID 表示同一 clientId 已有活跃连接。
	ErrDuplicateID = errors.New("signal: clientId 已存在活跃连接")
	// ErrSendQueueFull 表示发送队列已满（慢客户端），消息被丢弃。
	ErrSendQueueFull = errors.New("signal: 发送队列已满，消息已丢弃")
)

// closeGrace 是房间关闭时留给 room-closed 报文投递的时间。
// 没有它，"先广播再断连"会让最后一条消息随机丢失。
const closeGrace = 200 * time.Millisecond

// Client 是一条已升级的 WebSocket 连接。
// 所有写入都经 send 队列与唯一的 writePump，避免并发写同一连接。
type Client struct {
	id     string
	roomID string
	conn   *websocket.Conn
	send   chan []byte
	cfg    config.SignalConfig

	closeOnce sync.Once
	closed    chan struct{}
}

func newClient(id, roomID string, conn *websocket.Conn, cfg config.SignalConfig) *Client {
	return &Client{
		id:     id,
		roomID: roomID,
		conn:   conn,
		send:   make(chan []byte, cfg.SendQueueSize),
		cfg:    cfg,
		closed: make(chan struct{}),
	}
}

// ID 返回连接标识。
func (c *Client) ID() string { return c.id }

// RoomID 返回连接声明的房间；成员资格以 room.Manager 为准。
func (c *Client) RoomID() string { return c.roomID }

// Send 把消息放入发送队列。
// 队列满时丢弃并返回错误：宁可丢一条信令，也不能让慢客户端阻塞整个房间。
func (c *Client) Send(msg []byte) error {
	select {
	case <-c.closed:
		return ErrClientOffline
	default:
	}

	select {
	case c.send <- msg:
		return nil
	default:
		log.Printf("signal: client %s 发送队列已满，丢弃一条信令", c.id)
		return ErrSendQueueFull
	}
}

// Close 关闭底层连接（幂等）。
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.conn.Close(websocket.StatusNormalClosure, "bye")
	})
}

// writePump 是这条连接唯一的写入者：排空 send 队列并周期性 ping 探活。
func (c *Client) writePump(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.Close()
			return
		case <-c.closed:
			return
		case msg := <-c.send:
			writeCtx, cancel := context.WithTimeout(ctx, c.cfg.WriteTimeout)
			// 信令报文是 protobuf：走二进制帧。
			err := c.conn.Write(writeCtx, websocket.MessageBinary, msg)
			cancel()
			if err != nil {
				c.Close()
				return
			}
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, c.cfg.WriteTimeout)
			err := c.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				c.Close()
				return
			}
		}
	}
}

// Hub 维护 clientId → Client 与 roomID → 连接集合两张索引。
// 它实现 room.Broadcaster（方法集由 wire 在编译期绑定检查）。
type Hub struct {
	cfg config.SignalConfig

	mu      sync.RWMutex
	clients map[string]*Client
	rooms   map[string]map[string]*Client

	ctx    context.Context
	cancel context.CancelFunc
}

// NewHub 构造 Hub，并返回清理函数供 wire 作为 cleanup 聚合。
func NewHub(cfg *config.Config) (*Hub, func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{
		cfg:     cfg.Signal,
		clients: make(map[string]*Client),
		rooms:   make(map[string]map[string]*Client),
		ctx:     ctx,
		cancel:  cancel,
	}
	return h, h.Close, nil
}

// Close 关闭所有写协程与连接。
func (h *Hub) Close() {
	h.cancel()

	h.mu.Lock()
	clients := make([]*Client, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.clients = make(map[string]*Client)
	h.rooms = make(map[string]map[string]*Client)
	h.mu.Unlock()

	for _, c := range clients {
		c.Close()
	}
}

// Register 注册一条新连接并启动其写协程。
func (h *Hub) Register(id, roomID string, conn *websocket.Conn) (*Client, error) {
	h.mu.Lock()
	if _, exists := h.clients[id]; exists {
		h.mu.Unlock()
		return nil, ErrDuplicateID
	}
	c := newClient(id, roomID, conn, h.cfg)
	h.clients[id] = c
	if h.rooms[roomID] == nil {
		h.rooms[roomID] = make(map[string]*Client)
	}
	h.rooms[roomID][id] = c
	h.mu.Unlock()

	go c.writePump(h.ctx)
	return c, nil
}

// Unregister 移除连接并关闭它。
func (h *Hub) Unregister(id string) {
	h.mu.Lock()
	c, ok := h.clients[id]
	if ok {
		delete(h.clients, id)
		if room := h.rooms[c.roomID]; room != nil {
			delete(room, id)
			if len(room) == 0 {
				delete(h.rooms, c.roomID)
			}
		}
	}
	h.mu.Unlock()

	if ok {
		c.Close()
	}
}

// SendTo 向指定连接投递消息。目标不在线或队列已满都返回错误。
func (h *Hub) SendTo(id string, msg []byte) error {
	h.mu.RLock()
	c, ok := h.clients[id]
	h.mu.RUnlock()
	if !ok {
		return ErrClientOffline
	}
	return c.Send(msg)
}

// BroadcastToRoom 向房间内所有连接广播；except 非空时跳过该连接。返回成功入队数量。
func (h *Hub) BroadcastToRoom(roomID string, msg []byte, except string) int {
	h.mu.RLock()
	targets := make([]*Client, 0, len(h.rooms[roomID]))
	for id, c := range h.rooms[roomID] {
		if id == except {
			continue
		}
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	sent := 0
	for _, c := range targets {
		if c.Send(msg) == nil {
			sent++
		}
	}
	return sent
}

// RoomSize 返回房间内当前连接数。
func (h *Hub) RoomSize(roomID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms[roomID])
}

// CloseRoom 关闭房间内所有连接。
// 先从索引移除（不再接收新消息），再留 closeGrace 让队列中的消息投递完成。
func (h *Hub) CloseRoom(roomID string) {
	h.mu.Lock()
	targets := make([]*Client, 0, len(h.rooms[roomID]))
	for id, c := range h.rooms[roomID] {
		targets = append(targets, c)
		delete(h.clients, id)
	}
	delete(h.rooms, roomID)
	h.mu.Unlock()

	for _, c := range targets {
		client := c
		time.AfterFunc(closeGrace, client.Close)
	}
}
