// Package model 的 proto 预算闸门：**解码之前**按 wire format 扫一遍顶层帧。
//
// 为什么必须在 Unmarshal 之前：protobuf 的 repeated 字段在 Go 侧会被展开成
// 元素数量线性增长的堆对象。审计实测一帧 4,194,300 字节（field 24 重复 1,398,100 次）
// 把整机 RSS 从 22 MB 顶到 3,947 MB，关闭连接后不归还 —— 一次未认证的请求
// 就能把服务打死。长度上限（PR_SIGNAL_MAX_MESSAGE_BYTES）在这里完全无效：
// 攻击帧本来就小于上限。
//
// 所以本文件只做一件事：在**任何分配之前**把帧的"展开成本"算清楚，超预算直接拒。
// 计数只增不减，所以拒绝发生在读到第 N+1 个元素的那一刻 —— 4 MB 攻击帧的全部
// 内存成本就是它自己那份读缓冲。
//
// 本文件不认识业务语义，只认识下面 sch* 里列出的字段编号；编号与
// proto/projection_room.proto 必须保持一致（改了 proto 就要改这里，wire_test.go 锁住边界）。
package model

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/encoding/protowire"
)

// 顶层消息（Envelope）的字段编号。
const (
	fEnvelopeType        protowire.Number = 1
	fEnvelopePayload     protowire.Number = 9
	fEnvelopeMember      protowire.Number = 23
	fEnvelopeMembers     protowire.Number = 24
	fEnvelopeMediaIndex  protowire.Number = 25
	fEnvelopeCapacity    protowire.Number = 26
	fEnvelopeTopology    protowire.Number = 27
	fEnvelopeDistributor protowire.Number = 28
	fEnvelopeHave        protowire.Number = 29
)

// 嵌套消息的字段编号。
const (
	fMediaIndexSegments protowire.Number = 8
	fMemberInfoBackups  protowire.Number = 6
	fTopologyBackups    protowire.Number = 3
	fTopologyChildren   protowire.Number = 4
)

// 每种预算的计量单位（错误信息与日志用）。
const (
	unitElements = "个元素"
	unitBytes    = "字节"
	unitDepth    = "层嵌套"
	unitOccurs   = "次出现"
)

// WireBudget 是"解码之前的元素预算"。每一项都能由环境变量覆盖（见 internal/config）。
type WireBudget struct {
	// MaxRepeatedElements 是整帧允许展开的 repeated 元素总数（含重复 string）。
	//
	// 默认 16384 的依据：仓库里最大的一帧是主播发布的 index.json（实测 14000 片、
	// 1.39 MB），它整帧只有 14000 个元素，正好落在上限内；真实信令帧是
	// "一条成员表（≤ 房间人数）"量级，离上限三个数量级。攻击帧要做的是
	// "1,398,100 个元素"，因此这一条就把它挡在 Unmarshal 之前。
	MaxRepeatedElements int
	// MaxMembersPerMessage 是 members(24) 单字段元素上限（重复 string 字段共用它）。
	//
	// 默认 256 的依据：房间成员硬上限是 PR_MAX_MEMBERS（默认 16）。256 = 16 倍余量，
	// 同时把最恶劣的放大源（3 字节/元素）按死：即使每个元素撑到 1 KiB，
	// 也只有 256 KiB。
	MaxMembersPerMessage int
	// MaxFieldBytes 是单个 string/bytes 字段的字节上限。
	//
	// 默认 1 MiB 的依据：have 位图按 1 bit/片算，1 MiB = 838 万片
	// （2s/片 ≈ 4.6 年视频），SDP 实测几 KB。需要更大位图的形态应当改成紧凑位图，
	// 而不是抬高这个值。
	MaxFieldBytes int
	// MaxSegmentsPerIndex 是 media_index.segments(8) 单字段元素上限。
	// 默认 8192：覆盖"2s/片 的 4.5 小时视频（8100 片）"。更长的片子由
	// PR_SIGNAL_MAX_SEGMENTS 显式抬高（上限 1,000,000），而不是被默认值弄坏。
	MaxSegmentsPerIndex int
	// MaxNestedBytes 是单条嵌套元素（一个成员/一个分片）的字节上限。
	// 默认 4 MiB，与单条信令上限同量级：正常值 < 1 KiB。
	MaxNestedBytes int
}

// wireBudgetError 说明哪一个预算被突破、实际多少、上限多少。
type wireBudgetError struct {
	Field string
	Limit int64
	Got   int64
	Unit  string
}

func (e *wireBudgetError) Error() string {
	return fmt.Sprintf("protocol: 帧预算超限：%s %s %d > 上限 %d（拒绝解码）", e.Field, e.Unit, e.Got, e.Limit)
}

// schField 是"已知字段"的检查规则。
type schField struct {
	name string
	// maxBytes > 0 表示该字段按字节数卡（string 或 bytes）；0 表示用预算默认上限。
	maxBytes int
	// child 非 nil 表示嵌套消息（Envelope 上的重复消息字段会在这里按元素计数）。
	child *schMessage
	// singular 表示 proto3 单值字段：同一条消息里出现两次即为畸形。
	singular bool
}

// schMessage 描述一条已知消息的字段表。
//
// **未知编号一律跳过**（既不递归也不计数）：这样"客户端比服务端新"仍然可用；
// 而攻击者造一个服务端不认识的重复字段也拿不到放大效果 ——
// 生成的结构体里没有对应字段，Unmarshal 不会为它分配堆对象。
type schMessage struct {
	name   string
	fields map[protowire.Number]*schField
}

// schMemberInfo 对应 proto 里的 MemberInfo：唯一的 repeated 是 backup_ids(6)
// （拓扑备份父节点，正常 < 4 个）。
var schMemberInfo = &schMessage{
	name: "MemberInfo",
	fields: map[protowire.Number]*schField{
		fMemberInfoBackups: {name: "backup_ids"},
	},
}

// schSegment 对应 proto 里的 MediaSegment（全 flat 字段，没有 repeated）。
var schSegment = &schMessage{name: "MediaSegment", fields: map[protowire.Number]*schField{}}

// schMediaIndex 对应 proto 里的 MediaIndex ——"索引相关的 repeated"就是 segments(8)。
var schMediaIndex = &schMessage{
	name: "MediaIndex",
	fields: map[protowire.Number]*schField{
		fMediaIndexSegments: {name: "segments", child: schSegment},
	},
}

// schTopology 对应 proto 里的 TopologyAssignment：
// backup_ids(3) 与 children(4) 都是 repeated string。
var schTopology = &schMessage{
	name: "TopologyAssignment",
	fields: map[protowire.Number]*schField{
		fTopologyBackups:  {name: "backup_ids"},
		fTopologyChildren: {name: "children"},
	},
}

// schEnvelope 对应 proto 里的 Envelope —— **唯一从线上收到的顶层消息**。
//
// 这就是 S-1 要求的"所有 repeated 字段清单"（字段编号:名称 → proto 行号）：
//
//	24:members                   repeated MemberInfo    ← 审计实证的放大源，单字段上限 256
//	25:media_index.segments(8)   repeated MediaSegment  ← 索引，单字段上限 MaxSegmentsPerIndex
//	23:member(单值).backup_ids(6) repeated string       ← 计入整帧元素预算
//	27:topology(单值).backup_ids(3) repeated string
//	27:topology(单值).children(4)   repeated string
//	29:have   bytes（不是 repeated，但同样是无界分配 → 按字节卡）
//	 9:payload bytes（SDP/ICE 透传 → 按字节卡）
//
// 其余字段全是标量（string/int64/double/bool），没有放大空间。
var schEnvelope = &schMessage{
	name: "Envelope",
	fields: map[protowire.Number]*schField{
		fEnvelopeType:        {name: "type", maxBytes: 64},
		fEnvelopePayload:     {name: "payload", maxBytes: 0},
		fEnvelopeHave:        {name: "have", maxBytes: 0},
		fEnvelopeMember:      {name: "member", child: schMemberInfo, singular: true},
		fEnvelopeMembers:     {name: "members", child: schMemberInfo},
		fEnvelopeMediaIndex:  {name: "media_index", child: schMediaIndex, singular: true},
		fEnvelopeTopology:    {name: "topology", child: schTopology, singular: true},
		fEnvelopeCapacity:    {name: "capacity", child: &schMessage{name: "Capacity", fields: map[protowire.Number]*schField{}}, singular: true},
		fEnvelopeDistributor: {name: "distributor", child: &schMessage{name: "DistributorChange", fields: map[protowire.Number]*schField{}}, singular: true},
	},
}

// ScanWireBudget 在解码前扫描顶层帧，逐个核对预算。
//
// 返回 nil 表示"可以安全解码"；返回非 nil 表示必须拒绝（**不要**再 Unmarshal）。
func ScanWireBudget(data []byte, b WireBudget) error {
	bud := &wireBudgetState{budget: b.withDefaults()}
	return scanMessage(data, schEnvelope, bud, 0)
}

// State 暴露给调用方用于日志的诊断计数（目前主要是元素数与字节数）。
type State struct {
	Elements int
	Bytes    int
}

// ScanWireBudgetWithState 与 ScanWireBudget 相同，但额外返回扫描统计。
// 日志里带上"这帧展开成了多少个元素"能让"是不是放大帧"一眼可判。
func ScanWireBudgetWithState(data []byte, b WireBudget) (State, error) {
	bud := &wireBudgetState{budget: b.withDefaults()}
	err := scanMessage(data, schEnvelope, bud, 0)
	return State{Elements: bud.elements, Bytes: bud.bytes}, err
}

// wireBudgetState 是扫描的累计状态（跨整帧，不只单条消息）。
type wireBudgetState struct {
	budget   WireBudget
	elements int
	bytes    int
}

func (s *wireBudgetState) budgetErr(field string, limit, got int64, unit string) error {
	return &wireBudgetError{Field: field, Limit: limit, Got: got, Unit: unit}
}

// scanMessage 递归扫描一条消息。depth 只用于报错与防呆深度上限。
func scanMessage(data []byte, sch *schMessage, bud *wireBudgetState, depth int) error {
	// 真实结构最多 2 层（Envelope → MediaIndex → MediaSegment）；8 层是防呆。
	if depth > 8 {
		return bud.budgetErr(sch.name, 8, int64(depth), unitDepth)
	}

	// occ 记录"每个已知字段在本条消息里出现了几次"：
	// 单值字段第二次出现即畸形，重复消息字段用它卡单字段上限。
	occ := make(map[protowire.Number]int, len(sch.fields))

	pos := 0
	for pos < len(data) {
		num, typ, n := protowire.ConsumeTag(data[pos:])
		if n < 0 {
			return fmt.Errorf("protocol: 帧解析失败（%s 的字段号处）: %w", sch.name, protowire.ParseError(n))
		}
		pos += n

		field := sch.fields[num]
		if field == nil {
			// 未知字段：按线类型整体跳过。不递归、不计数。
			m := protowire.ConsumeFieldValue(num, typ, data[pos:])
			if m < 0 {
				return fmt.Errorf("protocol: 帧解析失败（%s 字段 %d）: %w", sch.name, num, protowire.ParseError(m))
			}
			pos += m
			continue
		}

		// 只对 length-delimited 字段（string/bytes/nested）做预算；其余标量整体跳过。
		if typ != protowire.BytesType {
			if field.child != nil || field.maxBytes != 0 {
				// 已知是 string/bytes/nested 的字段却给了别的线类型：伪造帧。
				return bud.budgetErr(field.name, 1, int64(typ), "线类型")
			}
			m := protowire.ConsumeFieldValue(num, typ, data[pos:])
			if m < 0 {
				return fmt.Errorf("protocol: 帧解析失败（%s 字段 %d）: %w", sch.name, num, protowire.ParseError(m))
			}
			pos += m
			continue
		}

		val, m := protowire.ConsumeBytes(data[pos:])
		if m < 0 {
			return fmt.Errorf("protocol: 帧解析失败（%s 的 %s）: %w", sch.name, field.name, protowire.ParseError(m))
		}
		pos += m

		occ[num]++
		if field.singular && occ[num] > 1 {
			return bud.budgetErr(field.name, 1, int64(occ[num]), unitOccurs)
		}

		// 1) 单字段元素上限：members 用 MaxMembersPerMessage，
		//    media_index 里的 segments 用 MaxSegmentsPerIndex。
		if limit := bud.budget.fieldCountLimit(num, sch); limit > 0 && occ[num] > limit {
			return bud.budgetErr(field.name, int64(limit), int64(occ[num]), unitElements)
		}

		// 2) 整帧元素预算（每出现一次已知 length-delimited 字段 = 1 个元素）。
		bud.elements++
		if bud.elements > bud.budget.MaxRepeatedElements {
			return bud.budgetErr(field.name, int64(bud.budget.MaxRepeatedElements), int64(bud.elements), unitElements)
		}

		// 3) 单字段字节上限。
		limit := field.maxBytes
		if limit == 0 {
			limit = bud.budget.MaxFieldBytes
		}
		if len(val) > limit {
			return bud.budgetErr(field.name, int64(limit), int64(len(val)), unitBytes)
		}
		bud.bytes += len(val)

		// 4) 递归检查嵌套消息。
		if field.child == nil {
			continue
		}
		if len(val) > bud.budget.MaxNestedBytes {
			return bud.budgetErr(field.name, int64(bud.budget.MaxNestedBytes), int64(len(val)), unitBytes)
		}
		if err := scanMessage(val, field.child, bud, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// fieldCountLimit 返回某字段在该消息里的元素个数上限（0 = 不单独卡）。
//
//   - Envelope.members        → MaxMembersPerMessage
//   - MediaIndex.segments     → MaxSegmentsPerIndex
//   - MemberInfo.backup_ids / TopologyAssignment.backup_ids/children → MaxMembersPerMessage
//
// 其余字段返回 0，只受整帧元素预算约束。
func (b WireBudget) fieldCountLimit(num protowire.Number, sch *schMessage) int {
	if sch == schEnvelope {
		switch num {
		case fEnvelopeMembers:
			return b.MaxMembersPerMessage
		case fEnvelopeMediaIndex:
			return 0 // 单值：它内部的 segments 由 schMediaIndex 那条限制卡
		default:
			return 0
		}
	}
	switch num {
	case fMediaIndexSegments:
		return b.MaxSegmentsPerIndex
	case fMemberInfoBackups, fTopologyBackups, fTopologyChildren:
		return b.MaxMembersPerMessage
	default:
		return 0
	}
}

func (b WireBudget) withDefaults() WireBudget {
	if b.MaxRepeatedElements <= 0 {
		b.MaxRepeatedElements = 16384
	}
	if b.MaxMembersPerMessage <= 0 {
		b.MaxMembersPerMessage = 256
	}
	if b.MaxFieldBytes <= 0 {
		b.MaxFieldBytes = 1 << 20
	}
	if b.MaxSegmentsPerIndex <= 0 {
		b.MaxSegmentsPerIndex = 8192
	}
	if b.MaxNestedBytes <= 0 {
		b.MaxNestedBytes = 4 << 20
	}
	return b
}

// ActiveWireFields 返回本扫描器实际认识的 Envelope 字段（按编号排序），形如 "24:members"。
// S-1 要求"盘一遍所有 repeated 字段并列表"，这份清单是那件事的机器可验证版本。
func ActiveWireFields() []string {
	nums := make([]int, 0, len(schEnvelope.fields))
	for num := range schEnvelope.fields {
		nums = append(nums, int(num))
	}
	sort.Ints(nums)

	out := make([]string, 0, len(nums))
	for _, n := range nums {
		out = append(out, fmt.Sprintf("%d:%s", n, schEnvelope.fields[protowire.Number(n)].name))
	}
	return out
}

// RepeatedFields 返回**重量级 repeated 字段**清单（S-1 报告里的字段列表）。
func RepeatedFields() []string {
	return []string{
		"24:members（repeated MemberInfo，单字段上限 256）",
		"25:media_index.segments（repeated MediaSegment，单字段上限 8192）",
		"23:member.backup_ids（repeated string）",
		"27:topology.backup_ids（repeated string）",
		"27:topology.children（repeated string）",
		"29:have（bytes，按字节卡 1 MiB）",
		"9:payload（bytes，按字节卡 1 MiB）",
	}
}
