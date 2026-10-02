package protocol

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	"ProjectionRoom/internal/media"
	"ProjectionRoom/internal/pb"
)

// 本文件是"线上 protobuf"与"进程内可读结构"之间唯一的转换点。
//
// 为什么不直接用生成的结构体：pb 的 int64 在 Go 里是 int64、在 TS 里是 bigint，
// 而领域代码（同步环、聊天时间戳、房间状态）习惯用普通数值；
// 把差异收在转换层，两边业务代码就不用为大整数类型改名改语义。
// 任何新增字段都必须同时改这里与 proto/projection_room.proto。

// Marshal 把领域信封编码成线上字节（protobuf）。
func Marshal(env *Envelope) ([]byte, error) {
	return proto.Marshal(ToPB(env))
}

// Unmarshal 把线上字节解码成领域信封。
func Unmarshal(data []byte) (*Envelope, error) {
	msg := &pb.Envelope{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("protocol: 解析 protobuf 信封失败: %w", err)
	}
	return FromPB(msg), nil
}

// MustMarshal 编码固定结构；失败属于编码错误，直接 panic 暴露。
func MustMarshal(env Envelope) []byte {
	b, err := Marshal(&env)
	if err != nil {
		panic("protocol: 编码信封失败: " + err.Error())
	}
	return b
}

// MustEnvelope 保留旧名字，等价于 MustMarshal（大量调用点沿用）。
func MustEnvelope(env Envelope) []byte { return MustMarshal(env) }

// ToPB 领域 → protobuf。
func ToPB(env *Envelope) *pb.Envelope {
	if env == nil {
		return &pb.Envelope{}
	}

	out := &pb.Envelope{
		Type:        env.Type,
		RoomId:      env.RoomID,
		ClientId:    env.ClientID,
		To:          env.To,
		From:        env.From,
		DisplayName: env.DisplayName,
		Role:        env.Role,
		Password:    env.Password,
		Payload:     env.Payload,
		Text:        env.Text,
		Ts:          env.TS,
		Action:      env.Action,
		CurrentTime: env.CurrentTime,
		HostClockMs: env.HostClockMs,
		ClockEpoch:  env.ClockEpoch,
		Paused:      env.Paused,
		Rate:        env.Rate,
		Seq:         env.Seq,
		SelfId:      env.SelfID,
		HostId:      env.HostID,
		Have:        env.Have,
		Complete:    env.Complete,
		Code:        env.Code,
		Message:     env.Message,
		Reason:      env.Reason,
	}

	if env.Playback != nil {
		out.Playback = &pb.PlaybackState{
			Paused:      env.Playback.Paused,
			CurrentTime: env.Playback.CurrentTime,
			HostClockMs: env.Playback.HostClockMs,
			Rate:        env.Playback.Rate,
			Seq:         env.Playback.Seq,
			ClockEpoch:  env.Playback.ClockEpoch,
		}
	}
	if env.Metrics != nil {
		out.Metrics = &pb.Metrics{
			RttMs:             env.Metrics.RTTMs,
			ThroughputBps:     env.Metrics.ThroughputBps,
			UploadCapacityBps: env.Metrics.UploadCapacityBps,
			Depth:             int32(env.Metrics.Depth),
			BufferHealth:      env.Metrics.BufferHealth,
			P95DeliveryMs:     env.Metrics.P95DeliveryMs,
			StallCount:        int32(env.Metrics.StallCount),
			Degraded:          env.Metrics.Degraded,
			PrimaryId:         env.Metrics.PrimaryID,
		}
	}
	if env.Member != nil {
		out.Member = memberToPB(env.Member)
	}
	for i := range env.Members {
		out.Members = append(out.Members, memberToPB(&env.Members[i]))
	}
	if env.MediaIndex != nil {
		out.MediaIndex = IndexToPB(env.MediaIndex)
	}
	if env.Capacity != nil {
		out.Capacity = &pb.Capacity{
			Mode:           env.Capacity.Mode,
			MaxMembers:     int32(env.Capacity.MaxMembers),
			StreamBps:      env.Capacity.StreamBps,
			HostChildSlots: int32(env.Capacity.HostChildSlots),
		}
	}
	if env.Topology != nil {
		out.Topology = &pb.TopologyAssignment{
			PeerId:        env.Topology.PeerID,
			PrimaryId:     env.Topology.PrimaryID,
			BackupIds:     env.Topology.BackupIDs,
			Children:      env.Topology.Children,
			Depth:         int32(env.Topology.Depth),
			Mode:          env.Topology.Mode,
			DistributorId: env.Topology.DistributorID,
			Reason:        env.Topology.Reason,
			MaxDepth:      int32(env.Topology.MaxDepth),
		}
	}
	if env.Distributor != nil {
		out.Distributor = &pb.DistributorChange{
			FromId: env.Distributor.FromID,
			ToId:   env.Distributor.ToID,
			Reason: env.Distributor.Reason,
		}
	}

	return out
}

// FromPB protobuf → 领域。
func FromPB(msg *pb.Envelope) *Envelope {
	if msg == nil {
		return &Envelope{}
	}

	out := &Envelope{
		Type:        msg.GetType(),
		RoomID:      msg.GetRoomId(),
		ClientID:    msg.GetClientId(),
		To:          msg.GetTo(),
		From:        msg.GetFrom(),
		DisplayName: msg.GetDisplayName(),
		Role:        msg.GetRole(),
		Password:    msg.GetPassword(),
		Text:        msg.GetText(),
		TS:          msg.GetTs(),
		Action:      msg.GetAction(),
		CurrentTime: msg.GetCurrentTime(),
		HostClockMs: msg.GetHostClockMs(),
		ClockEpoch:  msg.GetClockEpoch(),
		Paused:      msg.GetPaused(),
		Rate:        msg.GetRate(),
		Seq:         msg.GetSeq(),
		SelfID:      msg.GetSelfId(),
		HostID:      msg.GetHostId(),
		Have:        msg.GetHave(),
		Complete:    msg.GetComplete(),
		Code:        msg.GetCode(),
		Message:     msg.GetMessage(),
		Reason:      msg.GetReason(),
	}
	if payload := msg.GetPayload(); len(payload) > 0 {
		out.Payload = json.RawMessage(payload)
	}

	if p := msg.GetPlayback(); p != nil {
		out.Playback = &PlaybackState{
			Paused:      p.GetPaused(),
			CurrentTime: p.GetCurrentTime(),
			HostClockMs: p.GetHostClockMs(),
			Rate:        p.GetRate(),
			Seq:         p.GetSeq(),
			ClockEpoch:  p.GetClockEpoch(),
		}
	}
	if m := msg.GetMetrics(); m != nil {
		out.Metrics = &Metrics{
			RTTMs:             m.GetRttMs(),
			ThroughputBps:     m.GetThroughputBps(),
			UploadCapacityBps: m.GetUploadCapacityBps(),
			Depth:             int(m.GetDepth()),
			BufferHealth:      m.GetBufferHealth(),
			P95DeliveryMs:     m.GetP95DeliveryMs(),
			StallCount:        int(m.GetStallCount()),
			Degraded:          m.GetDegraded(),
			PrimaryID:         m.GetPrimaryId(),
		}
	}
	if m := msg.GetMember(); m != nil {
		member := memberFromPB(m)
		out.Member = &member
	}
	for _, m := range msg.GetMembers() {
		out.Members = append(out.Members, memberFromPB(m))
	}
	if idx := msg.GetMediaIndex(); idx != nil {
		out.MediaIndex = IndexFromPB(idx)
	}
	if c := msg.GetCapacity(); c != nil {
		out.Capacity = &Capacity{
			Mode:           c.GetMode(),
			MaxMembers:     int(c.GetMaxMembers()),
			StreamBps:      c.GetStreamBps(),
			HostChildSlots: int(c.GetHostChildSlots()),
		}
	}
	if t := msg.GetTopology(); t != nil {
		out.Topology = &TopologyAssignment{
			PeerID:        t.GetPeerId(),
			PrimaryID:     t.GetPrimaryId(),
			BackupIDs:     t.GetBackupIds(),
			Children:      t.GetChildren(),
			Depth:         int(t.GetDepth()),
			Mode:          t.GetMode(),
			DistributorID: t.GetDistributorId(),
			Reason:        t.GetReason(),
			MaxDepth:      int(t.GetMaxDepth()),
		}
	}
	if d := msg.GetDistributor(); d != nil {
		out.Distributor = &DistributorChange{
			FromID: d.GetFromId(),
			ToID:   d.GetToId(),
			Reason: d.GetReason(),
		}
	}

	return out
}

func memberToPB(m *MemberInfo) *pb.MemberInfo {
	return &pb.MemberInfo{
		Id:          m.ID,
		DisplayName: m.DisplayName,
		Role:        m.Role,
		Depth:       int32(m.Depth),
		PrimaryId:   m.PrimaryID,
		BackupIds:   m.BackupIDs,
		JoinedAt:    m.JoinedAt,
	}
}

func memberFromPB(m *pb.MemberInfo) MemberInfo {
	return MemberInfo{
		ID:          m.GetId(),
		DisplayName: m.GetDisplayName(),
		Role:        m.GetRole(),
		Depth:       int(m.GetDepth()),
		PrimaryID:   m.GetPrimaryId(),
		BackupIDs:   m.GetBackupIds(),
		JoinedAt:    m.GetJoinedAt(),
	}
}

// IndexToPB 把分片索引转成 protobuf 表示。
func IndexToPB(index *media.Index) *pb.MediaIndex {
	if index == nil {
		return nil
	}

	out := &pb.MediaIndex{
		Version:       int32(index.Version),
		InitFile:      index.InitFile,
		MimeType:      index.MimeType,
		TotalDuration: index.TotalDuration,
		SegmentSec:    index.SegmentSec,
		BitrateBps:    index.BitrateBps,
		TotalBytes:    index.TotalBytes,
	}
	for _, seg := range index.Segments {
		out.Segments = append(out.Segments, &pb.MediaSegment{
			Index:    int32(seg.Index),
			File:     seg.File,
			Offset:   seg.Offset,
			Size:     seg.Size,
			Duration: seg.Duration,
			StartPts: seg.StartPTS,
			Keyframe: seg.Keyframe,
			Sha256:   seg.SHA256,
		})
	}
	return out
}

// IndexFromPB 还原分片索引。
func IndexFromPB(msg *pb.MediaIndex) *media.Index {
	if msg == nil {
		return nil
	}

	out := &media.Index{
		Version:       int(msg.GetVersion()),
		InitFile:      msg.GetInitFile(),
		MimeType:      msg.GetMimeType(),
		TotalDuration: msg.GetTotalDuration(),
		SegmentSec:    msg.GetSegmentSec(),
		BitrateBps:    msg.GetBitrateBps(),
		TotalBytes:    msg.GetTotalBytes(),
	}
	for _, seg := range msg.GetSegments() {
		out.Segments = append(out.Segments, media.Segment{
			Index:    int(seg.GetIndex()),
			File:     seg.GetFile(),
			Offset:   seg.GetOffset(),
			Size:     seg.GetSize(),
			Duration: seg.GetDuration(),
			StartPTS: seg.GetStartPts(),
			Keyframe: seg.GetKeyframe(),
			SHA256:   seg.GetSha256(),
		})
	}
	return out
}

// SameIndex 判断两份分片索引是否等价（用于"索引已锁定"的幂等校验）。
func SameIndex(a, b *media.Index) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return proto.Equal(IndexToPB(a), IndexToPB(b))
}
