-- 0003_document_content down：删除正文与分块表
--
-- 先删 chunks 再删 contents；虽然当前没有外键，保持“子表先删”的顺序更安全。
DROP TABLE document_chunks;
DROP TABLE document_contents;
