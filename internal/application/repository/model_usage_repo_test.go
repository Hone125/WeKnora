package repository

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestModelUsageSQLiteMigrationPersistsAndIsolates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	migration, err := os.ReadFile("../../../migrations/sqlite/000014_model_usages.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(migration)).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewModelUsageRepository(db)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, record := range []*types.ModelUsageRecord{
		{ID: "included", TenantID: 7, ModelID: "a", ModelType: "chat", PromptTokens: 100, CachedTokens: 25, CreatedAt: start.Add(time.Minute)},
		{ID: "other-tenant", TenantID: 8, ModelID: "a", ModelType: "chat", PromptTokens: 900, CreatedAt: start.Add(time.Minute)},
		{ID: "other-model", TenantID: 7, ModelID: "b", ModelType: "chat", PromptTokens: 900, CreatedAt: start.Add(time.Minute)},
		{ID: "outside-window", TenantID: 7, ModelID: "a", ModelType: "chat", PromptTokens: 900, CreatedAt: start.Add(-time.Minute)},
		{ID: "end-boundary", TenantID: 7, ModelID: "a", ModelType: "chat", PromptTokens: 900, CreatedAt: start.Add(time.Hour)},
	} {
		if err := repo.Record(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen a real on-disk database, not an in-memory fake or AutoMigrate schema.
	fresh := open()
	freshConn, err := fresh.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer freshConn.Close()
	rows, err := NewModelUsageRepository(fresh).Aggregate(context.Background(), 7, []string{"a"}, start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CallCount != 1 || rows[0].PromptTokens != 100 || rows[0].CacheHitRate != 0.25 {
		t.Fatalf("persisted usage or filters are wrong: %+v", rows)
	}
}

func TestModelUsageUsesMigratedTableAndTenantFilter(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: conn}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewModelUsageRepository(db)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO "model_usages"`)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := repo.Record(context.Background(), &types.ModelUsageRecord{ID: "test-usage", TenantID: 7, ModelID: "model-a"}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	mock.ExpectQuery(`FROM "model_usages" WHERE tenant_id = \$1 AND model_id IN \(\$2\) AND created_at >= \$3 AND created_at < \$4`).
		WithArgs(uint64(7), "model-a", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"model_id", "model_type", "call_count", "prompt_tokens", "cached_tokens"}).AddRow("model-a", "chat", 1, 100, 25))
	rows, err := repo.Aggregate(context.Background(), 7, []string{"model-a"}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CacheHitRate != 0.25 {
		t.Fatalf("unexpected aggregate: %+v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
