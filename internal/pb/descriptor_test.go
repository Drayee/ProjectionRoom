package pb

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestEnvelopeDescriptorMatchesGoStruct 锁死"手改的 rawDesc 与 Go 结构体/标签一致"。
//
// 为什么必须有这条测试：host_token(34) 是加在生成的 rawDesc 上的一个纯加法字段，
// 如果描述符与结构体标签不一致，线上表现会是"字段静默丢失"（proto3 未知字段被忽略），
// 而不是一次响亮的报错。这里用反射读描述符，逐字段核对编号/名称/类型/基数。
func TestEnvelopeDescriptorMatchesGoStruct(t *testing.T) {
	md := (&Envelope{}).ProtoReflect().Descriptor()

	want := map[protoreflect.FieldNumber]struct {
		name  protoreflect.Name
		kind  protoreflect.Kind
		card  protoreflect.Cardinality
		jsonN string
	}{
		1:  {"type", protoreflect.StringKind, protoreflect.Optional, "type"},
		9:  {"payload", protoreflect.BytesKind, protoreflect.Optional, "payload"},
		18: {"playback", protoreflect.MessageKind, protoreflect.Optional, "playback"},
		20: {"metrics", protoreflect.MessageKind, protoreflect.Optional, "metrics"},
		23: {"member", protoreflect.MessageKind, protoreflect.Optional, "member"},
		24: {"members", protoreflect.MessageKind, protoreflect.Repeated, "members"},
		25: {"media_index", protoreflect.MessageKind, protoreflect.Optional, "mediaIndex"},
		29: {"have", protoreflect.BytesKind, protoreflect.Optional, "have"},
		33: {"reason", protoreflect.StringKind, protoreflect.Optional, "reason"},
		34: {"host_token", protoreflect.StringKind, protoreflect.Optional, "hostToken"},
	}

	if got := md.Fields().Len(); got != 34 {
		t.Fatalf("Envelope 字段数应为 34，实际 %d（描述符与 .proto 不同步）", got)
	}

	for num, exp := range want {
		fd := md.Fields().ByNumber(num)
		if fd == nil {
			t.Fatalf("描述符缺少字段 %d(%s)", num, exp.name)
		}
		if fd.Name() != exp.name {
			t.Fatalf("字段 %d 名称应为 %s，实际 %s", num, exp.name, fd.Name())
		}
		if fd.Kind() != exp.kind {
			t.Fatalf("字段 %d(%s) 类型应为 %v，实际 %v", num, exp.name, exp.kind, fd.Kind())
		}
		if fd.Cardinality() != exp.card {
			t.Fatalf("字段 %d(%s) 基数应为 %v，实际 %v", num, exp.name, exp.card, fd.Cardinality())
		}
		if got := fd.JSONName(); got != exp.jsonN {
			t.Fatalf("字段 %d(%s) JSON 名应为 %s，实际 %s", num, exp.name, exp.jsonN, got)
		}
	}

	// 嵌套类型的字段编号没被动过：members(24) 的元素类型必须仍是 MemberInfo。
	if elem := md.Fields().ByNumber(24).Message(); elem == nil || elem.Name() != "MemberInfo" {
		t.Fatalf("members(24) 的元素类型应为 MemberInfo，实际 %v", elem)
	}
}

// TestHostTokenRoundTrip 证明 host_token 真的在线上传输（而不仅仅是躺在描述符里）。
func TestHostTokenRoundTrip(t *testing.T) {
	in := &Envelope{Type: "join", RoomId: "ABC123", HostToken: "0123456789abcdef"}
	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}

	var out Envelope
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if out.GetHostToken() != in.HostToken {
		t.Fatalf("host_token 未往返: 期望 %q，实际 %q", in.HostToken, out.GetHostToken())
	}
	if out.GetType() != "join" || out.GetRoomId() != "ABC123" {
		t.Fatalf("既有字段被破坏: %+v", &out)
	}

	// 旧客户端不发 34 号字段：解码后必须为空且不影响其它字段（向后兼容）。
	legacy := &Envelope{Type: "join", RoomId: "ABC123"}
	legacyRaw, err := proto.Marshal(legacy)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var legacyOut Envelope
	if err := proto.Unmarshal(legacyRaw, &legacyOut); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if legacyOut.GetHostToken() != "" || legacyOut.GetRoomId() != "ABC123" {
		t.Fatalf("旧报文应解码为无令牌且保留其它字段: %+v", &legacyOut)
	}
}
