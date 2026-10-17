package service

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/service/mail"
	"ProjectionRoom/internal/store"
)

// 两段式注册：**先定用户名与口令，再验证邮箱**。
//
// 为什么分两段而不是一次提交三个字段：
//
//	① 用户可见的顺序就是"先填用户名密码 → 翻一页填邮箱"，一次提交会逼前端
//	   把邮箱也塞进第一屏，而那正是要改掉的形态；
//	② 邮箱要花时间（收信、读码），把它和 bcrypt（约 0.5s）放在同一个请求里，
//	   会让"验证码输错"变成"口令要重新哈希一遍"；
//	③ 起草时就做完 bcrypt 并落库，第二段只剩一次 INSERT + 一次 UPDATE ——
//	   这样"点提交"到"拿到账号"之间没有昂贵操作。
//
// # 草稿的生命周期
//
//	register/start  → 校验用户名/口令 → bcrypt → 写 register_drafts → 返回 ticket
//	register/email  → 凭 ticket 挂邮箱并发出验证码
//	register/verify → 凭 ticket + 验证码 → 建号 → 消费草稿 → 开会话
//
// 草稿有 TTL（默认 15 分钟）且**不含明文口令**。过期后用户从头再来，
// 而"从头再来"的成本只有一次 bcrypt。

// 草稿策略常量。
const (
	// draftTTL 是一份注册草稿的存活时间。
	//
	// 15 分钟的理由：够用户去收信、输码（验证码本身只有 5 分钟，所以真正的
	// 瓶颈是它），又短到"忘了关页面"不会在库里留下一堆长期草稿。
	draftTTL = 15 * time.Minute
	// draftTicketBytes 是 ticket 的随机熵（32 字节，与其它不透明令牌同规格）。
	draftTicketBytes = 32
)

// RegisterStartResult 是第一段的产出。
type RegisterStartResult struct {
	// Ticket 是后续两段的凭据。**只在这一次响应里出现**（库里只有 sha256）。
	Ticket string
	// ExpiresIn 是草稿剩余有效期（秒），前端据此提示"请在 N 分钟内完成"。
	ExpiresIn int
}

// RegisterStart 处理注册第一段：校验用户名/显示名/口令，写草稿，返回 ticket。
//
// 它**不检查邮箱**（那时还没有邮箱），因此不构成"邮箱是否被占用"的枚举面。
// 用户名是否被占用则必须当场告诉用户（否则他要走完整个邮箱验证才发现白填），
// 这一点与既有 Register 的取舍一致：注册入口有 IP 限流 + bcrypt 成本，可接受。
func (s *AccountService) RegisterStart(ctx context.Context, username, displayName, password, ip, ua string) (*RegisterStartResult, error) {
	name, err := CanonicalUsername(username)
	if err != nil {
		return nil, err
	}
	display, err := SanitizeDisplayName(displayName, name)
	if err != nil {
		return nil, err
	}
	if err := auth.ValidatePasswordPolicy(password); err != nil {
		return nil, err
	}

	// 用户名已存在就没必要算哈希（那是 0.5s 的 CPU）。
	if _, err := s.store.UserByUsername(ctx, name); err == nil {
		return nil, ErrUsernameTaken
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	hash, err := s.acquireAndHash(password)
	if err != nil {
		return nil, err
	}

	ticket, ticketHash, err := newRandomToken(draftTicketBytes)
	if err != nil {
		return nil, err
	}
	now := s.now()
	d := &store.RegisterDraft{
		TicketHash:   ticketHash,
		Username:     name,
		DisplayName:  display,
		PasswordHash: hash,
		IP:           optString(ip),
		UserAgent:    optString(ua),
		CreatedAt:    now,
		ExpiresAt:    now.Add(draftTTL),
	}
	if err := s.store.InsertRegisterDraft(ctx, d); err != nil {
		return nil, err
	}
	log.Printf("[INFO] 注册草稿已创建：user=%s（15 分钟内完成邮箱验证）", name)
	return &RegisterStartResult{Ticket: ticket, ExpiresIn: int(draftTTL.Seconds())}, nil
}

// RegisterSendEmailOTP 处理第二段的上半：给草稿挂邮箱并发出验证码。
//
// 它接受"重复调用"（用户没收到信会再点一次），由冷却期挡住刷屏：
// 冷却期内直接返回 ErrOTPTooSoon（这里可以如实报错 —— 因为 ticket 本身就是
// 调用方持有的凭据，不存在"泄露账号存在性"的问题）。
func (s *AccountService) RegisterSendEmailOTP(ctx context.Context, ticket, email, ip, ua string) (devCode string, err error) {
	tok := strings.TrimSpace(ticket)
	if tok == "" {
		return "", ErrDraftTicketInvalid
	}
	addr, err := CanonicalEmail(email)
	if err != nil {
		return "", err
	}
	// 邮件能力缺失时的判定要**先看 dev echo**：
	// dev echo 存在的唯一理由就是"本机没有 SMTP 也要能把链路走通"，
	// 所以那种模式下"没有发信通道"不是失败（码会直接回显在响应里）。
	// 反过来（先判 mail == nil 再返回 503）会让这个开关彻底失去意义 ——
	// 它永远只能在"已经配好 SMTP"的环境里生效，而那时它又恰恰是被禁止的。
	if s.mail == nil && !s.otpDevEcho {
		return "", mail.ErrMailUnavailable
	}

	d, err := s.store.DraftByTicket(ctx, HashOpaqueToken(tok))
	if err != nil {
		if errors.Is(err, store.ErrDraftNotFound) {
			return "", ErrDraftTicketInvalid
		}
		return "", err
	}

	// 邮箱已被别的账号占用：必须当场说（否则用户输完码才发现）。
	// 这一步也顺手挡掉"用注册接口探测邮箱"的滥用 —— 它要持有一个有效 ticket。
	if _, err := s.store.UserByEmail(ctx, addr); err == nil {
		return "", ErrEmailTaken
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}

	// 冷却期：同一份草稿在 60 秒内只发一条码。
	//
	// 判据用草稿行自己的时间戳而不是"查最近一条 OTP"：草稿上的码是**唯一**的那条
	//（AttachDraftOTP 会覆盖旧码），所以"上次发码时刻"就写在草稿上更准。
	// 这里没法直接用 last-sent，因为那需要一张按邮箱索引的查询，
	// 而草稿路径的归属凭据是 ticket（邮箱可能还没挂上）。
	//
	// 简化实现：靠 attempts 与 AttachDraftOTP 的覆盖语义 —— 重发会覆盖旧码，
	// 于是"连点重发"的最坏后果是用户拿到最后一封。真正的防轰炸在 handler 的
	// IP 限流上（register 与 otp 各有一条桶），那才是这道闸门的落点。
	if d.Email != nil && *d.Email == addr && d.EmailOTPHash != nil {
		// 已有未消费的码且邮箱没变：允许覆盖（用户可能就是没收到），
		// 但要求间隔不小于冷却期 —— 用行上的 created_at 作近似。
		if s.now().Sub(d.CreatedAt) < otpCooldown {
			return "", ErrOTPTooSoon
		}
	}

	code, codeHash, err := newOTP()
	if err != nil {
		return "", err
	}
	now := s.now()
	expiresAt := now.Add(s.otpTTL)
	if err := s.store.AttachDraftOTP(ctx, HashOpaqueToken(tok), addr, codeHash, expiresAt); err != nil {
		if errors.Is(err, store.ErrDraftNotFound) {
			return "", ErrDraftTicketInvalid
		}
		return "", err
	}

	msg := mail.Outbound{
		To:      addr,
		Subject: otpEmailSubject(store.PurposeEmailVerify),
		Body:    otpEmailBody(store.PurposeEmailVerify, code, s.otpTTL),
		Tag:     "otp.register draft=" + d.Username,
	}
	if s.otpDevEcho {
		devCode = code
	}
	if s.mail == nil {
		// 只可能是 dev echo 模式（上面已经拦掉了另一种）：不投递，只回显。
		log.Printf("[INFO] 注册验证码已生成（dev echo，未投递）：%s", msg.Tag)
		return devCode, nil
	}
	if !s.mail.Enqueue(msg) {
		log.Printf("[WARN] 邮件队列已满或已关闭，丢弃注册验证码：%s", msg.Tag)
		return devCode, nil
	}
	log.Printf("[INFO] 注册验证码已入队：%s", msg.Tag)
	return devCode, nil
}

// RegisterVerify 处理第二段的下半：凭 ticket + 验证码建号并开会话。
//
// 幂等：由 handler 层的幂等键保护（scope=`register.verify`）。这里额外做一件事 ——
// **草稿的单次消费**（ConsumeDraftOTP 会把码清掉，ConsumeDraft 把草稿标记消费）。
// 两者叠加之后，"同一个 ticket 建两个号"需要同时绕过幂等键与草稿状态，不可能。
func (s *AccountService) RegisterVerify(ctx context.Context, ticket, code, ip, ua string) (*Session, error) {
	tok := strings.TrimSpace(ticket)
	if tok == "" {
		return nil, ErrDraftTicketInvalid
	}
	ticketHash := HashOpaqueToken(tok)

	d, ok, err := s.store.ConsumeDraftOTP(ctx, ticketHash, HashOpaqueToken(strings.TrimSpace(code)), otpMaxAttempts)
	if err != nil {
		if errors.Is(err, store.ErrOTPAttemptsExceeded) {
			return nil, ErrOTPAttemptsExceeded
		}
		if errors.Is(err, store.ErrOTPNotFound) {
			return nil, ErrOTPInvalid
		}
		return nil, err
	}
	if !ok || d == nil {
		return nil, ErrOTPInvalid
	}
	if d.Email == nil {
		// 理论上不可达：ConsumeDraftOTP 只在 email_otp_hash 非空时才成功。
		return nil, ErrDraftTicketInvalid
	}

	// 建号。口令哈希在第一段就算好了，这里直接搬。
	u := &store.User{
		Username:     d.Username,
		DisplayName:  d.DisplayName,
		PasswordHash: d.PasswordHash,
		Email:        d.Email,
	}
	if err := s.store.CreateUser(ctx, u); err != nil {
		// 用户名/邮箱在起草之后被抢走：如实报错（用户要重新起一次草稿）。
		return nil, err
	}

	// 建号即视为"邮箱已验证"：验证码刚刚证明了归属。
	//
	// 这是与"注册后发验证链接"最关键的语义差别：那条路径里邮箱是**未经证明**的，
	// 所以必须等用户点链接；这里已经证明了，再发一封链接信纯属多余。
	now := s.now()
	if err := s.store.SetUserEmail(ctx, u.ID, *d.Email, &now); err != nil {
		// 失败不回滚建号：账号已经可用，邮箱验证状态可以稍后由管理员补齐。
		log.Printf("[WARN] 注册成功但标记邮箱已验证失败：user=%d %v", u.ID, err)
	} else {
		// **必须回读**：SetUserEmail 只改了库，内存里的 u.EmailVerifiedAt 还是 nil，
		// 而 issueSession → ProfileOf(u) 用的就是这个内存对象 —— 不回读的话
		// 注册响应里会显示 emailVerified=false，而库里明明是已验证的。
		// 这种"响应与库不一致"最麻烦：用户看到未验证，管理端看到已验证。
		if fresh, err := s.store.UserByID(ctx, u.ID); err == nil && fresh != nil {
			u = fresh
		} else if err != nil {
			log.Printf("[WARN] 注册成功后回读账号失败（响应里的验证状态可能不准）：user=%d %v", u.ID, err)
		}
	}

	sess, err := s.issueSession(ctx, u, ip, ua)
	if err != nil {
		return nil, err
	}
	log.Printf("[INFO] 注册完成（邮箱已验证）：user=%d %s", u.ID, u.Username)
	return sess, nil
}
