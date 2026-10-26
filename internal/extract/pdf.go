// Package extract 从上传文件中抽取纯文本。
//
// 当前只支持 PDF；解析失败/空文本都要给调用方明确错误，不能把二进制当正文入库。
package extract

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

const (
	// MaxPDFBytes 是单个 PDF 的大小上限；和正文 1 MiB 上限分开，
	// 因为 PDF 的压缩/字体数据可能远大于最终文本。
	MaxPDFBytes = 20 << 20

	// MaxExtractedBytes 防止恶意 PDF 解压出超大文本。
	MaxExtractedBytes = 4 << 20
)

var (
	// ErrTooLarge 表示文件超过 MaxPDFBytes。
	ErrTooLarge = errors.New("extract: file too large")
	// ErrInvalidPDF 表示文件不是可解析的 PDF。
	ErrInvalidPDF = errors.New("extract: invalid pdf")
	// ErrNoText 表示 PDF 可解析但没有可提取文本。
	ErrNoText = errors.New("extract: no text")
)

// PDFText 从 PDF 字节中提取纯文本。
func PDFText(data []byte) (string, error) {
	if len(data) == 0 {
		return "", ErrInvalidPDF
	}
	if len(data) > MaxPDFBytes {
		return "", ErrTooLarge
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return "", ErrInvalidPDF
	}
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPDF, err)
	}
	plain, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPDF, err)
	}
	raw, err := io.ReadAll(io.LimitReader(plain, MaxExtractedBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: read text: %w", ErrInvalidPDF, err)
	}
	if len(raw) > MaxExtractedBytes {
		return "", ErrTooLarge
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", ErrNoText
	}
	return text, nil
}
