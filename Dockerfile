# Multi-stage build for ProjectionRoom (production)
#
# ⚠ 两处关键约束（都踩过）：
#  1. **Go 版本必须与 go.mod 一致**（go.mod 写 1.26.0）。用旧版 builder 镜像会在
#     `go mod download` 阶段直接报 "errors parsing go.mod" 并让整个构建失败 ——
#     报错点离真正原因很远，所以这里把版本写死在注释里。
#  2. 二进制必须交叉编译为 linux/amd64 且 CGO_ENABLED=0（静态链接），
#     否则运行阶段找不到动态库。
#
# Stage 1: Build
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /build

# Copy go mod files first (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build（**不要加 -tags wireinject**）
#
# ⚠ 这是踩过的坑：cmd/wire.go 带 `//go:build wireinject`（里面只有 wire.Build 声明，
# InitializeApp 体是空的，返回 nil），而 cmd/wire_gen.go 带 `//go:build !wireinject`
# （这才是真正的装配实现）。带 `-tags wireinject` 编译会选中前者，
# 结果是 InitializeApp 返回 nil，服务启动后立刻 SIGSEGV —— 而且编译期毫无警告。
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o projectionroom ./cmd

# Stage 2: Runtime
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/projectionroom .

# Copy client dist (前端产物由 CI 的 npm 步骤生成后一起打进镜像)
COPY --from=builder /build/client/dist ./client/dist

# Expose port
EXPOSE 8080

# Set environment variables
ENV TZ=Asia/Shanghai
ENV PR_SERVE_STATIC=true

# Run as non-root user
RUN adduser -D appuser && chown -R appuser:appuser /app
USER appuser

CMD ["/app/projectionroom"]
