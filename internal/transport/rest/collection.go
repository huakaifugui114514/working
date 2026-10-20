package rest

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
)

const (
	collectionsPath = "/api/v1/collections"

	// maxRequestBytes 限制请求体大小，避免超大 JSON 占内存。
	maxRequestBytes = 1 << 20

	// listCursorBytes = 8 字节 UnixMilli + 16 字节 UUID。
	listCursorBytes = 24
)

// CollectionService 是 HTTP 层依赖的知识库业务接口。
// *knowledge.Service 实现了它；测试可以注入 fake。
type CollectionService interface {
	CreateCollection(ctx context.Context, name, description string) (*knowledge.Collection, error)
	GetCollection(ctx context.Context, id uuid.UUID) (*knowledge.Collection, error)
	ListCollections(ctx context.Context, cursor *knowledge.Cursor, limit int) ([]*knowledge.Collection, *knowledge.Cursor, error)
	UpdateCollection(ctx context.Context, id uuid.UUID, patch knowledge.CollectionUpdate) (*knowledge.Collection, error)
	SoftDeleteCollection(ctx context.Context, id uuid.UUID) error
}

// collectionResponse 是知识库的对外表示。
// 不直接复用领域模型：避免把 DeletedAt 之类的内部字段带出去。
type collectionResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type collectionListResponse struct {
	Items      []collectionResponse `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}

type createCollectionRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type updateCollectionRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// handleCreateCollection 处理 POST /api/v1/collections。
func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	var req createCollectionRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		s.writeInvalidJSON(w, r, err)
		return
	}

	c, err := s.collections.CreateCollection(r.Context(), req.Name, req.Description)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}

	w.Header().Set("Location", collectionsPath+"/"+c.ID.String())
	respond.JSON(w, s.logger, http.StatusCreated, collectionToResponse(c))
}

// handleGetCollection 处理 GET /api/v1/collections/{id}。
func (s *Server) handleGetCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	c, err := s.collections.GetCollection(r.Context(), id)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}
	respond.JSON(w, s.logger, http.StatusOK, collectionToResponse(c))
}

// handleListCollections 处理 GET /api/v1/collections?limit=&cursor=。
func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request) {
	limit, err := parseListLimit(r.URL.Query().Get("limit"), knowledge.DefaultCollectionListLimit, knowledge.MaxCollectionListLimit)
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

	collections, next, err := s.collections.ListCollections(r.Context(), cursor, limit)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}

	resp := collectionListResponse{
		Items: make([]collectionResponse, 0, len(collections)),
	}
	for _, c := range collections {
		resp.Items = append(resp.Items, collectionToResponse(c))
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

// handleUpdateCollection 处理 PATCH /api/v1/collections/{id}。
func (s *Server) handleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req updateCollectionRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		s.writeInvalidJSON(w, r, err)
		return
	}

	c, err := s.collections.UpdateCollection(r.Context(), id, knowledge.CollectionUpdate{
		Name:        req.Name,
		Description: req.Description,
	})
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}
	respond.JSON(w, s.logger, http.StatusOK, collectionToResponse(c))
}

// handleDeleteCollection 处理 DELETE /api/v1/collections/{id}。
func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if err := s.collections.SoftDeleteCollection(r.Context(), id); err != nil {
		s.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pathUUID 解析路径参数里的 UUID；非法值直接写 400。
// collection / document 的路径参数都走这里，避免每个 handler 重复一段解析和错误响应。
func (s *Server) pathUUID(w http.ResponseWriter, r *http.Request, param string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(param))
	if err != nil {
		respond.Error(w, s.logger, http.StatusBadRequest,
			apperr.CodeInvalidArgument, param+" 不是合法的 UUID")
		return uuid.Nil, false
	}
	return id, true
}

func parseListLimit(raw string, defaultLimit, maxLimit int) (int, error) {
	if raw == "" {
		return defaultLimit, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperr.New(apperr.CodeInvalidArgument, "limit 必须是整数")
	}
	if limit < 1 || limit > maxLimit {
		return 0, apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("limit 必须在 1~%d 之间", maxLimit))
	}
	return limit, nil
}

// decodeJSONBody 只接受一个 JSON 对象，拒绝未知字段和 trailing JSON。
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("请求体只能包含一个 JSON 对象")
	}
	return nil
}

func (s *Server) writeInvalidJSON(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.DebugContext(r.Context(), "解析请求体失败", "error", err)
	respond.Error(w, s.logger, http.StatusBadRequest,
		apperr.CodeInvalidArgument, "请求体不是合法的 JSON 对象")
}

// handleServiceError 统一处理知识库接口的错误。
//
// 客户端断开时 r.Context() 已经取消，此时写响应没有意义，也不能记成 500；
// 直接返回，让 AccessLog 记录实际状态。
// context.DeadlineExceeded 目前没有服务端请求超时策略，仍按内部错误处理。
func (s *Server) handleServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		s.logger.InfoContext(r.Context(), "请求已取消")
		return
	}
	respond.FromError(w, s.logger, err)
}

func collectionToResponse(c *knowledge.Collection) collectionResponse {
	return collectionResponse{
		ID:          c.ID.String(),
		Name:        c.Name,
		Description: c.Description,
		CreatedAt:   c.CreatedAt.UTC(),
		UpdatedAt:   c.UpdatedAt.UTC(),
	}
}

// encodeListCursor 把 (created_at, id) 编成不透明 token：
// 8 字节大端 UnixMilli + 16 字节 UUID，再做 base64url（无 padding）。
func encodeListCursor(cursor *knowledge.Cursor) (string, error) {
	if cursor == nil {
		return "", nil
	}

	ms := cursor.CreatedAt.UnixMilli()
	if ms < 0 || cursor.ID == uuid.Nil {
		return "", errors.New("invalid cursor")
	}

	var raw [listCursorBytes]byte
	binary.BigEndian.PutUint64(raw[:8], uint64(ms))
	copy(raw[8:], cursor.ID[:])
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func decodeListCursor(encoded string) (*knowledge.Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != listCursorBytes {
		return nil, apperr.New(apperr.CodeInvalidArgument, "cursor 无效")
	}

	ms := int64(binary.BigEndian.Uint64(raw[:8]))
	if ms < 0 {
		return nil, apperr.New(apperr.CodeInvalidArgument, "cursor 无效")
	}
	id, err := uuid.FromBytes(raw[8:])
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidArgument, "cursor 无效")
	}

	return &knowledge.Cursor{
		CreatedAt: time.UnixMilli(ms).UTC(),
		ID:        id,
	}, nil
}
