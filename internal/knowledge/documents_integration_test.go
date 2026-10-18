//go:build integration

package knowledge

import "testing"

// documents repository 的测试矩阵（实现后逐个补）：
//
//	TestIntegrationDocumentCRUD
//	  - Create：可空字段 nil / 非 nil 往返
//	  - Get：不存在 → ErrNotFound
//	  - Update：只改 title / 清空 source_uri / 清空 size_bytes / 空 patch no-op
//	  - SoftDelete：软删后 Get/Update 都 ErrNotFound；重复软删 ErrNotFound
//
//	TestIntegrationDocumentAssociation
//	  - Add 成功；重复 Add → ErrConflict
//	  - collection 不存在 / document 不存在 / 已软删 → ErrNotFound
//	  - Remove 成功；重复 Remove → ErrNotFound
//
//	TestIntegrationListDocumentsByCollection
//	  - 同一毫秒按 document_id 排序
//	  - 第一页/最后一页/空页的 next
//	  - 已软删 document 不出现在列表
//	  - cursor 不被修改
//	  - collection 不存在 → ErrNotFound
func TestIntegrationDocumentCRUD(t *testing.T) {
	t.Skip("TODO(documents): 实现 repository 后补 CRUD 集成测试")
}

func TestIntegrationDocumentAssociation(t *testing.T) {
	t.Skip("TODO(documents): 实现 repository 后补关联集成测试")
}

func TestIntegrationListDocumentsByCollection(t *testing.T) {
	t.Skip("TODO(documents): 实现 repository 后补 keyset 分页集成测试")
}
