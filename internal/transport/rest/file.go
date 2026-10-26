package rest

import (
	"errors"
	"io"
	"net/http"

	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/extract"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
)

const (
	// maxDocumentFileBytes 是上传文件上限；PDF 可能比最终文本大很多。
	maxDocumentFileBytes = extract.MaxPDFBytes
	// multipartOverhead 是 multipart boundary/header 的余量。
	multipartOverhead = 1 << 20
)

// handleUploadDocumentFile 处理 POST /api/v1/documents/{id}/file。
//
// 文件名/扩展名不可信，最终以字节开头的 %PDF- 魔数为准。
func (s *Server) handleUploadDocumentFile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDocumentFileBytes+multipartOverhead)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		respond.Error(w, s.logger, http.StatusBadRequest, apperr.CodeInvalidArgument, "上传内容不是合法的 multipart/form-data 或文件过大")
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		respond.Error(w, s.logger, http.StatusBadRequest, apperr.CodeInvalidArgument, "缺少 file 字段")
		return
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxDocumentFileBytes+1))
	if err != nil {
		respond.Error(w, s.logger, http.StatusBadRequest, apperr.CodeInvalidArgument, "读取上传文件失败")
		return
	}
	if len(data) > maxDocumentFileBytes {
		respond.Error(w, s.logger, http.StatusBadRequest, apperr.CodeInvalidArgument, "PDF 不能超过 20 MiB")
		return
	}

	text, err := extract.PDFText(data)
	if err != nil {
		switch {
		case errors.Is(err, extract.ErrTooLarge):
			respond.Error(w, s.logger, http.StatusBadRequest, apperr.CodeInvalidArgument, "PDF 不能超过 20 MiB")
		case errors.Is(err, extract.ErrNoText):
			respond.Error(w, s.logger, http.StatusBadRequest, apperr.CodeInvalidArgument, "PDF 没有可提取文本")
		default:
			respond.Error(w, s.logger, http.StatusBadRequest, apperr.CodeInvalidArgument, "PDF 解析失败")
		}
		return
	}

	if err := s.contents.ReplaceDocumentContent(r.Context(), id, text); err != nil {
		s.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
