package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
)

// Server 是 stdio MCP Server。
type Server struct {
	api    *APIClient
	logger *slog.Logger
}

// NewServer 注入 API 客户端与 logger。
func NewServer(api *APIClient, logger *slog.Logger) *Server {
	if api == nil {
		panic("mcp.NewServer: api is nil")
	}
	if logger == nil {
		panic("mcp.NewServer: logger is nil")
	}
	return &Server{api: api, logger: logger}
}

// Serve 从 in 读 JSON-RPC 行，向 out 写响应。stdout 只放协议消息，日志走 stderr。
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		resp, hasResponse := s.handle(ctx, req)
		if hasResponse {
			if err := enc.Encode(resp); err != nil {
				return fmt.Errorf("mcp: 写响应失败: %w", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("mcp: 读 stdin 失败: %w", err)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	if req.JSONRPC != "2.0" || req.Method == "" {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "invalid request"}}, true
	}
	// notification：JSON-RPC 不要求响应。
	if len(req.ID) == 0 {
		return rpcResponse{}, false
	}
	switch req.Method {
	case "initialize":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "knowflow-mcp", "version": "1.0.0"},
		}}, true
	case "ping":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}, true
	case "tools/list":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": toolDefs()}}, true
	case "tools/call":
		return s.callTool(ctx, req)
	default:
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}}, true
	}
}

func (s *Server) callTool(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}}, true
	}

	var (
		result any
		err    error
	)
	switch params.Name {
	case "knowledge_search":
		query, _ := params.Arguments["query"].(string)
		if strings.TrimSpace(query) == "" {
			return toolError(req.ID, "query 不能为空"), true
		}
		collectionID, _ := params.Arguments["collection_id"].(string)
		limit := 5
		if raw, ok := params.Arguments["limit"]; ok {
			switch v := raw.(type) {
			case float64:
				limit = int(v)
			case string:
				parsed, parseErr := strconv.Atoi(v)
				if parseErr == nil {
					limit = parsed
				}
			}
		}
		result, err = s.api.Search(ctx, query, collectionID, limit)
	case "document_metadata":
		documentID, _ := params.Arguments["document_id"].(string)
		if strings.TrimSpace(documentID) == "" {
			return toolError(req.ID, "document_id 不能为空"), true
		}
		result, err = s.api.Document(ctx, documentID)
	case "knowledge_ask":
		message, _ := params.Arguments["message"].(string)
		if strings.TrimSpace(message) == "" {
			return toolError(req.ID, "message 不能为空"), true
		}
		collectionID, _ := params.Arguments["collection_id"].(string)
		result, err = s.api.Ask(ctx, message, collectionID)
	default:
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}}, true
	}
	if err != nil {
		s.logger.WarnContext(ctx, "MCP tool 调用失败", "tool", params.Name, "error", err)
		return toolError(req.ID, "工具调用失败: "+err.Error()), true
	}
	text, _ := json.MarshalIndent(result, "", "  ")
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: toolResult{
		Content: []toolContent{{Type: "text", Text: string(text)}},
	}}, true
}

func toolError(id json.RawMessage, message string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: toolResult{
		Content: []toolContent{{Type: "text", Text: message}},
		IsError: true,
	}}
}

func toolDefs() []toolDef {
	return []toolDef{
		{
			Name:        "knowledge_search",
			Description: "在 KnowFlow 知识库中检索与 query 相关的 chunk，返回 score 和来源。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":         map[string]any{"type": "string", "description": "检索词"},
					"collection_id": map[string]any{"type": "string", "description": "可选知识库 UUID"},
					"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 5},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "document_metadata",
			Description: "按 document_id 查询文档元数据。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"document_id": map[string]any{"type": "string", "description": "文档 UUID"},
				},
				"required": []string{"document_id"},
			},
		},
		{
			Name:        "knowledge_ask",
			Description: "向 KnowFlow 发起单轮 RAG 问答，返回 answer 和 citations。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message":       map[string]any{"type": "string", "description": "问题"},
					"collection_id": map[string]any{"type": "string", "description": "可选知识库 UUID"},
				},
				"required": []string{"message"},
			},
		},
	}
}
