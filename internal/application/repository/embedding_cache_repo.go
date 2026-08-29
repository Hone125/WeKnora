package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// embeddingCacheRow 是 embedding 二级缓存表的 gorm 行映射。
// key 是 sha256(model_id,dimension,text) 的十六进制，全局唯一，天然可跨租户共享；
// vector 是 float32 向量的二进制序列化（PG bytea / SQLite BLOB）。
type embeddingCacheRow struct {
	CacheKey  string    `gorm:"column:cache_key;primaryKey;type:varchar(64)"`
	Vector    []byte    `gorm:"column:vector"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// TableName 指定表名。
func (embeddingCacheRow) TableName() string { return "embedding_cache" }

// embeddingCacheRepository 实现 embedding 二级缓存的字节级读写。
type embeddingCacheRepository struct {
	db *gorm.DB
}

// NewEmbeddingCacheRepository 创建 embedding 二级缓存仓储。
func NewEmbeddingCacheRepository(db *gorm.DB) interfaces.EmbeddingCacheRepository {
	return &embeddingCacheRepository{db: db}
}

// Get 读取 key 对应的向量字节；未命中返回 ok=false 且 err=nil。
func (r *embeddingCacheRepository) Get(ctx context.Context, key string) ([]byte, bool, error) {
	var row embeddingCacheRow
	err := r.db.WithContext(ctx).
		Select("vector").
		Where("cache_key = ?", key).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return row.Vector, true, nil
}

// Set 写入（覆盖）一条缓存。用 ON CONFLICT (cache_key) DO UPDATE，
// 同一 key 幂等：重复写入只刷新向量与时间戳。
func (r *embeddingCacheRepository) Set(ctx context.Context, key string, value []byte) error {
	row := &embeddingCacheRow{
		CacheKey:  key,
		Vector:    value,
		CreatedAt: time.Now(),
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "cache_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"vector", "created_at"}),
		}).
		Create(row).Error
}
