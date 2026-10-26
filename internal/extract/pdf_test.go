package extract

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func minimalPDF(text string) []byte {
	escaped := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(text)
	content := fmt.Sprintf("BT /F1 18 Tf 72 700 Td (%s) Tj ET", escaped)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

func TestPDFText(t *testing.T) {
	data := minimalPDF("KnowFlow PDF test")
	text, err := PDFText(data)
	if err != nil {
		t.Fatalf("PDFText: %v", err)
	}
	if !strings.Contains(text, "KnowFlow PDF test") {
		t.Fatalf("text = %q", text)
	}
}

func TestPDFTextErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{name: "empty", data: nil, want: ErrInvalidPDF},
		{name: "not pdf", data: []byte("hello"), want: ErrInvalidPDF},
		{name: "too large", data: make([]byte, MaxPDFBytes+1), want: ErrTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := PDFText(tt.data); err == nil || !strings.Contains(err.Error(), tt.want.Error()) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
