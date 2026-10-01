// Package mp4 提供 fragmented MP4 的最小解析与按 moof 边界切分能力（SPEC §4.2）。
//
// 为什么必须按 moof 边界切：SourceBuffer.appendBuffer() 要求每个片段从一个完整的
// moof（+其后的 mdat）开始。按固定字节数切割会产生"半个 moof"，浏览器直接抛错。
package mp4

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// errTruncated 表示 box 数据不完整。
var errTruncated = fmt.Errorf("mp4: box 数据不完整")

// boxHeader 是一个 MP4 box 的头部信息。
type boxHeader struct {
	Type      string
	Offset    int64
	Size      int64 // 含头部的总长度；-1 表示延伸到文件末尾（size==0）
	HeaderLen int64 // 4/8/16 字节
}

// readBoxHeaderAt 从 r 的 off 处读取一个 box 头。
func readBoxHeaderAt(r io.ReaderAt, off int64) (boxHeader, error) {
	var buf [16]byte
	if _, err := r.ReadAt(buf[:8], off); err != nil {
		return boxHeader{}, fmt.Errorf("mp4: 读取 box 头失败 (offset=%d): %w", off, err)
	}

	size := int64(binary.BigEndian.Uint32(buf[0:4]))
	typ := string(buf[4:8])
	headerLen := int64(8)

	switch size {
	case 1:
		if _, err := r.ReadAt(buf[8:16], off+8); err != nil {
			return boxHeader{}, fmt.Errorf("mp4: 读取 largesize 失败 (offset=%d): %w", off, err)
		}
		size = int64(binary.BigEndian.Uint64(buf[8:16]))
		headerLen = 16
	case 0:
		size = -1
	}

	if size != -1 && size < headerLen {
		return boxHeader{}, fmt.Errorf("mp4: box %q 长度非法 (%d)", typ, size)
	}

	return boxHeader{Type: typ, Offset: off, Size: size, HeaderLen: headerLen}, nil
}

// box 是一个已解析的子 box：Type 是四字符类型，Data 是负载（不含头部）。
type box struct {
	Type string
	Data []byte
}

// parseChildren 遍历一段 box 负载中的所有子 box。
func parseChildren(payload []byte) ([]box, error) {
	var out []box

	for off := 0; off+8 <= len(payload); {
		size := int64(binary.BigEndian.Uint32(payload[off : off+4]))
		typ := string(payload[off+4 : off+8])
		headerLen := int64(8)

		switch size {
		case 1:
			if off+16 > len(payload) {
				return nil, errTruncated
			}
			size = int64(binary.BigEndian.Uint64(payload[off+8 : off+16]))
			headerLen = 16
		case 0:
			size = int64(len(payload) - off)
		}

		if size < headerLen || off+int(size) > len(payload) {
			return nil, fmt.Errorf("mp4: 子 box %q 长度非法 (size=%d, 剩余=%d)", typ, size, len(payload)-off)
		}

		out = append(out, box{Type: typ, Data: payload[off+int(headerLen) : off+int(size)]})
		off += int(size)
	}

	return out, nil
}

// findBox 返回第一个指定类型的子 box。
func findBox(boxes []box, typ string) *box {
	for i := range boxes {
		if boxes[i].Type == typ {
			return &boxes[i]
		}
	}
	return nil
}

// findBoxes 返回所有指定类型的子 box。
func findBoxes(boxes []box, typ string) []box {
	var out []box
	for _, b := range boxes {
		if b.Type == typ {
			out = append(out, b)
		}
	}
	return out
}

// boxBytes 把类型与负载组装成一个 box（仅测试与工具使用）。
func boxBytes(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// codecString 把若干编码串拼成 mimeType 所需的 codecs 参数。
func codecString(codecs []string) string {
	return `"` + strings.Join(codecs, ",") + `"`
}
