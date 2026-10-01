package media

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
