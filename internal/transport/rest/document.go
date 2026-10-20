package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
)

const documentsPath = "/api/v1/documents"

// DocumentService 是 HTTP 层依赖的文档业务接口。
// *knowledge.DocumentService 实现了它；测试可以注入 fake。
type DocumentService interface {
	CreateDocument(ctx context.Context, title string, sourceURI, mimeType *string, sizeBytes *int64) (*knowledge.Document, error)
	GetDocument(ctx context.Context, id uuid.UUID) (*knowledge.Document, error)
	UpdateDocument(ctx context.Context, id uuid.UUID, patch knowledge.DocumentUpdate) (*knowledge.Document, error)
	SoftDeleteDocument(ctx context.Context, id uuid.UUID) error
	RestoreDocument(ctx context.Context, id uuid.UUID) (*knowledge.Document, error)
	AddDocumentToCollection(ctx context.Context, collectionID, documentID uuid.UUID) (*knowledge.CollectionDocument, error)
	RemoveDocumentFromCollection(ctx context.Context, collectionID, documentID uuid.UUID) error
	ListDocumentsByCollection(ctx context.Context, collectionID uuid.UUID, cursor *knowledge.Cursor, limit int) ([]*knowledge.CollectionDocument, *knowledge.Cursor, error)
}

// documentResponse 是文档的对外表示。
// 不直接复用领域模型：避免把 DeletedAt 之类的内部字段带出去。
type documentResponse struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	SourceURI *string   `json:"source_uri"`
	MimeType  *string   `json:"mime_type"`
	SizeBytes *int64    `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// collectionDocumentResponse 把关联的 added_at 与 document 嵌套返回。
// added_at 属于 collection_documents，不属于 document，所以不能平铺在 document 里。
type collectionDocumentResponse struct {
	Document documentResponse `json:"document"`
	AddedAt  time.Time        `json:"added_at"`
}

type collectionDocumentListResponse struct {
	Items      []collectionDocumentResponse `json:"items"`
	NextCursor *string                      `json:"next_cursor"`
}

type createDocumentRequest struct {
	Title     string  `json:"title"`
	SourceURI *string `json:"source_uri"`
	MimeType  *string `json:"mime_type"`
	SizeBytes *int64  `json:"size_bytes"`
}

// optionalValue 在传输层表达 PATCH 的三态：
//   - 字段没出现：Set=false
//   - 字段是 null：Set=true, Value=nil
//   - 字段有值：Set=true, Value!=nil
//
// 不能直接用 *T：JSON 的"缺省"和"显式 null"都会让 *T 变成 nil，无法区分
// "不改"和"清空"。领域层的 knowledge.Optional 表达同样的语义，但那个类型
// 不带 JSON 行为，所以这里单独实现 UnmarshalJSON。
type optionalValue[T any] struct {
	Set   bool
	Value *T
}

func (o *optionalValue[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		o.Value = nil
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

type updateDocumentRequest struct {
	// title 的 null 与缺省都表示"不改"（见 ADR 004 / OpenAPI），所以用 *string。
	Title     *string               `json:"title"`
	SourceURI optionalValue[string] `json:"source_uri"`
	MimeType  optionalValue[string] `json:"mime_type"`
	SizeBytes optionalValue[int64]  `json:"size_bytes"`
}

type addDocumentRequest struct {
	DocumentID string `json:"document_id"`
}

// handleCreateDocument 处理 POST /api/v1/documents。
func (s *Server) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	var req createDocumentRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		s.writeInvalidJSON(w, r, err)
		return
	}

	d, err := s.documents.CreateDocument(r.Context(), req.Title, req.SourceURI, req.MimeType, req.SizeBytes)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}

	w.Header().Set("Location", documentsPath+"/"+d.ID.String())
	respond.JSON(w, s.logger, http.StatusCreated, documentToResponse(d))
}

// handleGetDocument 处理 GET /api/v1/documents/{id}。
func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	d, err := s.documents.GetDocument(r.Context(), id)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}
	respond.JSON(w, s.logger, http.StatusOK, documentToResponse(d))
}

// handleUpdateDocument 处理 PATCH /api/v1/documents/{id}。
func (s *Server) handleUpdateDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req updateDocumentRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		s.writeInvalidJSON(w, r, err)
		return
	}

	d, err := s.documents.UpdateDocument(r.Context(), id, knowledge.DocumentUpdate{
		Title:     req.Title,
		SourceURI: knowledge.Optional[string]{Set: req.SourceURI.Set, Value: req.SourceURI.Value},
		MimeType:  knowledge.Optional[string]{Set: req.MimeType.Set, Value: req.MimeType.Value},
		SizeBytes: knowledge.Optional[int64]{Set: req.SizeBytes.Set, Value: req.SizeBytes.Value},
	})
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}
	respond.JSON(w, s.logger, http.StatusOK, documentToResponse(d))
}

// handleDeleteDocument 处理 DELETE /api/v1/documents/{id}。
func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if err := s.documents.SoftDeleteDocument(r.Context(), id); err != nil {
		s.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRestoreDocument 处理 POST /api/v1/documents/{id}/restore。
func (s *Server) handleRestoreDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	d, err := s.documents.RestoreDocument(r.Context(), id)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}
	respond.JSON(w, s.logger, http.StatusOK, documentToResponse(d))
}

// handleAddDocumentToCollection 处理 POST /api/v1/collections/{id}/documents。
func (s *Server) handleAddDocumentToCollection(w http.ResponseWriter, r *http.Request) {
	collectionID, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req addDocumentRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		s.writeInvalidJSON(w, r, err)
		return
	}
	documentID, err := uuid.Parse(req.DocumentID)
	if err != nil {
		respond.Error(w, s.logger, http.StatusBadRequest,
			apperr.CodeInvalidArgument, "document_id 不是合法的 UUID")
		return
	}

	cd, err := s.documents.AddDocumentToCollection(r.Context(), collectionID, documentID)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}

	w.Header().Set("Location", collectionsPath+"/"+collectionID.String()+"/documents/"+documentID.String())
	respond.JSON(w, s.logger, http.StatusCreated, collectionDocumentToResponse(cd))
}

// handleListDocumentsByCollection 处理 GET /api/v1/collections/{id}/documents。
func (s *Server) handleListDocumentsByCollection(w http.ResponseWriter, r *http.Request) {
	collectionID, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	limit, err := parseListLimit(r.URL.Query().Get("limit"), knowledge.DefaultDocumentListLimit, knowledge.MaxDocumentListLimit)
	if err != nil {
		respond.FromError(w, s.logger, err)
		return
	}

	var cursor *knowledge.Cursor
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, err = decodeListCursor(raw)
		if err != nil {
			respond.FromError(w, s.logger, err)
			return
		}
	}

	items, next, err := s.documents.ListDocumentsByCollection(r.Context(), collectionID, cursor, limit)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}

	resp := collectionDocumentListResponse{
		Items: make([]collectionDocumentResponse, 0, len(items)),
	}
	for _, item := range items {
		resp.Items = append(resp.Items, collectionDocumentToResponse(item))
	}
	if next != nil {
		encoded, err := encodeListCursor(next)
		if err != nil {
			respond.FromError(w, s.logger, apperr.Wrap(err, apperr.CodeInternal, "生成游标失败"))
			return
		}
		resp.NextCursor = &encoded
	}
	respond.JSON(w, s.logger, http.StatusOK, resp)
}

// handleRemoveDocumentFromCollection 处理
// DELETE /api/v1/collections/{id}/documents/{document_id}。
func (s *Server) handleRemoveDocumentFromCollection(w http.ResponseWriter, r *http.Request) {
	collectionID, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}
	documentID, ok := s.pathUUID(w, r, "document_id")
	if !ok {
		return
	}

	if err := s.documents.RemoveDocumentFromCollection(r.Context(), collectionID, documentID); err != nil {
		s.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func documentToResponse(d *knowledge.Document) documentResponse {
	return documentResponse{
		ID:        d.ID.String(),
		Title:     d.Title,
		SourceURI: d.SourceURI,
		MimeType:  d.MimeType,
		SizeBytes: d.SizeBytes,
		CreatedAt: d.CreatedAt.UTC(),
		UpdatedAt: d.UpdatedAt.UTC(),
	}
}

func collectionDocumentToResponse(cd *knowledge.CollectionDocument) collectionDocumentResponse {
	return collectionDocumentResponse{
		Document: documentToResponse(cd.Document),
		AddedAt:  cd.AddedAt.UTC(),
	}
}
