package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ProjectionRoom/internal/config"
	"ProjectionRoom/internal/model"
	"ProjectionRoom/internal/service"
	"ProjectionRoom/internal/service/segment"
)

// segmentServer 起一个带切片端点的真实 HTTP 服务。
func segmentServer(t *testing.T, mutate func(*config.Config)) (*httptest.Server, *config.Config) {
	t.Helper()

	cfg := config.Default()
	cfg.Segment.TempDir = t.TempDir()
	// 单测里不指望后台清理协程，周期设长避免它随机插手。
	cfg.Segment.CleanupInterval = time.Hour
	if mutate != nil {
		mutate(cfg)
	}

	hub, hubCleanup, err := service.NewHub(cfg)
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	rooms := service.NewManager(cfg, hub)

	queue, queueCleanup, err := segment.NewQueue(cfg)
	if err != nil {
		t.Fatalf("构造切片队列失败: %v", err)
	}

	// 切片用例不碰账号面：空 AuthDeps（DSN 仍为空 → 认证与管理端路由都不注册）。
	srv := httptest.NewServer(NewRouter(cfg, hub, rooms, queue, AuthDeps{}, AdminDeps{}))
	t.Cleanup(func() {
		srv.Close()
		// NewRouter 会启动房间清扫协程（S-3）：测试结束必须停掉，避免用例间互相干扰。
		rooms.Stop()
		queueCleanup()
		hubCleanup()
	})
	return srv, cfg
}

// postFile 用 multipart/form-data 上传一个文件字段。
func postFile(t *testing.T, url, field, name string, payload []byte) *http.Response {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(field, name)
	if err != nil {
		t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("写入 multipart 失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}

	resp, err := http.Post(url, writer.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("POST %s 失败: %v", url, err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
}

const segmentJobsURL = "/api/v1/segment/jobs"

// TestSegmentSubmitAccepted 覆盖 202：返回 jobId / state / queuePosition，
// 且作业可以被立刻查询到。
func TestSegmentSubmitAccepted(t *testing.T) {
	tools := requireSegmentFFmpeg(t)
	srv, _ := segmentServer(t, nil)

	resp := postFile(t, srv.URL+segmentJobsURL, "file", "movie.mp4", makeRealVideo(t, tools.FFmpeg, 2))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("上传应返回 202，实际 %d: %s", resp.StatusCode, body)
	}

	var submitted struct {
		JobID         string `json:"jobId"`
		State         string `json:"state"`
		QueuePosition int    `json:"queuePosition"`
		Error         string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(submitted.JobID) != 12 {
		t.Fatalf("作业 ID 应为 12 位，实际 %q", submitted.JobID)
	}
	switch submitted.State {
	case "queued", "running", "done", "failed":
	default:
		t.Fatalf("刚提交的作业状态应为 queued/running/done/failed，实际 %q", submitted.State)
	}
	if submitted.State == "failed" {
		t.Fatalf("真实视频不应在提交阶段就失败: %s", submitted.Error)
	}

	jobResp, err := http.Get(srv.URL + segmentJobsURL + "/" + submitted.JobID)
	if err != nil {
		t.Fatalf("查询作业失败: %v", err)
	}
	if jobResp.StatusCode != http.StatusOK {
		t.Fatalf("查询已提交的作业应返回 200，实际 %d", jobResp.StatusCode)
	}
	var view struct {
		JobID    string  `json:"jobId"`
		State    string  `json:"state"`
		Progress float64 `json:"progress"`
	}
	decodeBody(t, jobResp, &view)
	if view.JobID != submitted.JobID {
		t.Fatalf("查询结果里的 jobId 不一致: %q vs %q", view.JobID, submitted.JobID)
	}
	if view.Progress < 0 || view.Progress > 1 {
		t.Fatalf("进度必须在 0–1 之间，实际 %v", view.Progress)
	}
}

// TestSegmentUnparsableUpload422 覆盖 422：上传的不是 ffprobe 能解析的视频，
// 必须在提交阶段就明确报错，而不是排进队列再失败。
func TestSegmentUnparsableUpload422(t *testing.T) {
	requireSegmentFFmpeg(t)
	srv, _ := segmentServer(t, nil)

	resp := postFile(t, srv.URL+segmentJobsURL, "file", "movie.mp4", []byte("这不是视频"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("无法解析的上传应返回 422，实际 %d", resp.StatusCode)
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析错误体失败: %v", err)
	}
	if body.Code != codeSegmentProbeFailed {
		t.Fatalf("错误码应为 %s，实际 %q", codeSegmentProbeFailed, body.Code)
	}
}

// TestSegmentUnknownJob404 覆盖 404：状态、产物、分批三条路径都要能区分"作业不存在"。
func TestSegmentUnknownJob404(t *testing.T) {
	srv, _ := segmentServer(t, nil)

	cases := []struct{ name, path, code string }{
		{"状态", segmentJobsURL + "/NOSUCHJOB", codeSegmentJobNotFound},
		{"产物", segmentJobsURL + "/NOSUCHJOB/result", codeSegmentJobNotFound},
		{"分批", segmentJobsURL + "/NOSUCHJOB/parts/1", codeSegmentJobNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("请求失败: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("未知作业应返回 404，实际 %d", resp.StatusCode)
			}
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("解析错误体失败: %v", err)
			}
			if body.Code != tc.code || body.Error == "" {
				t.Fatalf("错误体应为 {error, code=%s}，实际 %+v", tc.code, body)
			}
		})
	}
}

// TestSegmentQueueFull429 覆盖 429 + Retry-After：队列长度为 0 时任何提交都进不来。
func TestSegmentQueueFull429(t *testing.T) {
	srv, _ := segmentServer(t, func(cfg *config.Config) {
		cfg.Segment.QueueLength = 0
	})

	resp := postFile(t, srv.URL+segmentJobsURL, "file", "movie.mp4", []byte("hello-video"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("队列满应返回 429，实际 %d", resp.StatusCode)
	}
	retryAfter := resp.Header.Get("Retry-After")
	seconds, err := strconv.Atoi(retryAfter)
	if err != nil || seconds < 1 {
		t.Fatalf("Retry-After 应为正整数秒，实际 %q", retryAfter)
	}

	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析错误体失败: %v", err)
	}
	if body.Code != codeSegmentQueueFull || body.Error == "" {
		t.Fatalf("错误体应为 {error, code=%s}，实际 %+v", codeSegmentQueueFull, body)
	}
}

// TestSegmentRateLimited429 覆盖令牌桶为空时的 429（速率 1/分钟、容量 1）。
func TestSegmentRateLimited429(t *testing.T) {
	tools := requireSegmentFFmpeg(t)
	srv, _ := segmentServer(t, func(cfg *config.Config) {
		cfg.Segment.RatePerMinute = 1
		cfg.Segment.Burst = 1
		cfg.Segment.QueueLength = 64
	})
	payload := makeRealVideo(t, tools.FFmpeg, 2)

	first := postFile(t, srv.URL+segmentJobsURL, "file", "movie.mp4", payload)
	first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("桶里有令牌时应返回 202，实际 %d", first.StatusCode)
	}

	second := postFile(t, srv.URL+segmentJobsURL, "file", "movie.mp4", payload)
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("令牌桶空时应返回 429，实际 %d", second.StatusCode)
	}
	if seconds, err := strconv.Atoi(second.Header.Get("Retry-After")); err != nil || seconds < 1 {
		t.Fatalf("Retry-After 应为正整数秒，实际 %q", second.Header.Get("Retry-After"))
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(second.Body).Decode(&body); err != nil {
		t.Fatalf("解析错误体失败: %v", err)
	}
	if body.Code != codeSegmentRateLimited {
		t.Fatalf("错误码应为 %s，实际 %q", codeSegmentRateLimited, body.Code)
	}
}

// TestSegmentSourceTooLarge413 覆盖 413：声明长度就超限（Content-Length 已知的路径）。
func TestSegmentSourceTooLarge413(t *testing.T) {
	srv, _ := segmentServer(t, func(cfg *config.Config) {
		cfg.Segment.MaxSourceBytes = 16
	})

	resp := postFile(t, srv.URL+segmentJobsURL, "file", "movie.mp4", bytes.Repeat([]byte("x"), 4096))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("超过大小上限应返回 413，实际 %d: %s", resp.StatusCode, body)
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析错误体失败: %v", err)
	}
	if body.Code != codeSegmentSourceTooBig {
		t.Fatalf("错误码应为 %s，实际 %q", codeSegmentSourceTooBig, body.Code)
	}
}

// TestSegmentEmptyFile422 覆盖 422：空文件不允许（不允许"提交一个空作业"）。
func TestSegmentEmptyFile422(t *testing.T) {
	srv, _ := segmentServer(t, nil)

	resp := postFile(t, srv.URL+segmentJobsURL, "file", "empty.mp4", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("空文件应返回 422，实际 %d", resp.StatusCode)
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析错误体失败: %v", err)
	}
	if body.Code != codeSegmentSourceEmpty {
		t.Fatalf("错误码应为 %s，实际 %q", codeSegmentSourceEmpty, body.Code)
	}
}

// TestSegmentDurationTooLong422 覆盖 422：时长超限必须在上传后立刻判出（ffprobe），
// 而不是等切完才发现。没有 ffprobe 就无法探测时长，直接跳过。
func TestSegmentDurationTooLong422(t *testing.T) {
	tools := requireSegmentFFmpeg(t)

	srv, _ := segmentServer(t, func(cfg *config.Config) {
		cfg.Segment.MaxDuration = time.Second // 任何真实视频都超限
	})

	payload := makeRealVideo(t, tools.FFmpeg, 3)
	resp := postFile(t, srv.URL+segmentJobsURL, "file", "movie.mp4", payload)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("时长超限应返回 422，实际 %d: %s", resp.StatusCode, body)
	}

	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析错误体失败: %v", err)
	}
	if body.Code != codeSegmentTooLong {
		t.Fatalf("错误码应为 %s，实际 %q", codeSegmentTooLong, body.Code)
	}
	if !strings.Contains(body.Error, "上限") {
		t.Fatalf("错误信息应说明上限，实际 %q", body.Error)
	}
}

// TestSegmentMissingFileField400 覆盖 multipart 里没有 file 字段的情况。
func TestSegmentMissingFileField400(t *testing.T) {
	srv, _ := segmentServer(t, nil)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("note", "没有文件")
	writer.Close()

	resp, err := http.Post(srv.URL+segmentJobsURL, writer.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺少 file 字段应返回 400，实际 %d", resp.StatusCode)
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("解析错误体失败: %v", err)
	}
	if payload.Code != "BAD_REQUEST" {
		t.Fatalf("错误码应为 BAD_REQUEST，实际 %q", payload.Code)
	}
}

// TestSegmentErrorMapping 直接锁住两个映射函数的语义
// （409 这类状态用真实作业很难稳定构造，但映射本身必须被覆盖）。
func TestSegmentErrorMapping(t *testing.T) {
	submitCases := []struct {
		err    error
		status int
		code   string
	}{
		{&segment.QuotaError{Err: segment.ErrQueueFull, RetryAfter: 5 * time.Second}, http.StatusTooManyRequests, codeSegmentQueueFull},
		{&segment.QuotaError{Err: segment.ErrRateLimited, RetryAfter: 20 * time.Second}, http.StatusTooManyRequests, codeSegmentRateLimited},
		{fmt.Errorf("%w（上限）", segment.ErrSourceTooLarge), http.StatusRequestEntityTooLarge, codeSegmentSourceTooBig},
		{segment.ErrSourceEmpty, http.StatusUnprocessableEntity, codeSegmentSourceEmpty},
		{fmt.Errorf("%w：视频 61 分钟", segment.ErrDurationTooLong), http.StatusUnprocessableEntity, codeSegmentTooLong},
		{segment.ErrProbeFailed, http.StatusUnprocessableEntity, codeSegmentProbeFailed},
		{errors.New("boom"), http.StatusInternalServerError, "INTERNAL"},
	}
	for _, tc := range submitCases {
		recorder, ctx := newTestContext()
		writeSegmentSubmitError(ctx, tc.err)
		assertErrorResponse(t, recorder, tc.status, tc.code)
	}

	artifactCases := []struct {
		err    error
		status int
		code   string
	}{
		{segment.ErrJobNotFound, http.StatusNotFound, codeSegmentJobNotFound},
		{segment.ErrNoParts, http.StatusNotFound, codeSegmentNoParts},
		{segment.ErrPartNotFound, http.StatusNotFound, codeSegmentPartNotFound},
		{fmt.Errorf("%w: 还在切", segment.ErrJobNotReady), http.StatusConflict, codeSegmentJobNotReady},
		{errors.New("boom"), http.StatusInternalServerError, "INTERNAL"},
	}
	for _, tc := range artifactCases {
		recorder, ctx := newTestContext()
		writeSegmentArtifactError(ctx, tc.err)
		assertErrorResponse(t, recorder, tc.status, tc.code)
	}
}

// TestSegmentResultDeliveryHTTP 是切片端点的真实端到端：
// 一次上传 → 单次返回 zip → 再用小上限验证 manifest 与分批下载（含 sha256 校验）。
func TestSegmentResultDeliveryHTTP(t *testing.T) {
	tools := requireSegmentFFmpeg(t)

	payload := makeRealVideo(t, tools.FFmpeg, 6)

	// 先离线跑一次流水线，好挑一个"必然分批"的上限（单份 < 1GB 的真实阈值不适合单测）。
	probeDir := t.TempDir()
	srcPath := filepath.Join(probeDir, "src.mp4")
	if err := os.WriteFile(srcPath, payload, 0o644); err != nil {
		t.Fatalf("写入临时视频失败: %v", err)
	}
	info, err := segment.Probe(context.Background(), tools, srcPath)
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	artifacts, err := segment.Process(context.Background(), srcPath, filepath.Join(probeDir, "out"), segment.ProcessOptions{
		Tools: tools, SegmentSeconds: 2, Auto: true, Info: info,
	})
	if err != nil {
		t.Fatalf("离线流水线失败: %v", err)
	}
	var maxFile int64
	for _, f := range artifacts.Files {
		if f.Size > maxFile {
			maxFile = f.Size
		}
	}
	// 上限取"最大产物 + 3 个 zip 成员开销 + 1"：这样无论产物是
	// index+init+每片一个文件（PR_SEGMENT_PACK_SIZE=1）还是 index+init+若干 pack-*.bin，
	// 都必然至少切成两份，且每个文件本身都装得下。
	partLimit := maxFile + 3*512 + 1

	// ---- 单次返回：GET /result 直接拿到 zip ----
	srv, _ := segmentServer(t, nil)
	jobID := submitAndWait(t, srv, payload)

	singleResp, err := http.Get(srv.URL + segmentJobsURL + "/" + jobID + "/result")
	if err != nil {
		t.Fatalf("下载产物失败: %v", err)
	}
	if singleResp.StatusCode != http.StatusOK {
		t.Fatalf("已完成的作业应返回 200，实际 %d", singleResp.StatusCode)
	}
	if ct := singleResp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("单次返回应是 zip，实际 Content-Type=%q", ct)
	}
	zipBytes, err := io.ReadAll(singleResp.Body)
	singleResp.Body.Close()
	if err != nil {
		t.Fatalf("读取 zip 失败: %v", err)
	}
	entries := zipEntries(t, zipBytes)
	for _, want := range []string{"index.json", "init.mp4"} {
		if _, ok := entries[want]; !ok {
			t.Fatalf("zip 里缺少 %s，实际 %v", want, mapKeys(entries))
		}
	}
	var index model.Index
	if err := json.Unmarshal(entries["index.json"], &index); err != nil {
		t.Fatalf("zip 里的 index.json 无法解析: %v", err)
	}
	if index.Version <= 0 || index.MimeType == "" || index.TotalDuration <= 0 {
		t.Fatalf("zip 里的 index.json 不是合法索引: %+v", index)
	}
	if err := index.Validate(); err != nil {
		t.Fatalf("zip 里的 index.json 不自洽: %v", err)
	}
	// 分片内容的文件形态由打包决定：默认每 100 片一个 pack-*.bin，
	// PR_SEGMENT_PACK_SIZE=1 才是逐片一个 c*.m4s。zip 里必须有与之对应的文件。
	if index.Packed() {
		for _, pack := range index.Packs {
			if _, ok := entries[pack.File]; !ok {
				t.Fatalf("zip 里缺少分片包 %s，实际 %v", pack.File, mapKeys(entries))
			}
		}
	} else if _, ok := entries["c00001.m4s"]; !ok {
		t.Fatalf("未打包时 zip 里应有 c00001.m4s，实际 %v", mapKeys(entries))
	}

	// ---- 分批：小上限 → GET /result 返回 manifest，逐份下载并校验 sha256 ----
	srv2, _ := segmentServer(t, func(cfg *config.Config) {
		cfg.Segment.SingleResponseMaxBytes = partLimit
	})
	jobID2 := submitAndWait(t, srv2, payload)

	manifestResp, err := http.Get(srv2.URL + segmentJobsURL + "/" + jobID2 + "/result")
	if err != nil {
		t.Fatalf("请求 manifest 失败: %v", err)
	}
	if ct := manifestResp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("超过上限时应返回 JSON manifest，实际 Content-Type=%q", ct)
	}
	var manifest struct {
		JobID          string `json:"jobId"`
		Bytes          int64  `json:"bytes"`
		Segments       int    `json:"segments"`
		SingleResponse bool   `json:"singleResponse"`
		Parts          []struct {
			N      int    `json:"n"`
			Bytes  int64  `json:"bytes"`
			SHA256 string `json:"sha256"`
			URL    string `json:"url"`
		} `json:"parts"`
	}
	decodeBody(t, manifestResp, &manifest)
	if manifest.SingleResponse {
		t.Fatalf("超过上限时 singleResponse 应为 false: %+v", manifest)
	}
	if len(manifest.Parts) < 2 {
		t.Fatalf("应至少切成 2 份，实际 %d", len(manifest.Parts))
	}
	if manifest.JobID != jobID2 {
		t.Fatalf("manifest 的 jobId 应为 %s，实际 %s", jobID2, manifest.JobID)
	}

	var totalEntries int
	for i, part := range manifest.Parts {
		if part.N != i+1 || part.URL == "" || len(part.SHA256) != 64 {
			t.Fatalf("第 %d 份的 manifest 字段不完整: %+v", i+1, part)
		}
		if part.Bytes >= partLimit {
			t.Fatalf("第 %d 份 %d 字节不应达到单份上限 %d", part.N, part.Bytes, partLimit)
		}

		partResp, err := http.Get(srv2.URL + part.URL)
		if err != nil {
			t.Fatalf("下载第 %d 份失败: %v", part.N, err)
		}
		if partResp.StatusCode != http.StatusOK {
			t.Fatalf("下载第 %d 份应返回 200，实际 %d", part.N, partResp.StatusCode)
		}
		if got := partResp.Header.Get("Content-Length"); got != strconv.FormatInt(part.Bytes, 10) {
			t.Fatalf("第 %d 份的 Content-Length 应为 %d，实际 %q", part.N, part.Bytes, got)
		}
		if got := partResp.Header.Get("X-Segment-Sha256"); got != part.SHA256 {
			t.Fatalf("第 %d 份的 X-Segment-Sha256 与 manifest 不一致", part.N)
		}
		partBytes, err := io.ReadAll(partResp.Body)
		partResp.Body.Close()
		if err != nil {
			t.Fatalf("读取第 %d 份失败: %v", part.N, err)
		}
		if int64(len(partBytes)) != part.Bytes {
			t.Fatalf("第 %d 份实际 %d 字节与 manifest 的 %d 不一致", part.N, len(partBytes), part.Bytes)
		}
		sum := sha256.Sum256(partBytes)
		if hex.EncodeToString(sum[:]) != part.SHA256 {
			t.Fatalf("第 %d 份的 sha256 校验失败", part.N)
		}
		for name := range zipEntries(t, partBytes) {
			if name == "" {
				continue
			}
			totalEntries++
		}
	}

	// 越界的分批编号必须 404，而不是给出半份数据。
	missing, err := http.Get(srv2.URL + segmentJobsURL + "/" + jobID2 + "/parts/999")
	if err != nil {
		t.Fatalf("请求越界分批失败: %v", err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("越界分批应返回 404，实际 %d", missing.StatusCode)
	}

	// 分批作业不能走单次返回。
	noParts, err := http.Get(srv2.URL + segmentJobsURL + "/" + jobID2)
	if err != nil {
		t.Fatalf("查询作业失败: %v", err)
	}
	var view struct {
		Result *struct {
			Parts []struct {
				URL string `json:"url"`
			} `json:"parts"`
		} `json:"result"`
	}
	decodeBody(t, noParts, &view)
	if view.Result == nil || len(view.Result.Parts) == 0 || view.Result.Parts[0].URL == "" {
		t.Fatalf("作业状态里的 result 应带上分批下载地址: %+v", view.Result)
	}
}

// ---------- 小工具 ----------

// requireSegmentFFmpeg 定位 ffmpeg；找不到就跳过（这些用例必须真的跑一次 ffprobe）。
func requireSegmentFFmpeg(t *testing.T) segment.Tools {
	t.Helper()

	tools := segment.DiscoverTools("")
	if !tools.Available() {
		t.Skip("未找到 ffmpeg/ffprobe，跳过需要真实探测上传文件的用例")
	}
	return tools
}

// submitAndWait 上传并轮询到作业结束，返回 jobId。
func submitAndWait(t *testing.T, srv *httptest.Server, payload []byte) string {
	t.Helper()

	resp := postFile(t, srv.URL+segmentJobsURL, "file", "movie.mp4", payload)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("上传应返回 202，实际 %d: %s", resp.StatusCode, body)
	}
	var submitted struct {
		JobID string `json:"jobId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		jobResp, err := http.Get(srv.URL + segmentJobsURL + "/" + submitted.JobID)
		if err != nil {
			t.Fatalf("查询作业失败: %v", err)
		}
		var view struct {
			State string `json:"state"`
			Error string `json:"error"`
		}
		err = json.NewDecoder(jobResp.Body).Decode(&view)
		jobResp.Body.Close()
		if err != nil {
			t.Fatalf("解析作业状态失败: %v", err)
		}
		switch view.State {
		case "done":
			return submitted.JobID
		case "failed":
			t.Fatalf("作业失败: %s", view.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待作业 %s 结束超时", submitted.JobID)
	return ""
}

// makeRealVideo 用 ffmpeg 生成一段真实视频（返回文件字节）。
func makeRealVideo(t *testing.T, ffmpeg string, seconds int) []byte {
	t.Helper()

	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	cmd := exec.Command(ffmpeg,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=160x120:rate=10:duration=%d", seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%d", seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "64k",
		"-shortest",
		src,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg 生成测试视频失败: %v\n%s", err, output)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读取测试视频失败: %v", err)
	}
	return data
}

func zipEntries(t *testing.T, data []byte) map[string][]byte {
	t.Helper()

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip 无法打开: %v", err)
	}
	out := make(map[string][]byte, len(reader.File))
	for _, entry := range reader.File {
		rc, err := entry.Open()
		if err != nil {
			t.Fatalf("打开 zip 条目 %s 失败: %v", entry.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("读取 zip 条目 %s 失败: %v", entry.Name, err)
		}
		out[entry.Name] = content
	}
	return out
}

func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func newTestContext() (*httptest.ResponseRecorder, *gin.Context) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/segment/jobs/X", nil)
	return recorder, ctx
}

func assertErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()

	if recorder.Code != wantStatus {
		t.Fatalf("状态码应为 %d，实际 %d（body=%s）", wantStatus, recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是合法 JSON: %v", err)
	}
	if body.Code != wantCode {
		t.Fatalf("错误码应为 %q，实际 %q", wantCode, body.Code)
	}
	if body.Error == "" {
		t.Fatal("错误体必须带可读的 error 文案")
	}
}
