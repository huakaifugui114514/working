//go:build integration

package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
)

// 集成测试：需要本地 MySQL（deploy/docker-compose.yml）且已执行迁移。
// 默认不跑（make check 不带 -tags=integration）；手动运行：
//
//	set -a; . deploy/.env; set +a
//	KF_DATABASE_HOST=127.0.0.1 KF_DATABASE_PORT=$MYSQL_PORT \
//	KF_DATABASE_NAME=$MYSQL_DATABASE KF_DATABASE_USER=$MYSQL_USER \
//	KF_DATABASE_PASSWORD=$MYSQL_PASSWORD \
//		go test -tags=integration -count=1 -v ./internal/knowledge/
//
// 每个测试用一条固定连接，在会话里建同名 TEMPORARY TABLE collections。
// 临时表会遮蔽真实表，测试结束连接关闭后自动消失，不会碰真实数据。
const createTempCollectionsTable = `
CREATE TEMPORARY TABLE collections (
    id          BINARY(16)    NOT NULL,
    name        VARCHAR(255)  NOT NULL,
    description VARCHAR(1024)     NULL,
    created_at  DATETIME(3)   NOT NULL,
    updated_at  DATETIME(3)   NOT NULL,
    deleted_at  DATETIME(3)       NULL DEFAULT NULL,
    PRIMARY KEY (id),
    KEY idx_created_id (created_at, id)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci`

func newIntegrationRepo(t *testing.T) (*MySQLRepository, *sql.Conn) {
	t.Helper()

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	db, err := database.Open(context.Background(), cfg.Database,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("连接本地 MySQL 失败（先 cd deploy && docker compose up -d）: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("获取固定连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := conn.ExecContext(context.Background(), createTempCollectionsTable); err != nil {
		t.Fatalf("创建临时 collections 表失败: %v", err)
	}
	return &MySQLRepository{db: conn}, conn
}

func insertTestCollection(
	t *testing.T,
	conn *sql.Conn,
	id uuid.UUID,
	name string,
	description any,
	createdAt time.Time,
	deletedAt *time.Time,
) {
	t.Helper()
	_, err := conn.ExecContext(context.Background(),
		`INSERT INTO collections (id, name, description, created_at, updated_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id[:], name, description, createdAt, createdAt, deletedAt)
	if err != nil {
		t.Fatalf("插入测试 collection 失败: %v", err)
	}
}

func testUUID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-%012d", n))
}

func TestIntegrationCollectionCRUD(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	ctx := context.Background()

	created, err := repo.CreateCollection(ctx, "it-create", "原始描述")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("CreateCollection 应生成非零 UUID")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("CreateCollection 应填 CreatedAt / UpdatedAt")
	}

	got, err := repo.GetCollection(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if got.ID != created.ID || got.Name != created.Name || got.Description != created.Description {
		t.Fatalf("GetCollection = %+v, want %+v", got, created)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) || !got.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("时间往返不一致: got=%v/%v want=%v/%v",
			got.CreatedAt, got.UpdatedAt, created.CreatedAt, created.UpdatedAt)
	}

	if _, err := repo.GetCollection(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的 id 应返回 ErrNotFound, got %v", err)
	}
}

func TestIntegrationListCollectionsKeyset(t *testing.T) {
	repo, conn := newIntegrationRepo(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond)
	ids := make([]uuid.UUID, 6)
	for i := range ids {
		ids[i] = testUUID(i + 1)
	}

	insertTestCollection(t, conn, ids[0], "r0", "d0", base.Add(-2*time.Millisecond), nil)
	insertTestCollection(t, conn, ids[1], "r1", "d1", base.Add(-time.Millisecond), nil)
	insertTestCollection(t, conn, ids[2], "r2", "d2", base, nil)
	insertTestCollection(t, conn, ids[3], "r3", "d3", base, nil)
	insertTestCollection(t, conn, ids[4], "r4", "d4", base, nil)
	deletedAt := base
	insertTestCollection(t, conn, ids[5], "r5-deleted", nil, base, &deletedAt)

	page, next, err := repo.ListCollections(ctx, nil, 2)
	if err != nil {
		t.Fatalf("ListCollections 第一页: %v", err)
	}
	if len(page) != 2 || page[0].ID != ids[4] || page[1].ID != ids[3] {
		t.Fatalf("第一页 = %v, want [%v %v]", idsOf(page), ids[4], ids[3])
	}
	if next == nil || next.ID != ids[3] || !next.CreatedAt.Equal(base) {
		t.Fatalf("第一页 next = %+v, want (base, %v)", next, ids[3])
	}

	page, next, err = repo.ListCollections(ctx, next, 2)
	if err != nil {
		t.Fatalf("ListCollections 第二页: %v", err)
	}
	if len(page) != 2 || page[0].ID != ids[2] || page[1].ID != ids[1] {
		t.Fatalf("第二页 = %v, want [%v %v]", idsOf(page), ids[2], ids[1])
	}
	if next == nil || next.ID != ids[1] {
		t.Fatalf("第二页 next = %+v, want id=%v", next, ids[1])
	}

	page, next, err = repo.ListCollections(ctx, next, 2)
	if err != nil {
		t.Fatalf("ListCollections 第三页: %v", err)
	}
	if len(page) != 1 || page[0].ID != ids[0] {
		t.Fatalf("第三页 = %v, want [%v]", idsOf(page), ids[0])
	}
	if next != nil {
		t.Fatalf("最后一页 next 应为 nil, got %+v", next)
	}

	all, next, err := repo.ListCollections(ctx, nil, 5)
	if err != nil {
		t.Fatalf("ListCollections 刚好一页: %v", err)
	}
	if len(all) != 5 || next != nil {
		t.Fatalf("刚好一页: len=%d next=%+v, want len=5 next=nil", len(all), next)
	}
	for _, c := range all {
		if c.ID == ids[5] {
			t.Fatal("已软删的记录不应出现在列表里")
		}
	}

	old := &Cursor{CreatedAt: base.Add(-time.Hour), ID: uuid.Nil}
	empty, next, err := repo.ListCollections(ctx, old, 2)
	if err != nil {
		t.Fatalf("ListCollections 空页: %v", err)
	}
	if len(empty) != 0 || next != nil {
		t.Fatalf("空页 = %v, next=%+v, want empty + nil", idsOf(empty), next)
	}

	cur := &Cursor{CreatedAt: base, ID: ids[3]}
	before := *cur
	if _, _, err := repo.ListCollections(ctx, cur, 1); err != nil {
		t.Fatalf("ListCollections 不应修改 cursor: %v", err)
	}
	if *cur != before {
		t.Fatalf("ListCollections 修改了调用方的 cursor: got %+v, want %+v", *cur, before)
	}
}

func TestIntegrationUpdateCollectionPatch(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	ctx := context.Background()

	created, err := repo.CreateCollection(ctx, "it-update", "原始描述")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	newName := "新名字"
	updated, err := repo.UpdateCollection(ctx, created.ID, CollectionUpdate{Name: &newName})
	if err != nil {
		t.Fatalf("UpdateCollection(name): %v", err)
	}
	if updated.Name != newName || updated.Description != "原始描述" {
		t.Fatalf("UpdateCollection(name) = %+v, want name=%q desc=%q",
			updated, newName, "原始描述")
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("UpdatedAt 未刷新: old=%v new=%v", created.UpdatedAt, updated.UpdatedAt)
	}

	got, err := repo.GetCollection(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if got.Name != newName || got.Description != "原始描述" {
		t.Fatalf("只改 name 却动了 description: %+v", got)
	}
	if !got.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("返回的 UpdatedAt 与库中不一致: got=%v db=%v", updated.UpdatedAt, got.UpdatedAt)
	}

	newDesc := "新描述"
	updated2, err := repo.UpdateCollection(ctx, created.ID, CollectionUpdate{Description: &newDesc})
	if err != nil {
		t.Fatalf("UpdateCollection(description): %v", err)
	}
	if updated2.Name != newName || updated2.Description != newDesc {
		t.Fatalf("UpdateCollection(description) = %+v, want name=%q desc=%q",
			updated2, newName, newDesc)
	}

	same, err := repo.UpdateCollection(ctx, created.ID, CollectionUpdate{})
	if err != nil {
		t.Fatalf("空 PATCH 应为 no-op: %v", err)
	}
	if same.Name != newName || same.Description != newDesc || !same.UpdatedAt.Equal(updated2.UpdatedAt) {
		t.Fatalf("空 PATCH 不应改数据: %+v", same)
	}

	if _, err := repo.UpdateCollection(ctx, uuid.New(), CollectionUpdate{Name: &newName}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的 id 应返回 ErrNotFound, got %v", err)
	}

	toDelete, err := repo.CreateCollection(ctx, "it-update-deleted", "d")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if err := repo.SoftDeleteCollection(ctx, toDelete.ID); err != nil {
		t.Fatalf("SoftDeleteCollection: %v", err)
	}
	if _, err := repo.UpdateCollection(ctx, toDelete.ID, CollectionUpdate{Name: &newName}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已软删的 id 应返回 ErrNotFound, got %v", err)
	}
}

func TestIntegrationSoftDelete(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	ctx := context.Background()

	created, err := repo.CreateCollection(ctx, "it-soft-delete", "d")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if err := repo.SoftDeleteCollection(ctx, created.ID); err != nil {
		t.Fatalf("第一次 SoftDeleteCollection: %v", err)
	}
	if _, err := repo.GetCollection(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("软删后 GetCollection 应返回 ErrNotFound, got %v", err)
	}
	if err := repo.SoftDeleteCollection(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复软删应返回 ErrNotFound, got %v", err)
	}

	page, next, err := repo.ListCollections(ctx, nil, 10)
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(page) != 0 || next != nil {
		t.Fatalf("软删记录不应出现在列表里: %v, next=%+v", idsOf(page), next)
	}
}

func TestIntegrationNullableDescription(t *testing.T) {
	repo, conn := newIntegrationRepo(t)
	ctx := context.Background()

	id := testUUID(42)
	createdAt := time.Now().UTC().Truncate(time.Millisecond)
	insertTestCollection(t, conn, id, "it-null-description", nil, createdAt, nil)

	got, err := repo.GetCollection(ctx, id)
	if err != nil {
		t.Fatalf("GetCollection(NULL description): %v", err)
	}
	if got.Description != "" {
		t.Fatalf("NULL description 应折叠成空串, got %q", got.Description)
	}

	page, _, err := repo.ListCollections(ctx, nil, 10)
	if err != nil {
		t.Fatalf("ListCollections(NULL description): %v", err)
	}
	if len(page) != 1 || page[0].Description != "" {
		t.Fatalf("ListCollections 应把 NULL description 折叠成空串: %+v", page)
	}
}

func idsOf(collections []*Collection) []uuid.UUID {
	ids := make([]uuid.UUID, len(collections))
	for i, c := range collections {
		ids[i] = c.ID
	}
	return ids
}
