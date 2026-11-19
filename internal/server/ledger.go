package server

import (
	"context"

	"github.com/ejagombar/Glowworm/internal/core/ledger"
)

// book is the ledger as of today, shared with widget queries' cache.
func (a *App) book(ctx context.Context) (*ledger.Book, error) {
	return ledger.Cached(ctx, a.DB.DB, a.DB.DB, a.Clock.Today())
}
