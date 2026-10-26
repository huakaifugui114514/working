package knowledge

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkContentBasics(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
		wantErr error
	}{
		{
			name:    "空字符串",
			content: "",
			wantErr: ErrContentEmpty,
		},
		{
			name:    "纯空白",
			content: " \t\r\n\u3000",
			wantErr: ErrContentEmpty,
		},
		{
			name:    "短文本去掉首尾空白",
			content: "  hello  ",
			want:    []string{"hello"},
		},
		{
			name:    "CRLF 和 CR 都归一为一个 LF",
			content: "第一行\r\n第二行\r第三行",
			want:    []string{"第一行\n第二行\n第三行"},
		},
		{
			name:    "emoji 按 rune 计数",
			content: "😀😀",
			want:    []string{"😀😀"},
		},
		{
			name:    "恰好 1000 rune 不额外切块",
			content: strings.Repeat("a", MaxChunkRunes),
			want:    []string{strings.Repeat("a", MaxChunkRunes)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ChunkContent(tt.content)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ChunkContent() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}

			assertChunkInvariants(t, got)
			if len(got) != len(tt.want) {
				t.Fatalf("块数 = %d, want %d", len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i].Content != tt.want[i] {
					t.Fatalf("第 %d 块 = %q, want %q", i, got[i].Content, tt.want[i])
				}
			}
		})
	}
}

func TestChunkContentNoBreak(t *testing.T) {
	content := patternedRunes(2500)
	chunks, err := ChunkContent(content)
	if err != nil {
		t.Fatalf("ChunkContent() error = %v", err)
	}

	assertChunkInvariants(t, chunks)
	wantLens := []int{1000, 1000, 700}
	if len(chunks) != len(wantLens) {
		t.Fatalf("块数 = %d, want %d", len(chunks), len(wantLens))
	}
	for i, want := range wantLens {
		if chunks[i].CharCount != want {
			t.Fatalf("第 %d 块 CharCount = %d, want %d", i, chunks[i].CharCount, want)
		}
	}

	// 没有自然断点时按 1000 硬切，相邻块精确重叠 100 个 rune。
	for i := 0; i+1 < len(chunks); i++ {
		want := tailRunes(chunks[i].Content, ChunkOverlapRunes)
		got := headRunes(chunks[i+1].Content, ChunkOverlapRunes)
		if got != want {
			t.Fatalf("第 %d/%d 块重叠不一致:\n tail = %q\n head = %q", i, i+1, want, got)
		}
	}
}

func TestChunkContentNaturalBreak(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantFirst string
	}{
		{
			name:      "中文句号",
			content:   strings.Repeat("甲", 500) + "。" + strings.Repeat("乙", 1200),
			wantFirst: strings.Repeat("甲", 500) + "。",
		},
		{
			name:      "换行",
			content:   strings.Repeat("a", 500) + "\n" + strings.Repeat("b", 1000),
			wantFirst: strings.Repeat("a", 500),
		},
		{
			name:      "空格",
			content:   strings.Repeat("a", 500) + " " + strings.Repeat("b", 1000),
			wantFirst: strings.Repeat("a", 500),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, err := ChunkContent(tt.content)
			if err != nil {
				t.Fatalf("ChunkContent() error = %v", err)
			}
			assertChunkInvariants(t, chunks)
			if len(chunks) == 0 {
				t.Fatal("没有产生任何块")
			}
			if chunks[0].Content != tt.wantFirst {
				t.Fatalf("第一块 = %q, want %q", chunks[0].Content, tt.wantFirst)
			}
		})
	}
}

func TestChunkContentUTF8AndOverlap(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantLens []int
	}{
		{
			name:     "中文",
			content:  strings.Repeat("汉", 2500),
			wantLens: []int{1000, 1000, 700},
		},
		{
			name:     "emoji",
			content:  strings.Repeat("😀", 1200),
			wantLens: []int{1000, 300},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, err := ChunkContent(tt.content)
			if err != nil {
				t.Fatalf("ChunkContent() error = %v", err)
			}
			assertChunkInvariants(t, chunks)

			if len(chunks) != len(tt.wantLens) {
				t.Fatalf("块数 = %d, want %d", len(chunks), len(tt.wantLens))
			}
			for i, want := range tt.wantLens {
				if chunks[i].CharCount != want {
					t.Fatalf("第 %d 块 CharCount = %d, want %d", i, chunks[i].CharCount, want)
				}
			}

			for i := 0; i+1 < len(chunks); i++ {
				want := tailRunes(chunks[i].Content, ChunkOverlapRunes)
				got := headRunes(chunks[i+1].Content, ChunkOverlapRunes)
				if got != want {
					t.Fatalf("第 %d/%d 块重叠不一致", i, i+1)
				}
			}
		})
	}
}

func TestChunkContentDoesNotSplitDecimal(t *testing.T) {
	// 小数点不是句号；如果按 ASCII '.' 硬断，第一块会停在 "3."，
	// 第二块从 ".14" 开始，检索“3.14”时语义就被切碎了。
	content := strings.Repeat("a", 990) + "3.14" + strings.Repeat("b", 1000)
	chunks, err := ChunkContent(content)
	if err != nil {
		t.Fatalf("ChunkContent() error = %v", err)
	}
	assertChunkInvariants(t, chunks)
	if !strings.Contains(chunks[0].Content, "3.14") {
		t.Fatalf("第一块没有保住 3.14: %q", tailRunes(chunks[0].Content, 20))
	}
}

func TestChunkContentSkipsBlankWindows(t *testing.T) {
	content := "a" + strings.Repeat(" ", 2500) + "b"
	chunks, err := ChunkContent(content)
	if err != nil {
		t.Fatalf("ChunkContent() error = %v", err)
	}

	assertChunkInvariants(t, chunks)
	if len(chunks) != 2 {
		t.Fatalf("块数 = %d, want 2", len(chunks))
	}
	if chunks[0].Content != "a" || chunks[1].Content != "b" {
		t.Fatalf("块内容 = [%q, %q], want [a, b]", chunks[0].Content, chunks[1].Content)
	}
}

func TestChunkContentTooManyChunks(t *testing.T) {
	// 2M 个 rune 会超过 2000 块；这里直接测 chunker 的上限，
	// 不经过 service 的 1 MiB 校验。
	_, err := ChunkContent(strings.Repeat("a", 2_000_000))
	if !errors.Is(err, ErrTooManyChunks) {
		t.Fatalf("ChunkContent() error = %v, want %v", err, ErrTooManyChunks)
	}
}

func assertChunkInvariants(t *testing.T, chunks []ContentChunk) {
	t.Helper()

	for i, chunk := range chunks {
		if chunk.Index != i {
			t.Fatalf("第 %d 块 Index = %d, want %d", i, chunk.Index, i)
		}
		if chunk.Content == "" {
			t.Fatalf("第 %d 块为空", i)
		}
		if chunk.Content != strings.TrimSpace(chunk.Content) {
			t.Fatalf("第 %d 块首尾未去空白: %q", i, chunk.Content)
		}
		if got := utf8.RuneCountInString(chunk.Content); chunk.CharCount != got {
			t.Fatalf("第 %d 块 CharCount = %d, want %d", i, chunk.CharCount, got)
		}
		if chunk.CharCount > MaxChunkRunes {
			t.Fatalf("第 %d 块超过上限: %d > %d", i, chunk.CharCount, MaxChunkRunes)
		}
		if !utf8.ValidString(chunk.Content) {
			t.Fatalf("第 %d 块不是合法 UTF-8", i)
		}
	}
}

func patternedRunes(n int) string {
	var b strings.Builder
	b.Grow(n)
	for i := 0; i < n; i++ {
		b.WriteRune(rune('a' + i%26))
	}
	return b.String()
}

func headRunes(s string, n int) string {
	r := []rune(s)
	if n > len(r) {
		n = len(r)
	}
	return string(r[:n])
}

func tailRunes(s string, n int) string {
	r := []rune(s)
	if n > len(r) {
		n = len(r)
	}
	return string(r[len(r)-n:])
}
