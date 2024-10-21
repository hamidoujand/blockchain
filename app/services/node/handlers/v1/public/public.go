// Package public maintains the group of handlers for public access.
package public

import (
	"context"
	"net/http"

	"github.com/hamidoujand/blockchain/foundation/blockchain/state"
	"github.com/hamidoujand/blockchain/foundation/web"
	"go.uber.org/zap"
)

// Handlers manages the set of bar ledger endpoints.
type Handlers struct {
	Log   *zap.SugaredLogger
	State *state.State
}

// Genesis returns the genesis information.
func (h Handlers) Genesis(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	gen := h.State.Genesis()
	return web.Respond(ctx, w, gen, http.StatusOK)
}
