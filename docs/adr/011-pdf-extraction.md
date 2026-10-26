# ADR 011：PDF 文本抽取

- **状态**：已接受并实现
- **日期**：2026-10-27

## 背景

当前正文只能通过 `PUT /content` 传纯文本。真实知识库需要上传 PDF。

## 决定

- 新增 `POST /api/v1/documents/{id}/file`，`multipart/form-data` 字段 `file`。
- 使用 `github.com/ledongthuc/pdf` 纯 Go 提取文本：
  - 不引外部 `pdftotext` 二进制，部署简单；
  - 上限 20 MiB；抽取文本上限 4 MiB；
  - 以 `%PDF-` 魔数校验，不信任文件名/扩展名。
- 抽取后复用现有 `ContentService.ReplaceDocumentContent`：
  - 分块、SHA-256、事务、`embedding_jobs`、`document_events` 全部复用；
  - 不新增第二套入库路径。
- 无可提取文本返回 400，而不是把空正文写进去。

## 被否掉的方案

1. **直接存 PDF 二进制 + 检索时解析**：查询时 CPU 不可控，且 chunk 无法提前算。
2. **调用外部 `pdftotext`**：生产镜像要额外装 poppler，升级/漏洞面更大。
3. **OCR 扫描件**：需要 Tesseract/云 OCR，当前不引入。

## 代价

- 扫描件/纯图片 PDF 提取不到文本，需要 OCR（未实现）。
- 复杂排版/表格会丢结构；当前只保证纯文本可检索。
- PDF 解析库对恶意文件的健壮性需要持续关注；已加大小/文本上限。
- 解析在 HTTP 请求内同步完成；超大 PDF 会占请求时间，后续可改为异步 Workflow 节点。
