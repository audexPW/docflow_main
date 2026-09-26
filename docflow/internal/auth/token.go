package auth

import (
	"fmt"
	"time"

	"docflow/internal/domain"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenManager(secret []byte, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: secret, ttl: ttl}
}

type Claims struct {
	Role domain.Role `json:"role"`
	jwt.RegisteredClaims
}

func (m *TokenManager) Issue(u domain.User) (string, time.Time, error) {
	expiresAt := time.Now().Add(m.ttl)
	claims := Claims{
		Role: u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID.String(),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

type Identity struct {
	UserID   uuid.UUID
	Role     domain.Role
	IssuedAt time.Time
}

func (m *TokenManager) Parse(tokenString string) (Identity, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return Identity{}, err
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return Identity{}, fmt.Errorf("invalid subject: %w", err)
	}
	var issuedAt time.Time
	if claims.IssuedAt != nil {
		issuedAt = claims.IssuedAt.Time
	}
	return Identity{UserID: id, Role: claims.Role, IssuedAt: issuedAt}, nil
}
