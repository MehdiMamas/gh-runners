package localci

import (
	"context"

	"github.com/MehdiMamas/gh-runners/internal/proc"
)

type groupKey struct{}

func withGroup(ctx context.Context, g *proc.Group) context.Context {
	return context.WithValue(ctx, groupKey{}, g)
}

func groupFrom(ctx context.Context) *proc.Group {
	g, _ := ctx.Value(groupKey{}).(*proc.Group)
	return g
}
