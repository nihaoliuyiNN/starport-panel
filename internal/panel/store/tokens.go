package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TokenPrefix API Token 明文前缀，便于在日志 / 配置里一眼识别。
const TokenPrefix = "spt_"

// APIToken API 访问令牌（不含明文；明文只在创建时返回一次）。
type APIToken struct {
	ID         uint64    `json:"id"`
	Name       string    `json:"name"`
	Prefix     string    `json:"prefix"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  time.Time `json:"revokedAt,omitempty"`
}

// Revoked 是否已吊销。
func (t APIToken) Revoked() bool { return !t.RevokedAt.IsZero() }

// CreateToken 生成并保存一个令牌，返回记录与明文（仅此一次）。
func (s *Store) CreateToken(ctx context.Context, name string) (APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIToken{}, "", errors.New("store: token name 为空")
	}
	raw := make([]byte, 30)
	if _, err := rand.Read(raw); err != nil {
		return APIToken{}, "", err
	}
	plain := TokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	prefix := plain[:12]
	created := time.Now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens(name, prefix, hash, created_at) VALUES(?,?,?,?)`,
		name, prefix, HashToken(plain), ts(created))
	if err != nil {
		return APIToken{}, "", err
	}
	id, _ := res.LastInsertId()
	return APIToken{ID: uint64(id), Name: name, Prefix: prefix, CreatedAt: created}, plain, nil
}

// HashToken 明文 → 存储用哈希。
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// AuthenticateToken 按明文校验：存在且未吊销返回记录并刷新 last_used_at（最多每分钟写一次）；否则 ErrNotFound。
func (s *Store) AuthenticateToken(ctx context.Context, plain string) (APIToken, error) {
	t, err := s.scanToken(s.db.QueryRowContext(ctx,
		`SELECT id, name, prefix, created_at, last_used_at, revoked_at FROM api_tokens WHERE hash = ?`, HashToken(plain)))
	if err != nil {
		return APIToken{}, err
	}
	if t.Revoked() {
		return APIToken{}, ErrNotFound
	}
	if time.Since(t.LastUsedAt) > time.Minute {
		_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now(), t.ID)
	}
	return t, nil
}

// ListTokens 全部令牌（含已吊销），按创建倒序。
func (s *Store) ListTokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, prefix, created_at, last_used_at, revoked_at FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := s.scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken 吊销；不存在 ErrNotFound，已吊销幂等。
func (s *Store) RevokeToken(ctx context.Context, id uint64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = CASE WHEN revoked_at = '' THEN ? ELSE revoked_at END WHERE id = ?`, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountActiveTokens 未吊销令牌数（用于判断面板是否已配置访问凭据）。
func (s *Store) CountActiveTokens(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE revoked_at = ''`).Scan(&n)
	return n, err
}

type rowScanner interface{ Scan(dest ...any) error }

func (s *Store) scanToken(r rowScanner) (APIToken, error) {
	var t APIToken
	var created, used, revoked string
	if err := r.Scan(&t.ID, &t.Name, &t.Prefix, &created, &used, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIToken{}, ErrNotFound
		}
		return APIToken{}, fmt.Errorf("store: 读 token: %w", err)
	}
	t.CreatedAt, t.LastUsedAt, t.RevokedAt = parseTS(created), parseTS(used), parseTS(revoked)
	return t, nil
}
