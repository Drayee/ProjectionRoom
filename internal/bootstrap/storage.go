package bootstrap

import (
	"context"
	"log"
	"time"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/storage"
)

// NewFileStore 构造对象存储客户端（图片等静态附件的落点）。
//
// # 为什么返回 (*storage.Client, error) 而不是 *storage.Client
//
// 与 NewMailer 的取舍不同：邮件未配置时**必须**能跑（账号层降级），
// 而对象存储"配了但连不上"是真错误 —— 静默降级会让"上传成功但图片 404"
// 变成运行期才发现的谜。所以：
//   - Endpoint 为空 → (nil, nil)，上传路由不注册（合法状态）
//   - Endpoint 有值但连不上 → 返回 error，启动失败
//
// # 为什么启动时就建桶
//
// 桶不存在时首次上传会失败，而用户看到的是"上传失败"，真正原因（桶没建）
// 只在服务端日志里。启动期把它建好，"配置正确"就变成了一个可验证的事实。
func NewFileStore(cfg *config.Config) (*storage.Client, error) {
	client, err := storage.New(storage.Config{
		Endpoint:      cfg.Storage.Endpoint,
		AccessKey:     cfg.Storage.AccessKey,
		SecretKey:     cfg.Storage.SecretKey,
		Bucket:        cfg.Storage.Bucket,
		UseSSL:        cfg.Storage.UseSSL,
		PublicBaseURL: cfg.Storage.PublicBaseURL,
	})
	if err != nil {
		return nil, err
	}
	if client == nil {
		log.Printf("[INFO] 对象存储未启用（%s 为空）：/api/uploads 不注册，图片仍只接受外链",
			"PR_STORAGE_ENDPOINT")
		return nil, nil
	}

	// 建桶 + 策略。给一个独立超时：MinIO 没起来时不应该把整个启动拖住，
	// 但也不该无限等待 —— 5 秒足够一个本机 MinIO 响应。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.EnsureBucket(ctx); err != nil {
		return nil, err
	}

	log.Printf("[INFO] 对象存储已启用：%s/%s（对外前缀 %s）",
		cfg.Storage.Endpoint, cfg.Storage.Bucket,
		firstNonEmpty(cfg.Storage.PublicBaseURL, "(同 Endpoint)"))
	return client, nil
}

// firstNonEmpty 返回第一个非空字符串（日志用的小工具）。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
