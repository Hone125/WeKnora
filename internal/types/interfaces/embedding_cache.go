package interfaces

import "context"

// EmbeddingCacheRepository 定义 embedding 二级缓存（DB 持久化）的字节级读写。
// 它是 embedding.EmbeddingCacheStore 的仓储侧实现，由 container 注入到
// embedding 包（见 container.registerEmbeddingCacheStore）。key 由 embedding
// 包生成（sha256(model_id,dimension,text)），此处不关心其构成，仅按字节存取向量。
type EmbeddingCacheRepository interface {
	// Get 返回 key 对应的向量字节；ok=false 且 err=nil 表示未命中。
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)
	// Set 写入（覆盖）一条缓存。调用方异步调用，不阻塞 embedding 主链。
	Set(ctx context.Context, key string, value []byte) error
}
