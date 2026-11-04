package identity

import (
	"context"

	"github.com/mdhuser/spendwise-biz/internal/identity/domain"
)

type principalKey struct{}

type Principal struct {
	User    domain.User
	Session domain.Session
}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	value, ok := ctx.Value(principalKey{}).(Principal)
	return value, ok
}
