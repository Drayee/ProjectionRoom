package model

import (
	"errors"
	"testing"
)

func validIndex() *Index {
	return &Index{
		Version:       1,
		InitFile:      "init.mp4",
		MimeType:      `video/mp4; codecs="avc1.64001f"`,
		TotalDuration: 4,
		SegmentSec:    2,
		BitrateBps:    2_000_000,
		TotalBytes:    1_000_000,
		Segments: []Segment{
			{Index: 1, File: "c00001.m4s", Size: 500_000, Duration: 2, StartPTS: 0, Keyframe: true},
			{Index: 2, File: "c00002.m4s", Size: 500_000, Duration: 2, StartPTS: 2, Keyframe: true},
		},
	}
}

func TestValidateAcceptsWellFormedIndex(t *testing.T) {
	if err := validIndex().Validate(); err != nil {
		t.Fatalf("合法索引不应报错: %v", err)
	}
}

func TestValidateRejectsBrokenIndexes(t *testing.T) {
	cases := map[string]func(*Index){
		"版本非法":        func(i *Index) { i.Version = 0 },
		"缺少 init":     func(i *Index) { i.InitFile = " " },
		"mimeType 非法": func(i *Index) { i.MimeType = "application/octet-stream" },
		"总时长非正":       func(i *Index) { i.TotalDuration = 0 },
		"码率非正":        func(i *Index) { i.BitrateBps = 0 },
		"没有分片":        func(i *Index) { i.Segments = nil },
		"序号不连续":       func(i *Index) { i.Segments[1].Index = 5 },
		"分片大小非法":      func(i *Index) { i.Segments[0].Size = 0 },
		"缺少文件名":       func(i *Index) { i.Segments[0].File = "" },
		"时间字段为负":      func(i *Index) { i.Segments[1].StartPTS = -1 },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			index := validIndex()
			mutate(index)
			if err := index.Validate(); err == nil {
				t.Fatal("畸形索引必须被拒绝：服务端若接受它，整个房间的容量会算错")
			}
		})
	}
}

func TestValidateReportsEmptyIndex(t *testing.T) {
	index := validIndex()
	index.Segments = nil
	if err := index.Validate(); !errors.Is(err, ErrEmptyIndex) {
		t.Fatalf("空索引应返回 ErrEmptyIndex，实际 %v", err)
	}
}

// packedIndex 返回一份打包布局的合法索引：两片一个包，共两个包。
func packedIndex() *Index {
	index := validIndex()
	index.Segments = []Segment{
		{Index: 1, File: "pack-0001.bin", Offset: 0, Size: 300_000, Duration: 2, StartPTS: 0, Keyframe: true},
		{Index: 2, File: "pack-0001.bin", Offset: 300_000, Size: 200_000, Duration: 2, StartPTS: 2, Keyframe: true},
		{Index: 3, File: "pack-0002.bin", Offset: 0, Size: 400_000, Duration: 2, StartPTS: 4, Keyframe: true},
	}
	index.Packs = []Pack{
		{File: "pack-0001.bin", FirstSegment: 1, Count: 2, Bytes: 500_000},
		{File: "pack-0002.bin", FirstSegment: 3, Count: 1, Bytes: 400_000},
	}
	return index
}

// TestValidateAcceptsPackedIndex 锁定"两种形态都接受"：打包布局必须合法。
func TestValidateAcceptsPackedIndex(t *testing.T) {
	index := packedIndex()
	if !index.Packed() {
		t.Fatal("有 packs 的索引应报告 Packed() = true")
	}
	if err := index.Validate(); err != nil {
		t.Fatalf("合法的打包索引不应报错: %v", err)
	}
	// 分片数与文件数是两个概念：3 片装在 2 个包里。
	if len(index.Segments) != 3 || index.FileCount() != 2 {
		t.Fatalf("分片数应为 3、文件数应为 2，实际 %d/%d", len(index.Segments), index.FileCount())
	}
}

// TestValidateRejectsBrokenPacks 覆盖"能被写错而播放器看不出来"的那几种偏移错误。
func TestValidateRejectsBrokenPacks(t *testing.T) {
	cases := map[string]func(*Index){
		"包文件名重复":           func(i *Index) { i.Packs[1].File = "pack-0001.bin" },
		"firstSegment 不连续": func(i *Index) { i.Packs[1].FirstSegment = 5 },
		"count 为零":         func(i *Index) { i.Packs[0].Count = 0 },
		"bytes 为零":         func(i *Index) { i.Packs[0].Bytes = 0 },
		"包数不覆盖全部分片":        func(i *Index) { i.Packs = i.Packs[:1] },
		"分片不属于该包":          func(i *Index) { i.Segments[1].File = "pack-0002.bin" },
		"包内偏移重叠":           func(i *Index) { i.Segments[1].Offset = 100 },
		"越过包边界":            func(i *Index) { i.Segments[1].Size = i.Packs[0].Bytes },
		"包覆盖超界":            func(i *Index) { i.Packs[1].Count = 9 },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			index := packedIndex()
			mutate(index)
			if err := index.Validate(); err == nil {
				t.Fatal("畸形打包索引必须被拒绝：偏移错了客户端会切出半片数据")
			}
		})
	}
}

func TestSegmentAtAndMean(t *testing.T) {
	index := validIndex()

	if got := index.MeanSegmentSeconds(); got != 2 {
		t.Fatalf("平均分片时长应为 2，实际 %v", got)
	}
	if got := index.SegmentAt(0); got != 1 {
		t.Fatalf("SegmentAt(0) 应为 1，实际 %d", got)
	}
	if got := index.SegmentAt(2); got != 2 {
		t.Fatalf("SegmentAt(2) 应为 2，实际 %d", got)
	}
	if got := index.SegmentAt(10); got != 2 {
		t.Fatalf("越界位置应夹到最后一个分片，实际 %d", got)
	}

	empty := &Index{}
	if got := empty.SegmentAt(1); got != 0 {
		t.Fatalf("空索引应返回 0，实际 %d", got)
	}
	if got := empty.MeanSegmentSeconds(); got != 0 {
		t.Fatalf("空索引平均时长应为 0，实际 %v", got)
	}
}
