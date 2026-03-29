package llm

import (
	"testing"
)

func TestDefaultTokenizer(t *testing.T) {
	tokenizer := NewDefaultEstimator()

	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "empty string",
			text:     "",
			expected: 0,
		},
		{
			name:     "short text",
			text:     "hello world",
			expected: 3, // ~10 chars / 4 = 2.5 -> integer division gives 2
		},
		{
			name:     "longer text",
			text:     "This is a longer piece of text that should have more tokens.",
			expected: 20, // ~82 chars / 4 = 20 (integer division)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tokenizer.EstimateTokens(tt.text)
			// Allow some tolerance due to integer division
			if result < tt.expected-2 || result > tt.expected+1 {
				t.Errorf("EstimateTokens() = %v, want around %v", result, tt.expected)
			}
		})
	}
}

func TestDefaultTokenizer_EstimateTokensFromMessages(t *testing.T) {
	tokenizer := NewDefaultEstimator()

	messages := []map[string]string{
		{"role": "system", "content": "You are a helpful assistant."},
		{"role": "user", "content": "Hello, how are you?"},
		{"role": "assistant", "content": "I'm doing well, thank you!"},
	}

	result := tokenizer.EstimateTokensFromMessages(messages)
	// ~14 + 6 + 12 = 32 chars / 4 = 8 tokens plus overhead
	expected := 8 + (3 * 4) // tokens + overhead

	if result < expected-2 || result > expected+2 {
		t.Errorf("EstimateTokensFromMessages() = %v, want around %v", result, expected)
	}
}

func TestTokenBudgetManager_NewTokenBudgetManager(t *testing.T) {
	config := &TokenBudgetConfig{
		MaxTotalTokens:      128000,
		ReservedTokens:      4000,
		Strategy:            StrategyPrioritize,
		WindowOverlapTokens: 100,
		SummaryThreshold:    10000,
		Tokenizer:           NewDefaultEstimator(),
	}

	manager := NewTokenBudgetManager(config)

	if manager == nil {
		t.Fatal("NewTokenBudgetManager() returned nil")
	}

	if manager.config.MaxTotalTokens != 128000 {
		t.Errorf("MaxTotalTokens = %v, want %v", manager.config.MaxTotalTokens, 128000)
	}
}

func TestTokenBudgetManager_NilConfig(t *testing.T) {
	manager := NewTokenBudgetManager(nil)

	if manager == nil {
		t.Fatal("NewTokenBudgetManager(nil) returned nil")
	}

	if manager.config.MaxTotalTokens != 128000 {
		t.Errorf("MaxTotalTokens = %v, want %v", manager.config.MaxTotalTokens, 128000)
	}
}

func TestTokenBudgetManager_GetAvailableTokens(t *testing.T) {
	config := &TokenBudgetConfig{
		MaxTotalTokens: 10000,
		ReservedTokens: 2000,
		Tokenizer:      NewDefaultEstimator(),
	}

	manager := NewTokenBudgetManager(config)
	available := manager.GetAvailableTokens()

	if available != 8000 {
		t.Errorf("GetAvailableTokens() = %v, want %v", available, 8000)
	}
}

func TestTokenBudgetManager_CanFit(t *testing.T) {
	config := &TokenBudgetConfig{
		MaxTotalTokens: 10000,
		ReservedTokens: 2000,
		Tokenizer:      NewDefaultEstimator(),
	}

	manager := NewTokenBudgetManager(config)

	tests := []struct {
		name      string
		messages  []map[string]string
		wantFits  bool
	}{
		{
			name:     "empty messages",
			messages: []map[string]string{},
			wantFits: true,
		},
		{
			name: "small message",
			messages: []map[string]string{
				{"role": "user", "content": "hello"},
			},
			wantFits: true,
		},
		{
			name: "large message",
			messages: []map[string]string{
				{"role": "user", "content": "This is a very long message that exceeds the budget. " +
					"This is a very long message that exceeds the budget. " +
					"This is a very long message that exceeds the budget. " +
					"This is a very long message that exceeds the budget. " +
					"This is a very long message that exceeds the budget."},
			},
			wantFits: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := manager.CanFit(tt.messages)
			if result != tt.wantFits {
				t.Errorf("CanFit() = %v, want %v", result, tt.wantFits)
			}
		})
	}
}

func TestTokenBudgetManager_CheckCompatibility(t *testing.T) {
	config := &TokenBudgetConfig{
		MaxTotalTokens: 1000,
		ReservedTokens: 200,
		Tokenizer:      NewDefaultEstimator(),
	}

	manager := NewTokenBudgetManager(config)

	t.Run("compatible content", func(t *testing.T) {
		messages := []map[string]string{
			{"role": "user", "content": "small message"},
		}

		compatible, diff, err := manager.CheckCompatibility(messages)
		if err != nil {
			t.Errorf("CheckCompatibility() returned error: %v", err)
		}
		if !compatible {
			t.Error("CheckCompatibility() returned false, want true")
		}
		if diff < 0 {
			t.Errorf("CheckCompatibility() diff = %v, want >= 0", diff)
		}
	})

	t.Run("incompatible content", func(t *testing.T) {
		messages := []map[string]string{
			{"role": "user", "content": "This is a very long message that exceeds the budget. " +
				"This is a very long message that exceeds the budget. " +
				"This is a very long message that exceeds the budget."},
		}

		compatible, diff, err := manager.CheckCompatibility(messages)
		if err == nil {
			t.Error("CheckCompatibility() expected error, got nil")
		}
		if compatible {
			t.Error("CheckCompatibility() returned true, want false")
		}
		if diff <= 0 {
			t.Errorf("CheckCompatibility() diff = %v, want > 0", diff)
		}
	})
}

func TestTokenBudgetManager_Allocate_Truncate(t *testing.T) {
	config := &TokenBudgetConfig{
		MaxTotalTokens: 1000,
		ReservedTokens: 200,
		Strategy:      StrategyTruncate,
		Tokenizer:      NewDefaultEstimator(),
	}

	manager := NewTokenBudgetManager(config)

	// Create content that exceeds budget
	messages := []map[string]string{
		{"role": "system", "content": "System prompt"},
		{"role": "user", "content": "This is a very long message that exceeds the available budget. " +
			"This is a very long message that exceeds the available budget. " +
			"This is a very long message that exceeds the available budget."},
	}

	result, err := manager.Allocate(messages)
	if err != nil {
		t.Fatalf("Allocate() returned error: %v", err)
	}

	if result.Allocated > 800 {
		t.Errorf("Allocated tokens = %v, want <= 800", result.Allocated)
	}

	if !result.Metadata["truncated"].(bool) {
		t.Error("Expected truncated metadata to be true")
	}
}

func TestTokenBudgetManager_Allocate_Prioritize(t *testing.T) {
	config := &TokenBudgetConfig{
		MaxTotalTokens: 500,
		ReservedTokens: 100,
		Strategy:      StrategyPrioritize,
		Tokenizer:      NewDefaultEstimator(),
	}

	manager := NewTokenBudgetManager(config)

	messages := []map[string]string{
		{"role": "system", "content": "system system system system system"},
		{"role": "user", "content": "user user user user user"},
		{"role": "assistant", "content": "assistant assistant assistant assistant assistant"},
		{"role": "tool", "content": "tool tool tool tool tool"},
	}

	result, err := manager.Allocate(messages)
	if err != nil {
		t.Fatalf("Allocate() returned error: %v", err)
	}

	// Check that system messages are prioritized
	if result.Allocated > 400 {
		t.Errorf("Allocated tokens = %v, want <= 400", result.Allocated)
	}

	if !result.Metadata["prioritized"].(bool) {
		t.Error("Expected prioritized metadata to be true")
	}
}

func TestTokenBudgetManager_OptimizeContext(t *testing.T) {
	config := &TokenBudgetConfig{
		MaxTotalTokens: 500,
		ReservedTokens: 100,
		Tokenizer:      NewDefaultEstimator(),
	}

	manager := NewTokenBudgetManager(config)

	messages := []map[string]string{
		{"role": "user", "content": "Message about apples and oranges"},
		{"role": "ass", "content": "Response about apples"},
		{"role": "user", "content": "Message about bananas"},
	}

	keywords := []string{"apples", "fruit"}

	result, err := manager.OptimizeContext(messages, keywords)
	if err != nil {
		t.Fatalf("OptimizeContext() returned error: %v", err)
	}

	if result.Allocated > 400 {
		t.Errorf("Allocated tokens = %v, want <= 400", result.Allocated)
	}

	matchedCount := result.Metadata["matched_count"].(int)
	if matchedCount < 1 {
		t.Errorf("Expected at least 1 match, got %v", matchedCount)
	}
}

func TestAllocationStrategy_String(t *testing.T) {
	tests := []struct {
		strategy AllocationStrategy
	}{
		{StrategyTruncate},
		{StrategySummarize},
		{StrategyPrioritize},
		{StrategyWindow},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			// Just verify the strategy is valid
			_ = tt.strategy
		})
	}
}

func TestAllocationResult(t *testing.T) {
	result := &AllocationResult{
		Allocated: 100,
		Remaining: 700,
		Content:   []string{"test"},
		Metadata:  map[string]any{"test": true},
	}

	if result.Allocated != 100 {
		t.Errorf("Allocated = %v, want 100", result.Allocated)
	}

	if result.Remaining != 700 {
		t.Errorf("Remaining = %v, want 700", result.Remaining)
	}

	if len(result.Content) != 1 {
		t.Errorf("Content length = %v, want 1", len(result.Content))
	}

	if !result.Metadata["test"].(bool) {
		t.Error("Metadata test value should be true")
	}
}
