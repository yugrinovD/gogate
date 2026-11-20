// Package reqctx stores request-scoped values without importing HTTP packages.
package reqctx

import "context"

type requestIDKey struct{}
type identityKey struct{}

// Identity is the authenticated key/tenant pair.
type Identity struct {
	KeyID     string
	TenantID  string
	KeyPrefix string
	KeyName   string
}

// WithRequestID attaches a request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request id, if any.
func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return v
}

// WithIdentity attaches authentication identity.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the authenticated identity.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	v, ok := ctx.Value(identityKey{}).(Identity)
	return v, ok
}
