package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

// SegmenterManifestKey 是切片器清单在桶里的对象 key。
//
// 为什么把清单放对象存储而不是每次启动去列桶：
//   - 列桶要遍历全部对象（桶里主要是用户图片，数量会持续增长）；
//   - 清单需要 sha256 与字节数，那要逐个对象读一遍 —— 5 个 17MB 的二进制，
//     启动时读 82MB 显然不可接受。
//
// 所以由 CI 在构建完二进制后**同时生成这份 JSON** 并上传，服务端只读一个小文件。
const SegmenterManifestKey = "segmenter/manifest.json"

// SegmenterArtifact 是清单里的一条平台记录。
//
// 字段与 handler 的 SegmenterDownload 对齐（那边是 JSON 契约，这里是存储侧的
// 中间表示）。分开定义而不是共用：存储层的形状变了不该直接改到 API 契约上。
type SegmenterArtifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	File   string `json:"file"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// SegmenterManifest 是 CI 产出的清单文件。
type SegmenterManifest struct {
	Version   string              `json:"version"`
	Platforms []SegmenterArtifact `json:"platforms"`
}

// FetchSegmenterManifest 从桶里读切片器清单。
//
// 返回 (nil, nil) 表示"清单不存在"——这是合法状态（CI 还没传过），
// 调用方据此回落到本地目录扫描。真正的传输/解析错误才返回 error。
func (c *Client) FetchSegmenterManifest(ctx context.Context) (*SegmenterManifest, error) {
	if c == nil {
		return nil, nil
	}

	obj, err := c.mc.GetObject(ctx, c.bucket, SegmenterManifestKey, minio.GetObjectOptions{})
	if err != nil {
		// GetObject 对不存在的对象是延迟报错的（在第一次 Read 时），
		// 所以这里不能直接判 err，要继续往下读到 Stat/Decode 才知道。
		var resp minio.ErrorResponse
		if errors.As(err, &resp) && resp.Code == "NoSuchKey" {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: 打开切片器清单失败: %w", err)
	}
	defer func() { _ = obj.Close() }()

	// Stat 才会真正发请求；对象不存在时在这里返回 NoSuchKey。
	if _, err := obj.Stat(); err != nil {
		var resp minio.ErrorResponse
		if errors.As(err, &resp) && (resp.Code == "NoSuchKey" || resp.StatusCode == 404) {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: 读取切片器清单元信息失败: %w", err)
	}

	// 清单很小（几百字节），限一个上限防止被塞入超大对象
	const maxManifestBytes = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(obj, maxManifestBytes))
	if err != nil {
		return nil, fmt.Errorf("storage: 读取切片器清单失败: %w", err)
	}

	var m SegmenterManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("storage: 解析切片器清单失败: %w", err)
	}
	return &m, nil
}

// SegmenterPublicURL 返回某个切片器二进制的公开下载地址。
//
// 走与图片相同的对外前缀（<base>/<bucket>/<key>），因此经 nginx 的
// /files/ 反代即可下载，不需要额外的路由。
func (c *Client) SegmenterPublicURL(file string) string {
	return c.publicURL("segmenter/" + strings.TrimPrefix(file, "/"))
}

// PutSegmenterBinary 上传一个切片器二进制（CI 用，服务端不调用）。
//
// 单独一个方法而不是复用 PutImage：后者有图片类型白名单与 10 MiB 上限，
// 而这里要传 17MB 的 ELF/PE 文件，两者规则完全不同。
func (c *Client) PutSegmenterBinary(ctx context.Context, name string, r io.Reader, size int64, sha256Hex string) error {
	if c == nil {
		return ErrUnavailable
	}
	_, err := c.mc.PutObject(ctx, c.bucket, "segmenter/"+name, r, size, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
		// 二进制内容随版本变，但 key 不变（固定文件名），所以**不能**长缓存：
		// 否则用户会拿到上一版的 exe 却配着新清单的 sha256，校验必然失败。
		CacheControl: "no-cache",
		UserMetadata: map[string]string{"sha256": sha256Hex},
	})
	if err != nil {
		return fmt.Errorf("storage: 上传切片器二进制失败: %w", err)
	}
	return nil
}

// PutSegmenterManifest 上传清单（CI 用）。
func (c *Client) PutSegmenterManifest(ctx context.Context, m *SegmenterManifest) error {
	if c == nil {
		return ErrUnavailable
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("storage: 序列化切片器清单失败: %w", err)
	}
	_, err = c.mc.PutObject(ctx, c.bucket, SegmenterManifestKey,
		strings.NewReader(string(raw)), int64(len(raw)), minio.PutObjectOptions{
			ContentType:  "application/json",
			CacheControl: "no-cache",
		})
	if err != nil {
		return fmt.Errorf("storage: 上传切片器清单失败: %w", err)
	}
	return nil
}

// 保证 time 被用到（PutObjectOptions 里没有时间字段，但保留导入以便将来加 TTL）。
var _ = time.Now
