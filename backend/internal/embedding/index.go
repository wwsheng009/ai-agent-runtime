package embedding

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// VectorDim 向量维度
const VectorDim = 768

// Embedding 向量嵌入
type Embedding struct {
	Vector []float32
}

// NewEmbedding 创建新的向量嵌入
func NewEmbedding(vector []float32) (*Embedding, error) {
	if len(vector) != VectorDim {
		return nil, fmt.Errorf("vector dimension mismatch: expected %d, got %d", VectorDim, len(vector))
	}
	return &Embedding{Vector: vector}, nil
}

// Normalize 归一化向量
func (e *Embedding) Normalize() {
	var norm float32
	for _, v := range e.Vector {
		norm += v * v
	}
	norm = float32(math.Sqrt(float64(norm)))

	if norm > 0 {
		for i := range e.Vector {
			e.Vector[i] /= norm
		}
	}
}

// CosineSimilarity 计算余弦相似度
func (e *Embedding) CosineSimilarity(other *Embedding) float32 {
	if len(e.Vector) != len(other.Vector) {
		return 0
	}

	var dotProduct float32
	for i := range e.Vector {
		dotProduct += e.Vector[i] * other.Vector[i]
	}

	// 假设向量已经归一化
	return dotProduct
}

// EuclideanDistance 计算欧几里得距离
func (e *Embedding) EuclideanDistance(other *Embedding) float32 {
	if len(e.Vector) != len(other.Vector) {
		return math.MaxFloat32
	}

	var sum float32
	for i := range e.Vector {
		diff := e.Vector[i] - other.Vector[i]
		sum += diff * diff
	}

	return float32(math.Sqrt(float64(sum)))
}

// EmbeddingGenerator 嵌入生成器接口
type EmbeddingGenerator interface {
	// Generate 为文本生成嵌入向量
	Generate(text string) (*Embedding, error)

	// GenerateBatch 批量生成嵌入向量
	GenerateBatch(texts []string) ([]*Embedding, error)

	// GetDimension 获取向量维度
	GetDimension() int
}

var tokenPattern = regexp.MustCompile(`[\p{L}\p{N}_/.-]+`)

// LocalEmbeddingGenerator 本地嵌入生成器（基于特征哈希）
type LocalEmbeddingGenerator struct {
	dim int
}

// MockEmbeddingGenerator 兼容旧名称
type MockEmbeddingGenerator = LocalEmbeddingGenerator

// NewLocalEmbeddingGenerator 创建本地嵌入生成器
func NewLocalEmbeddingGenerator(dim int) *LocalEmbeddingGenerator {
	if dim <= 0 {
		dim = VectorDim
	}
	return &LocalEmbeddingGenerator{dim: dim}
}

// NewMockEmbeddingGenerator 创建兼容旧名称的本地嵌入生成器
func NewMockEmbeddingGenerator(dim int) *MockEmbeddingGenerator {
	return NewLocalEmbeddingGenerator(dim)
}

// Generate 生成文本的本地嵌入
func (g *LocalEmbeddingGenerator) Generate(text string) (*Embedding, error) {
	vector := make([]float32, g.dim)
	features := collectEmbeddingFeatures(text)
	for feature, weight := range features {
		idx, sign := hashFeature(feature, g.dim)
		vector[idx] += sign * weight
	}

	return NewEmbedding(vector)
}

// GenerateBatch 批量生成本地嵌入
func (g *LocalEmbeddingGenerator) GenerateBatch(texts []string) ([]*Embedding, error) {
	embeddings := make([]*Embedding, 0, len(texts))
	for _, text := range texts {
		embedding, err := g.Generate(text)
		if err != nil {
			return nil, err
		}
		embeddings = append(embeddings, embedding)
	}
	return embeddings, nil
}

// GetDimension 获取向量维度
func (g *LocalEmbeddingGenerator) GetDimension() int {
	return g.dim
}

func collectEmbeddingFeatures(text string) map[string]float32 {
	normalized := strings.ToLower(strings.TrimSpace(text))
	tokens := tokenPattern.FindAllString(normalized, -1)
	features := make(map[string]float32)

	for i, token := range tokens {
		if token == "" {
			continue
		}
		features["tok:"+token] += 3

		runes := []rune(token)
		if len(runes) > 2 {
			for start := 0; start <= len(runes)-3; start++ {
				features["tri:"+string(runes[start:start+3])] += 1
			}
		}

		if i < len(tokens)-1 {
			features["bi:"+token+"|"+tokens[i+1]] += 2
		}
	}

	if len(tokens) == 0 && normalized != "" {
		for _, r := range []rune(normalized) {
			features["chr:"+string(r)] += 1
		}
	}

	return features
}

func hashFeature(feature string, dim int) (int, float32) {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(feature))
	sum := hasher.Sum64()
	index := int(sum % uint64(dim))
	sign := float32(1)
	if sum&1 == 1 {
		sign = -1
	}
	return index, sign
}

// IndexedItem 索引项
type IndexedItem struct {
	ID        string         // 唯一标识
	Content   string         // 内容
	Embedding *Embedding     // 嵌入向量
	Metadata  map[string]any // 元数据
}

// VectorIndex 向量索引
type VectorIndex struct {
	items     []*IndexedItem
	generator EmbeddingGenerator
	dim       int
	mu        sync.RWMutex
}

// NewVectorIndex 创建新的向量索引
func NewVectorIndex(generator EmbeddingGenerator) (*VectorIndex, error) {
	if generator == nil {
		generator = NewLocalEmbeddingGenerator(VectorDim)
	}

	dim := generator.GetDimension()
	if dim <= 0 {
		return nil, fmt.Errorf("invalid vector dimension: %d", dim)
	}

	return &VectorIndex{
		items:     make([]*IndexedItem, 0),
		generator: generator,
		dim:       dim,
	}, nil
}

// Add 添加单个项到索引
func (vi *VectorIndex) Add(id, content string, metadata map[string]any) error {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	// 生成嵌入
	embedding, err := vi.generator.Generate(content)
	if err != nil {
		return fmt.Errorf("failed to generate embedding: %w", err)
	}

	// 归一化
	embedding.Normalize()

	item := &IndexedItem{
		ID:        id,
		Content:   content,
		Embedding: embedding,
		Metadata:  metadata,
	}

	vi.items = append(vi.items, item)
	return nil
}

// AddBatch 批量添加项到索引
func (vi *VectorIndex) AddBatch(items []struct {
	ID       string
	Content  string
	Metadata map[string]any
}) error {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	// 准备文本列表
	texts := make([]string, 0, len(items))
	for _, item := range items {
		texts = append(texts, item.Content)
	}

	// 批量生成嵌入
	embeddings, err := vi.generator.GenerateBatch(texts)
	if err != nil {
		return fmt.Errorf("failed to generate embeddings: %w", err)
	}

	// 归一化嵌入
	for _, emb := range embeddings {
		emb.Normalize()
	}

	// 添加到索引
	for i, item := range items {
		indexedItem := &IndexedItem{
			ID:        item.ID,
			Content:   item.Content,
			Embedding: embeddings[i],
			Metadata:  item.Metadata,
		}
		vi.items = append(vi.items, indexedItem)
	}

	return nil
}

// Search 相似度搜索
func (vi *VectorIndex) Search(query string, topK int, threshold float32) ([]*SearchResult, error) {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	// 生成查询嵌入
	queryEmbedding, err := vi.generator.Generate(query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate query embedding: %w", err)
	}
	queryEmbedding.Normalize()

	// 计算相似度
	results := make([]*SearchResult, 0, len(vi.items))
	for i, item := range vi.items {
		similarity := queryEmbedding.CosineSimilarity(item.Embedding)

		if similarity >= threshold {
			results = append(results, &SearchResult{
				Item:     item,
				Score:    similarity,
				Rank:     i,
				Distance: queryEmbedding.EuclideanDistance(item.Embedding),
			})
		}
	}

	// 按相似度排序
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// 返回前 K 个结果
	if topK > 0 && topK < len(results) {
		results = results[:topK]
	}

	return results, nil
}

// SearchByVector 使用向量搜索
func (vi *VectorIndex) SearchByVector(queryVector *Embedding, topK int, threshold float32) ([]*SearchResult, error) {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	if len(queryVector.Vector) != vi.dim {
		return nil, fmt.Errorf("vector dimension mismatch: expected %d, got %d", vi.dim, len(queryVector.Vector))
	}

	// 确保查询向量已归一化
	queryVector.Normalize()

	// 计算相似度
	results := make([]*SearchResult, 0, len(vi.items))
	for i, item := range vi.items {
		similarity := queryVector.CosineSimilarity(item.Embedding)

		if similarity >= threshold {
			results = append(results, &SearchResult{
				Item:     item,
				Score:    similarity,
				Rank:     i,
				Distance: queryVector.EuclideanDistance(item.Embedding),
			})
		}
	}

	// 按相似度排序
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// 返回前 K 个结果
	if topK > 0 && topK < len(results) {
		results = results[:topK]
	}

	return results, nil
}

// Delete 从索引中删除项
func (vi *VectorIndex) Delete(id string) error {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	for i, item := range vi.items {
		if item.ID == id {
			vi.items = append(vi.items[:i], vi.items[i+1:]...)
			return nil
		}
	}

	return fmt.Errorf("item not found: %s", id)
}

// Update 更新索引中的项
func (vi *VectorIndex) Update(id, content string, metadata map[string]any) error {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	// 查找并删除旧项
	for i, item := range vi.items {
		if item.ID == id {
			// 生成新嵌入
			embedding, err := vi.generator.Generate(content)
			if err != nil {
				return fmt.Errorf("failed to generate embedding: %w", err)
			}
			embedding.Normalize()

			// 更新项
			vi.items[i] = &IndexedItem{
				ID:        id,
				Content:   content,
				Embedding: embedding,
				Metadata:  metadata,
			}

			return nil
		}
	}

	return fmt.Errorf("item not found: %s", id)
}

// Get 获取索引中的项
func (vi *VectorIndex) Get(id string) (*IndexedItem, error) {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	for _, item := range vi.items {
		if item.ID == id {
			return item, nil
		}
	}

	return nil, fmt.Errorf("item not found: %s", id)
}

// List 列出索引中的所有项
func (vi *VectorIndex) List() []*IndexedItem {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	// 返回副本
	items := make([]*IndexedItem, len(vi.items))
	copy(items, vi.items)

	return items
}

// Size 获取索引大小
func (vi *VectorIndex) Size() int {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	return len(vi.items)
}

// Clear 清空索引
func (vi *VectorIndex) Clear() {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	vi.items = make([]*IndexedItem, 0)
}

// Save 保存索引到字节格式（简化实现）
func (vi *VectorIndex) Save() ([]byte, error) {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	// 简化：返回元数据大小
	count := len(vi.items)
	_ = count // 实际实现需要序列化完整数据

	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(len(vi.items)))

	return buf, nil
}

// Load 从字节格式加载索引（简化实现）
func (vi *VectorIndex) Load(data []byte) error {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	// 简化实现
	if len(data) < 8 {
		return fmt.Errorf("invalid data")
	}

	count := binary.BigEndian.Uint64(data[:8])
	_ = count // 实际实现需要解析完整数据

	return nil
}

// SearchResult 搜索结果
type SearchResult struct {
	Item     *IndexedItem // 索引项
	Score    float32      // 相似度分数（0-1）
	Rank     int          // 原始排名
	Distance float32      // 距离
}

// String 返回结果的字符串表示
func (sr *SearchResult) String() string {
	return fmt.Sprintf("Score: %.4f, ID: %s", sr.Score, sr.Item.ID)
}

// GenerateID 生成唯一 ID
func GenerateID(prefix, key string) string {
	hash := md5.Sum([]byte(key))
	hexHash := hex.EncodeToString(hash[:])
	return fmt.Sprintf("%s_%s", prefix, hexHash[:8])
}
