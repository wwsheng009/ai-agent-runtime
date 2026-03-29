package embedding

import (
	"testing"
)

func TestNewMockEmbeddingGenerator(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)

	if generator == nil {
		t.Fatal("NewMockEmbeddingGenerator() returned nil")
	}

	if generator.dim != 768 {
		t.Errorf("dim = %v, want %v", generator.dim, 768)
	}
}

func TestNewMockEmbeddingGenerator_DefaultDim(t *testing.T) {
	generator := NewMockEmbeddingGenerator(0)

	if generator.dim != VectorDim {
		t.Errorf("dim = %v, want %v", generator.dim, VectorDim)
	}
}

func TestMockEmbeddingGenerator_Generate(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)

	embedding, err := generator.Generate("test text")
	if err != nil {
		t.Fatalf("Generate() returned error: %v", err)
	}

	if embedding == nil {
		t.Fatal("Generate() returned nil embedding")
	}

	if len(embedding.Vector) != 768 {
		t.Errorf("Vector length = %v, want %v", len(embedding.Vector), 768)
	}
}

func TestMockEmbeddingGenerator_GenerateBatch(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)

	texts := []string{"text1", "text2", "text3"}
	embeddings, err := generator.GenerateBatch(texts)
	if err != nil {
		t.Fatalf("GenerateBatch() returned error: %v", err)
	}

	if len(embeddings) != len(texts) {
		t.Errorf("Generated %v embeddings, want %v", len(embeddings), len(texts))
	}

	for i, emb := range embeddings {
		if len(emb.Vector) != 768 {
			t.Errorf("Embedding %v has length %v, want %v", i, len(emb.Vector), 768)
		}
	}
}

func TestNewEmbedding(t *testing.T) {
	vector := make([]float32, 768)
	for i := range vector {
		vector[i] = 0.5
	}

	embedding, err := NewEmbedding(vector)
	if err != nil {
		t.Fatalf("NewEmbedding() returned error: %v", err)
	}

	if embedding == nil {
		t.Fatal("NewEmbedding() returned nil")
	}

	if len(embedding.Vector) != 768 {
		t.Errorf("Vector length = %v, want %v", len(embedding.Vector), 768)
	}
}

func TestNewEmbedding_WrongDimension(t *testing.T) {
	vector := make([]float32, 100)

	_, err := NewEmbedding(vector)
	if err == nil {
		t.Error("NewEmbedding() expected error for wrong dimension")
	}
}

func TestEmbedding_Normalize(t *testing.T) {
	vector := make([]float32, VectorDim) // Use correct dimension
	for i := range vector {
		vector[i] = 1.0
	}

	embedding, err := NewEmbedding(vector)
	if err != nil {
		t.Fatalf("Failed to create embedding: %v", err)
	}
	if embedding == nil {
		t.Fatal("Embedding is nil")
	}

	embedding.Normalize()

	// After normalization, every element should be sqrt(1/768)
	// And the sum of squares should be 1
	var norm float32
	for _, v := range embedding.Vector {
		norm += v * v
	}

	// After sqrt, norm should be close to 1 (sum of squares)
	if norm < 0.99 || norm > 1.01 {
		t.Errorf("Vector norm not normalized properly, got %v", norm)
	}
}

func TestEmbedding_CosineSimilarity(t *testing.T) {
	// Create two identical vectors
	vector1 := make([]float32, 768)
	vector2 := make([]float32, 768)
	for i := range vector1 {
		vector1[i] = 1.0
		vector2[i] = 1.0
	}

	emb1, _ := NewEmbedding(vector1)
	emb2, _ := NewEmbedding(vector2)
	emb1.Normalize()
	emb2.Normalize()

	similarity := emb1.CosineSimilarity(emb2)

	// Similarity should be very close to 1.0
	if similarity < 0.99 || similarity > 1.01 {
		t.Errorf("CosineSimilarity() = %v, want approximately 1.0", similarity)
	}
}

func TestEmbedding_CosineSimilarity_DifferentVectors(t *testing.T) {
	vector1 := make([]float32, 768)
	vector2 := make([]float32, 768)
	for i := range vector1 {
		vector1[i] = 1.0
		vector2[i] = -1.0
	}

	emb1, _ := NewEmbedding(vector1)
	emb2, _ := NewEmbedding(vector2)
	emb1.Normalize()
	emb2.Normalize()

	similarity := emb1.CosineSimilarity(emb2)

	// Similarity should be close to -1.0 (opposite vectors)
	if similarity > -0.99 {
		t.Errorf("CosineSimilarity() = %v, want approximately -1.0", similarity)
	}
}

func TestNewVectorIndex(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)
	index, err := NewVectorIndex(generator)

	if err != nil {
		t.Fatalf("NewVectorIndex() returned error: %v", err)
	}

	if index == nil {
		t.Fatal("NewVectorIndex() returned nil")
	}

	if index.Size() != 0 {
		t.Errorf("Initial Size() = %v, want 0", index.Size())
	}
}

func TestNewVectorIndex_NilGenerator(t *testing.T) {
	index, err := NewVectorIndex(nil)

	if err != nil {
		t.Fatalf("NewVectorIndex(nil) returned error: %v", err)
	}

	if index == nil {
		t.Fatal("NewVectorIndex(nil) returned nil")
	}

	if _, ok := index.generator.(*LocalEmbeddingGenerator); !ok {
		t.Fatalf("expected default generator to be LocalEmbeddingGenerator, got %T", index.generator)
	}
}

func TestVectorIndex_Add(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)
	index, _ := NewVectorIndex(generator)

	err := index.Add("test-id", "test content", map[string]any{"key": "value"})
	if err != nil {
		t.Fatalf("Add() returned error: %v", err)
	}

	if index.Size() != 1 {
		t.Errorf("Size() = %v, want 1", index.Size())
	}

	item, err := index.Get("test-id")
	if err != nil {
		t.Fatalf("Get() returned error: %v", err)
	}

	if item.ID != "test-id" {
		t.Errorf("ID = %v, want test-id", item.ID)
	}

	if item.Content != "test content" {
		t.Errorf("Content = %v, want test content", item.Content)
	}
}

func TestVectorIndex_AddBatch(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)
	index, _ := NewVectorIndex(generator)

	items := []struct {
		ID       string
		Content  string
		Metadata map[string]any
	}{
		{"id1", "content1", map[string]any{"k": "v1"}},
		{"id2", "content2", map[string]any{"k": "v2"}},
		{"id3", "content3", map[string]any{"k": "v3"}},
	}

	err := index.AddBatch(items)
	if err != nil {
		t.Fatalf("AddBatch() returned error: %v", err)
	}

	if index.Size() != 3 {
		t.Errorf("Size() = %v, want 3", index.Size())
	}
}

func TestVectorIndex_Search(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)
	index, _ := NewVectorIndex(generator)

	// Add some items
	items := []struct {
		ID       string
		Content  string
		Metadata map[string]any
	}{
		{"id1", "similar content", map[string]any{"type": "test"}},
		{"id2", "different content", map[string]any{"type": "other"}},
	}

	index.AddBatch(items)

	// Search
	results, err := index.Search("similar", 5, 0.1)
	if err != nil {
		t.Fatalf("Search() returned error: %v", err)
	}

	if len(results) == 0 {
		t.Error("Search() returned no results")
	}
}

func TestLocalEmbeddingGenerator_SemanticOverlap(t *testing.T) {
	generator := NewLocalEmbeddingGenerator(768)

	query, err := generator.Generate("customer invoice status")
	if err != nil {
		t.Fatalf("Generate() query returned error: %v", err)
	}
	similar, err := generator.Generate("customer invoice lookup")
	if err != nil {
		t.Fatalf("Generate() similar returned error: %v", err)
	}
	different, err := generator.Generate("system kernel memory tuning")
	if err != nil {
		t.Fatalf("Generate() different returned error: %v", err)
	}

	query.Normalize()
	similar.Normalize()
	different.Normalize()

	simSimilar := query.CosineSimilarity(similar)
	simDifferent := query.CosineSimilarity(different)
	if simSimilar <= simDifferent {
		t.Fatalf("expected semantic overlap similarity %v to be greater than unrelated similarity %v", simSimilar, simDifferent)
	}
}

func TestVectorIndex_SearchByVector(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)
	index, _ := NewVectorIndex(generator)

	// Add item
	index.Add("test-id", "test content", map[string]any{})

	// Generate a query embedding
	queryEmbedding, _ := generator.Generate("test query")

	// Search by vector
	results, err := index.SearchByVector(queryEmbedding, 5, 0.1)
	if err != nil {
		t.Fatalf("SearchByVector() returned error: %v", err)
	}

	if len(results) == 0 {
		t.Error("SearchByVector() returned no results")
	}
}

func TestVectorIndex_Delete(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)
	index, _ := NewVectorIndex(generator)

	index.Add("test-id", "test content", nil)

	if index.Size() != 1 {
		t.Errorf("Before delete: Size() = %v, want 1", index.Size())
	}

	err := index.Delete("test-id")
	if err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}

	if index.Size() != 0 {
		t.Errorf("After delete: Size() = %v, want 0", index.Size())
	}

	_, err = index.Get("test-id")
	if err == nil {
		t.Error("Get() should return error after delete")
	}
}

func TestVectorIndex_Update(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)
	index, _ := NewVectorIndex(generator)

	index.Add("test-id", "old content", map[string]any{"old": true})

	err := index.Update("test-id", "new content", map[string]any{"new": true})
	if err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}

	item, err := index.Get("test-id")
	if err != nil {
		t.Fatalf("Get() returned error: %v", err)
	}

	if item.Content != "new content" {
		t.Errorf("Content = %v, want new content", item.Content)
	}

	if _, exists := item.Metadata["old"]; exists {
		t.Error("Old metadata should be removed")
	}

	if !item.Metadata["new"].(bool) {
		t.Error("New metadata should be true")
	}
}

func TestVectorIndex_Get_NonExistent(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)
	index, _ := NewVectorIndex(generator)

	_, err := index.Get("non-existent-id")
	if err == nil {
		t.Error("Get() should return error for non-existent ID")
	}
}

func TestVectorIndex_List(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)
	index, _ := NewVectorIndex(generator)

	items := []struct {
		ID       string
		Content  string
		Metadata map[string]any
	}{
		{"id1", "content1", nil},
		{"id2", "content2", nil},
		{"id3", "content3", nil},
	}

	index.AddBatch(items)

	list := index.List()
	if len(list) != 3 {
		t.Errorf("List() returned %v items, want 3", len(list))
	}
}

func TestVectorIndex_Clear(t *testing.T) {
	generator := NewMockEmbeddingGenerator(768)
	index, _ := NewVectorIndex(generator)

	items := []struct {
		ID       string
		Content  string
		Metadata map[string]any
	}{
		{"id1", "content1", nil},
		{"id2", "content2", nil},
	}

	index.AddBatch(items)

	index.Clear()

	if index.Size() != 0 {
		t.Errorf("After Clear(): Size() = %v, want 0", index.Size())
	}
}

func TestGenerateID(t *testing.T) {
	id1 := GenerateID("skill", "test-skill")
	id2 := GenerateID("skill", "test-skill")

	if id1 != id2 {
		t.Errorf("GenerateID() should return same ID for same input, got %v and %v", id1, id2)
	}

	id3 := GenerateID("skill", "different-skill")
	if id1 == id3 {
		t.Error("GenerateID() should return different IDs for different inputs")
	}
}

func TestSearchResult_String(t *testing.T) {
	item := &IndexedItem{
		ID:      "test-id",
		Content: "test content",
	}

	result := &SearchResult{
		Item:  item,
		Score: 0.85,
		Rank:  0,
	}

	str := result.String()
	if str == "" {
		t.Error("SearchResult.String() returned empty string")
	}
}

func TestEmbedding_EuclideanDistance(t *testing.T) {
	vector1 := make([]float32, 768)
	vector2 := make([]float32, 768)
	for i := range vector1 {
		vector1[i] = 0.0
		vector2[i] = 0.0
	}

	emb1, _ := NewEmbedding(vector1)
	emb2, _ := NewEmbedding(vector2)

	distance := emb1.EuclideanDistance(emb2)
	if distance != 0 {
		t.Errorf("EuclideanDistance() for identical vectors = %v, want 0", distance)
	}
}
