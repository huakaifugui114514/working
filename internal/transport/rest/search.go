package rest

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
)

const searchPath = "/api/v1/search"

// SearchService 是 HTTP 层依赖的检索业务接口。
// *knowledge.SearchService 实现了它；测试可以注入 fake。
type SearchService interface {
	Search(
		ctx context.Context,
		q string,
		collectionID *uuid.UUID,
		limit int,
		offset int,
	) (*knowledge.SearchResult, error)
}

type searchHitResponse struct {
	ChunkID       string  `json:"chunk_id"`
	DocumentID    string  `json:"document_id"`
	DocumentTitle string  `json:"document_title"`
	ChunkIndex    int     `json:"chunk_index"`
	Content       string  `json:"content"`
	Score         float64 `json:"score"`
}

type searchResponse struct {
	Items      []searchHitResponse `json:"items"`
	NextOffset *int                `json:"next_offset"`
}

// handleSearch 处理 GET /api/v1/search。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	limit, err := parseSearchQueryInt(r, "limit", knowledge.DefaultSearchLimit, 1, knowledge.MaxSearchLimit)
	if err != nil {
		respond.FromError(w, s.logger, err)
		return
	}
	offset, err := parseSearchQueryInt(r, "offset", 0, 0, knowledge.MaxSearchWindow)
	if err != nil {
		respond.FromError(w, s.logger, err)
		return
	}

	var collectionID *uuid.UUID
	if raw := r.URL.Query().Get("collection_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			respond.Error(w, s.logger, http.StatusBadRequest,
				apperr.CodeInvalidArgument, "collection_id 不是合法的 UUID")
			return
		}
		collectionID = &id
	}

	result, err := s.searches.Search(
		r.Context(),
		r.URL.Query().Get("q"),
		collectionID,
		limit,
		offset,
	)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}

	items := make([]searchHitResponse, 0, len(result.Items))
	for _, hit := range result.Items {
		items = append(items, searchHitResponse{
			ChunkID:       hit.ChunkID.String(),
			DocumentID:    hit.DocumentID.String(),
			DocumentTitle: hit.DocumentTitle,
			ChunkIndex:    hit.ChunkIndex,
			Content:       hit.Content,
			Score:         hit.Score,
		})
	}

	respond.JSON(w, s.logger, http.StatusOK, searchResponse{
		Items:      items,
		NextOffset: result.NextOffset,
	})
}

// parseSearchQueryInt 解析可缺省的整数 query 参数。
func parseSearchQueryInt(r *http.Request, name string, defaultValue, minValue, maxValue int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return defaultValue, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperr.New(apperr.CodeInvalidArgument, name+" 必须是整数")
	}
	if value < minValue || value > maxValue {
		return 0, apperr.New(
			apperr.CodeInvalidArgument,
			fmt.Sprintf("%s 必须在 %d~%d 之间", name, minValue, maxValue),
		)
	}
	return value, nil
}
