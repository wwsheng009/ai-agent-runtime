package auth

import "testing"

// RFC 7636 附录 B 的官方测试向量。
func TestChallengeS256MatchesRFCVector(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := ChallengeS256(verifier); got != want {
		t.Fatalf("ChallengeS256 = %q, want %q", got, want)
	}
}

func TestNewPKCEProducesS256AndUniqueVerifiers(t *testing.T) {
	first, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	if first.Method != "S256" {
		t.Fatalf("method = %q", first.Method)
	}
	if len(first.Verifier) < 43 || len(first.Verifier) > 128 {
		t.Fatalf("verifier length = %d（RFC 7636 要求 43-128）", len(first.Verifier))
	}
	if first.Challenge != ChallengeS256(first.Verifier) {
		t.Fatal("challenge 与 verifier 不匹配")
	}
	second, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	if first.Verifier == second.Verifier {
		t.Fatal("两次生成的 verifier 不应相同")
	}
}

func TestNewStateIsUnique(t *testing.T) {
	a, err := NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	b, err := NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if a == "" || a == b {
		t.Fatalf("state 应非空且互不相同: %q %q", a, b)
	}
}
