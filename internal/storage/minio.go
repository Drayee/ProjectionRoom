// Package storage 封装对象存储（MinIO / S3 兼容）的**唯一出口**。
//
// # 为什么单独一个包
//
// 上层（handler）不应该知道"文件存在哪、怎么取 URL"。把 S3 SDK 的调用收在这里，
// 将来换实现（本地磁盘、阿里云 OSS、R2）只动这个文件，不动业务代码。
//
// # 与不变量 I1 的关系
//
// I1 说的是**直播链路**上服务器不传输任何视频字节。本包只服务"图片等静态附件"，
// 直播分片仍然只在 peer 之间经 WebRTC DataChannel 流动。任何人往这里加
// "把视频分片存进桶"的路径之前，请先读 docs/ALGORITHM.md §0。
package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrUnavailable 表示对象存储未配置或不可用。
//
// 上层据此返回 503 而不是 500：这是"能力未开启"，不是"服务出错"。
// 与账号层"DSN 为空即不注册 /api/auth/*"是同一条约定。
var ErrUnavailable = errors.New("storage: 对象存储未启用")

// ErrUnsupportedType 表示上传的文件类型不在白名单内。
var ErrUnsupportedType = errors.New("storage: 不支持的文件类型")

// ErrTooLarge 表示文件超过单文件上限。
var ErrTooLarge = errors.New("storage: 文件超过大小上限")

// MaxImageBytes 是单张图片的大小上限（10 MiB）。
//
// 定这个值而不是更大：这是**头像与帖子配图**，不是原图存档。
// 10 MiB 已经能容纳手机直出的 4000x3000 JPEG，再大只会拖慢页面。
const MaxImageBytes = 10 << 20

// allowedImageTypes 是允许上传的 MIME 类型白名单。
//
// 只收位图，**刻意不收 SVG**：SVG 是 XML，可以内嵌 <script>，
// 一旦同源托管就等于给自己开了一个 XSS 入口（历史上这是最常见的
// "图片上传导致存储型 XSS"的来源）。要支持矢量图必须走独立的、
// 强 Content-Security-Policy 的域。
var allowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// Config 是对象存储的连接参数。
type Config struct {
	// Endpoint 是 S3 API 地址（host:port，不带 scheme）。
	Endpoint string
	// AccessKey / SecretKey 是凭据。
	AccessKey string
	SecretKey string
	// Bucket 是目标桶名。
	Bucket string
	// UseSSL 表示走 https。
	UseSSL bool
	// PublicBaseURL 是浏览器访问对象的**对外前缀**（如 https://cdn.example.com）。
	//
	// 为什么要单独一个字段而不是拼 Endpoint：Endpoint 通常是内网/回环地址
	// （127.0.0.1:9000），浏览器访问不到它；而对外前缀可能是经过反代的域名。
	// 两者是不同的事实，混用会让"上传成功但图片显示不出来"。
	PublicBaseURL string
}

// Client 是对象存储的窄接口实现。
type Client struct {
	mc     *minio.Client
	cfg    Config
	bucket string
}

// New 构造客户端。
//
// Endpoint 为空时返回 (nil, nil)：调用方据此**不注册**上传路由
// （与账号层 DSN 为空的做法一致）。这不是错误，是"该能力未开启"。
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, nil
	}
	if cfg.Bucket == "" {
		return nil, errors.New("storage: 缺少 bucket 名")
	}

	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: 构造 MinIO 客户端失败: %w", err)
	}
	return &Client{mc: mc, cfg: cfg, bucket: cfg.Bucket}, nil
}

// EnsureBucket 确认桶存在，不存在则创建，并设置为匿名可读。
//
// 为什么匿名可读：桶里只有用户上传的图片，它们本来就要公开展示
// （<img src> 无法携带凭据）。写入仍然需要凭据 —— 只有本服务持有。
//
// 为什么启动时做而不是首次上传时做：首次上传时才发现桶不存在，
// 用户看到的是"上传失败"，而真正的原因在启动期就该暴露。
func (c *Client) EnsureBucket(ctx context.Context) error {
	if c == nil {
		return ErrUnavailable
	}
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("storage: 检查桶失败: %w", err)
	}
	if !exists {
		if err := c.mc.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("storage: 创建桶失败: %w", err)
		}
		log.Printf("[INFO] 对象存储桶已创建：%s", c.bucket)
	}

	// 只读策略：允许匿名 s3:GetObject。
	// 不给 ListBucket：列目录没有业务需要，给了只会方便别人枚举全部文件。
	policy := fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {"AWS": ["*"]},
      "Action": ["s3:GetObject"],
      "Resource": ["arn:aws:s3:::%s/*"]
    }
  ]
}`, c.bucket)
	if err := c.mc.SetBucketPolicy(ctx, c.bucket, policy); err != nil {
		return fmt.Errorf("storage: 设置桶策略失败: %w", err)
	}
	return nil
}

// PutImage 保存一张图片，返回可公开访问的 URL。
//
// 参数：
//   - r       内容流（调用方负责关闭）
//   - size    字节数；<=0 表示未知（此时由 SDK 分块上传）
//   - mime    声明的 Content-Type
//
// 校验顺序是刻意的：先类型（廉价）再大小（可能要读完流），
// 让"传了个 .exe"这种明显错误尽快失败。
func (c *Client) PutImage(ctx context.Context, r io.Reader, size int64, mime string) (string, error) {
	if c == nil {
		return "", ErrUnavailable
	}

	mime = strings.ToLower(strings.TrimSpace(mime))
	// 去掉 "image/jpeg; charset=..." 这类参数
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	ext, ok := allowedImageTypes[mime]
	if !ok {
		return "", ErrUnsupportedType
	}

	if size > MaxImageBytes {
		return "", ErrTooLarge
	}

	key, err := newObjectKey(ext)
	if err != nil {
		return "", err
	}

	// size <= 0 时传 -1，让 SDK 用分块上传（它自己缓冲，不会把整个文件读进内存）
	putSize := size
	if putSize < 0 {
		putSize = -1
	}

	info, err := c.mc.PutObject(ctx, c.bucket, key, r, putSize, minio.PutObjectOptions{
		ContentType: mime,
		// 图片内容不变，可以长缓存；key 里含随机串，改图必然换 key
		CacheControl: "public, max-age=31536000, immutable",
	})
	if err != nil {
		return "", fmt.Errorf("storage: 上传失败: %w", err)
	}

	// 兜底：声明的 size 不可信时（客户端可能撒谎），SDK 返回的真实大小才是判据
	if info.Size > MaxImageBytes {
		// 已经传上去了，删掉再报错 —— 不能留下一个超大对象
		_ = c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
		return "", ErrTooLarge
	}

	return c.publicURL(key), nil
}

// Remove 删除一个对象（按公开 URL 反解 key）。
//
// 失败只返回错误，不做重试：调用方（例如删帖时清理图片）应当记日志，
// 而不是让整个删除操作回滚 —— 一张图没删掉比帖子删不掉轻得多。
func (c *Client) Remove(ctx context.Context, publicURL string) error {
	if c == nil {
		return ErrUnavailable
	}
	key := c.keyFromURL(publicURL)
	if key == "" {
		return errors.New("storage: 无法从 URL 解析对象 key")
	}
	return c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
}

// publicURL 拼出对外可访问的 URL。
func (c *Client) publicURL(key string) string {
	base := strings.TrimSuffix(c.cfg.PublicBaseURL, "/")
	if base == "" {
		// 没配对外前缀时退回 Endpoint（本地开发直连 MinIO 的场景）
		scheme := "http"
		if c.cfg.UseSSL {
			scheme = "https"
		}
		base = scheme + "://" + c.cfg.Endpoint
	}
	return base + "/" + c.bucket + "/" + key
}

// keyFromURL 从公开 URL 反解出对象 key。
//
// 只处理本客户端生成的 URL 形态（<base>/<bucket>/<key>）；解不出就返回空串，
// 调用方据此跳过删除。刻意不做"猜测"——删错对象比不删更糟。
func (c *Client) keyFromURL(u string) string {
	marker := "/" + c.bucket + "/"
	i := strings.LastIndex(u, marker)
	if i < 0 {
		return ""
	}
	return u[i+len(marker):]
}

// Enabled 报告对象存储是否可用。
func (c *Client) Enabled() bool { return c != nil }

// newObjectKey 生成对象 key：<yyyy>/<mm>/<random>.<ext>
//
// 为什么要按年月分目录：桶里文件多了以后，控制台与 mc 列目录会变得很慢；
// 分目录让"某个月传了什么"可以直接定位，也方便将来按时间做生命周期策略。
//
// 为什么用 16 字节随机而不是原始文件名：原始文件名可能含路径分隔符、
// 中文、甚至 ..（目录穿越），而且会泄露用户设备的命名习惯。
// 随机 key 让这些都变成非问题，代价是丢了可读性 —— 图片不需要可读性。
func newObjectKey(ext string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("storage: 生成对象 key 失败: %w", err)
	}
	now := time.Now()
	return path.Join(
		now.Format("2006"),
		now.Format("01"),
		hex.EncodeToString(buf)+ext,
	), nil
}
