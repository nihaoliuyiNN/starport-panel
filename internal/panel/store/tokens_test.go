package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTokens(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if _, _, err := s.CreateToken(ctx, "  "); err == nil {
		t.Fatal("empty name should fail")
	}
	if n, _ := s.CountActiveTokens(ctx); n != 0 {
		t.Fatalf("count=%d", n)
	}
	tk, plain, err := s.CreateToken(ctx, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, TokenPrefix) || len(plain) < 40 || tk.Prefix != plain[:12] || tk.Revoked() {
		t.Fatalf("token: %+v plain=%q", tk, plain)
	}
	got, err := s.AuthenticateToken(ctx, plain)
	if err != nil || got.ID != tk.ID || got.Name != "ci" {
		t.Fatalf("auth: %+v %v", got, err)
	}
	// last_used_at 被刷新
	list, _ := s.ListTokens(ctx)
	if len(list) != 1 || list[0].LastUsedAt.IsZero() {
		t.Fatalf("list: %+v", list)
	}
	if _, err := s.AuthenticateToken(ctx, plain+"x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token: %v", err)
	}
	if n, _ := s.CountActiveTokens(ctx); n != 1 {
		t.Fatalf("count=%d", n)
	}

	// 吊销后不能再用；幂等；不存在报 NotFound
	if err := s.RevokeToken(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateToken(ctx, plain); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token should fail: %v", err)
	}
	if err := s.RevokeToken(ctx, tk.ID); err != nil {
		t.Fatalf("revoke twice: %v", err)
	}
	if err := s.RevokeToken(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke missing: %v", err)
	}
	if n, _ := s.CountActiveTokens(ctx); n != 0 {
		t.Fatalf("count=%d", n)
	}
	// 两次签发明文不同
	_, p2, _ := s.CreateToken(ctx, "ci")
	if p2 == plain {
		t.Fatal("tokens must be unique")
	}
}
