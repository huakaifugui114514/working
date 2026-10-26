package knowledge

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxChunkRunes 是单个 chunk 的最大 rune 数。
	MaxChunkRunes = 1000

	// ChunkOverlapRunes 是相邻 chunk 的重叠 rune 数。
	// 重叠是为了避免一句话/一个概念正好被切在边界上，检索时两边都拿不到完整语义。
	ChunkOverlapRunes = 100
)

// ChunkContent 把完整正文切成 ContentChunk 列表。
//
// 契约：
//   - 输入先统一换行：\r\n 和 \r → \n
//   - 按 rune 切，不按 byte；中文、emoji 不能被切坏
//   - 单块不超过 MaxChunkRunes 个 rune
//   - 相邻块保留约 ChunkOverlapRunes 个 rune 的重叠
//   - 优先在换行、句号、问号、感叹号、分号、空格处断开；找不到自然断点才硬切
//   - 每块去掉首尾空白；去掉后为空的块跳过
//   - Index 必须连续：0..N-1
//   - CharCount 是存储后 Content 的 rune 数
//   - 空正文/纯空白 → ErrContentEmpty
//   - 块数超过 MaxDocumentChunks → ErrTooManyChunks
//
// 实现约定：断点只在 [start+ChunkOverlapRunes, start+MaxChunkRunes) 内从后往前找。
// 重叠区里的断点不采用，否则会切出几乎全是上一块重复内容的短块。
func ChunkContent(content string) ([]ContentChunk, error) {
	// 必须先处理 \r\n，再处理单独的 \r：
	// 如果只做 \r → \n，CRLF 会变成两个 \n，正文会多出空行。
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	// 先判断空正文，避免为纯空白分配 rune 切片。
	if strings.TrimSpace(content) == "" {
		return nil, ErrContentEmpty
	}

	runes := []rune(content)

	// 每块至少前进 MaxChunkRunes-ChunkOverlapRunes 个 rune；
	// 用这个步长估算容量，避免长正文反复扩容。
	chunks := make([]ContentChunk, 0, len(runes)/(MaxChunkRunes-ChunkOverlapRunes)+1)

	for start := 0; start < len(runes); {
		end := start + MaxChunkRunes
		if end > len(runes) {
			end = len(runes)
		}

		// 最后一块直接取到结尾：此时再为了断句而缩短，
		// 只会留下一个更小的尾巴，没有收益。
		if end < len(runes) {
			end = findChunkBreak(runes, start, end)
		}

		text := strings.TrimSpace(string(runes[start:end]))
		if text != "" {
			if len(chunks) >= MaxDocumentChunks {
				return nil, ErrTooManyChunks
			}
			chunks = append(chunks, ContentChunk{
				Index:     len(chunks),
				Content:   text,
				CharCount: utf8.RuneCountInString(text),
			})
		}

		if end == len(runes) {
			break
		}

		// 下一块回退 overlap 个 rune，让边界附近的语义在两边都能被检索到。
		// 如果块本身不比 overlap 长，回退会原地踏步；这时直接前进到 end，
		// 保证循环一定收敛（正常参数下走不到这个分支）。
		next := end - ChunkOverlapRunes
		if next <= start {
			next = end
		}
		start = next
	}

	return chunks, nil
}

// findChunkBreak 在 runes[start:end] 中从后往前找自然断点，
// 返回断点之后的下标（即新块的 end）。找不到时返回 end，按上限硬切。
//
// 搜索下界是 start+ChunkOverlapRunes：落在下一块重叠区里的断点不采用，
// 否则会切出“几乎全是上一块重复内容”的短块。
func findChunkBreak(runes []rune, start, end int) int {
	minEnd := start + ChunkOverlapRunes
	if minEnd >= end {
		return end
	}

	for i := end - 1; i >= minEnd; i-- {
		if isChunkBreak(runes, i) {
			return i + 1
		}
	}
	return end
}

// isChunkBreak 判断 runes[i] 是否适合作为块边界。
//
// 换行、空格、制表符等所有 Unicode 空白都算自然断点；
// 中文标点后面通常没有空格，所以直接断在标点之后。
// ASCII 的 . ? ! ; 只有后面是空白/结尾时才算句子边界：
// 否则 3.14、e.g.、URL 里的 ? 和 ; 会被误切。
func isChunkBreak(runes []rune, i int) bool {
	r := runes[i]
	if unicode.IsSpace(r) {
		return true
	}

	switch r {
	case '。', '？', '！', '；':
		return true
	case '.', '?', '!', ';':
		return i+1 == len(runes) || unicode.IsSpace(runes[i+1])
	default:
		return false
	}
}
