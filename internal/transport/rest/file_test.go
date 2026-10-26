package rest

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
)

func testPDF(text string) []byte {
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

func multipartPDF(t *testing.T, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "test.pdf")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return &body, writer.FormDataContentType()
}

func TestUploadDocumentFile(t *testing.T) {
	id := uuid.New()
	var gotContent string
	contents := &fakeContentService{replace: func(_ context.Context, documentID uuid.UUID, content string) error {
		if documentID != id {
			t.Fatalf("documentID = %v", documentID)
		}
		gotContent = content
		return nil
	}}
	s := newTestServerWithAll(t, nil, &fakeCollectionService{}, &fakeDocumentService{}, contents,
		&fakeSearchService{}, &fakeAgentService{})
	body, contentType := multipartPDF(t, testPDF("KnowFlow PDF upload"))
	req := httptest.NewRequest(http.MethodPost, documentsPath+"/"+id.String()+"/file", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotContent, "KnowFlow PDF upload") {
		t.Fatalf("content = %q", gotContent)
	}
}

func TestUploadDocumentFileErrors(t *testing.T) {
	id := uuid.New()
	tests := []struct {
		name    string
		path    string
		body    []byte
		content string
		status  int
		code    string
	}{
		{name: "invalid document id", path: documentsPath + "/not-uuid/file", status: http.StatusBadRequest, code: apperr.CodeInvalidArgument},
		{name: "not pdf", path: documentsPath + "/" + id.String() + "/file", body: []byte("hello"), status: http.StatusBadRequest, code: apperr.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServerWithAll(t, nil, &fakeCollectionService{}, &fakeDocumentService{},
				&fakeContentService{}, &fakeSearchService{}, &fakeAgentService{})
			body, contentType := multipartPDF(t, tt.body)
			req := httptest.NewRequest(http.MethodPost, tt.path, body)
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			assertHTTPError(t, rec, tt.status, tt.code)
		})
	}
}

func TestUploadDocumentFileMissingField(t *testing.T) {
	s := newTestServerWithAll(t, nil, &fakeCollectionService{}, &fakeDocumentService{},
		&fakeContentService{}, &fakeSearchService{}, &fakeAgentService{})
	req := httptest.NewRequest(http.MethodPost, documentsPath+"/"+uuid.NewString()+"/file", strings.NewReader("x"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
}
