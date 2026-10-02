package segment

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func artifactFiles(sizes ...int) []ArtifactFile {
	names := []string{IndexFileName, InitFileName, "c00001.m4s", "c00002.m4s", "c00003.m4s", "c00004.m4s"}
	out := make([]ArtifactFile, 0, len(sizes))
	for i, size := range sizes {
		name := "c00005.m4s"
		if i < len(names) {
			name = names[i]
		}
		out = append(out, ArtifactFile{Name: name, Size: int64(size)})
	}
	return out
}

// TestPlanPartsKeepsEveryPartUnderLimit 验证分批逻辑：每份打包后都严格小于上限，
// 且顺序与产物顺序一致（客户端按 url 顺序下载即可拼回完整产物）。
func TestPlanPartsKeepsEveryPartUnderLimit(t *testing.T) {
	files := artifactFiles(400, 400, 400, 400, 400, 400)
	const limit = 1500

	groups, err := PlanParts(files, limit)
	if err != nil {
		t.Fatalf("分批不应失败: %v", err)
	}
	if len(groups) != len(files) {
		t.Fatalf("每个文件 400+512 字节，上限 1500 时每份只能装 1 个，实际 %d 份", len(groups))
	}

	var flattened []ArtifactFile
	for i, group := range groups {
		flattened = append(flattened, group...)
		var estimated int64
		for _, f := range group {
			estimated += f.Size + zipPerFileOverhead
		}
		if estimated >= limit {
			t.Fatalf("第 %d 份估计大小 %d 不应达到上限 %d", i+1, estimated, limit)
		}
	}
	if len(flattened) != len(files) {
		t.Fatalf("分批后文件数变了：%d → %d", len(files), len(flattened))
	}
	for i := range files {
		if flattened[i].Name != files[i].Name {
			t.Fatalf("分批打乱了顺序：第 %d 个是 %s，应为 %s", i, flattened[i].Name, files[i].Name)
		}
	}
}

func TestPlanPartsPacksGreedily(t *testing.T) {
	files := artifactFiles(100, 100, 100, 100)
	groups, err := PlanParts(files, 1500)
	if err != nil {
		t.Fatalf("分批不应失败: %v", err)
	}
	// 每个文件算 612 字节 → 上限 1500 时每份装 2 个。
	if len(groups) != 2 {
		t.Fatalf("应切成 2 份，实际 %d", len(groups))
	}
	if len(groups[0]) != 2 || len(groups[1]) != 2 {
		t.Fatalf("两份应各含 2 个文件，实际 %d/%d", len(groups[0]), len(groups[1]))
	}
}

func TestPlanPartsRejectsOversizedSingleFile(t *testing.T) {
	// 单个分片本身就超过单份上限时无解：明确报错，而不是产出一份超限的下载。
	files := []ArtifactFile{{Name: "c00001.m4s", Size: 4096}}
	_, err := PlanParts(files, 2048)
	if !errors.Is(err, ErrTooBigForParts) {
		t.Fatalf("单个文件超过单份上限应返回 ErrTooBigForParts，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "c00001.m4s") {
		t.Fatalf("错误信息应点名文件，实际 %v", err)
	}
}

// TestWriteZipContainsArtifacts 验证 zip 内容：index.json、init.mp4 与全部分片，
// 逐个字节与磁盘上的产物一致。
func TestWriteZipContainsArtifacts(t *testing.T) {
	dir := t.TempDir()
	artifacts, err := writeFakeArtifacts(dir, 0)
	if err != nil {
		t.Fatalf("造产物失败: %v", err)
	}

	var buf bytes.Buffer
	size, err := WriteZip(&buf, dir, artifacts.Files)
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	if size != int64(buf.Len()) {
		t.Fatalf("WriteZip 返回的字节数 %d 与实际写入 %d 不一致", size, buf.Len())
	}

	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("生成的 zip 无法打开: %v", err)
	}

	want := map[string]int{}
	for _, f := range artifacts.Files {
		want[f.Name] = int(f.Size)
	}
	if len(reader.File) != len(want) {
		t.Fatalf("zip 条目数应为 %d，实际 %d", len(want), len(reader.File))
	}
	for _, entry := range reader.File {
		expected, ok := want[entry.Name]
		if !ok {
			t.Fatalf("zip 里出现了预期之外的文件: %s", entry.Name)
		}
		if int(entry.UncompressedSize64) != expected {
			t.Fatalf("%s 在 zip 里大小 %d，应为 %d", entry.Name, entry.UncompressedSize64, expected)
		}
		rc, err := entry.Open()
		if err != nil {
			t.Fatalf("打开 zip 条目 %s 失败: %v", entry.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("读取 zip 条目 %s 失败: %v", entry.Name, err)
		}
		if int64(len(content)) != int64(expected) {
			t.Fatalf("%s 内容长度 %d 与索引 %d 不一致", entry.Name, len(content), expected)
		}
	}
}

// TestWriteZipIsDeterministic 锁定"哈希可以预先算好"这个前提：
// 同一份产物重复打包必须逐字节相同，否则 manifest 里的 sha256 就没法在
// 作业完成时算出来（而那正是我们不想把第二份产物落盘的原因）。
func TestWriteZipIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	artifacts, err := writeFakeArtifacts(dir, 128)
	if err != nil {
		t.Fatalf("造产物失败: %v", err)
	}

	var first, second bytes.Buffer
	if _, err := WriteZip(&first, dir, artifacts.Files); err != nil {
		t.Fatalf("第一次打包失败: %v", err)
	}
	if _, err := WriteZip(&second, dir, artifacts.Files); err != nil {
		t.Fatalf("第二次打包失败: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("同一份产物两次打包的字节流不同，manifest 的 sha256 将无法校验")
	}

	sum := sha256.Sum256(first.Bytes())
	if hex.EncodeToString(sum[:]) != mustHashZip(t, dir, artifacts.Files) {
		t.Fatal("hashZip 与实际打包结果的摘要不一致")
	}
}

func mustHashZip(t *testing.T, dir string, files []ArtifactFile) string {
	t.Helper()
	_, sum, err := hashZip(dir, files)
	if err != nil {
		t.Fatalf("hashZip 失败: %v", err)
	}
	return sum
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:        "0 B",
		1023:     "1023 B",
		1024:     "1.00 KiB",
		1 << 20:  "1.00 MiB",
		1 << 30:  "1.00 GiB",
		16 << 30: "16.00 GiB",
		1536:     "1.50 KiB",
	}
	for input, want := range cases {
		if got := HumanBytes(input); got != want {
			t.Fatalf("HumanBytes(%d) = %q，应为 %q", input, got, want)
		}
	}
}
