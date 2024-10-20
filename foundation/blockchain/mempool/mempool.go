// Package mempool maintains the mempool for the blockchain.
package mempool

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/hamidoujand/blockchain/foundation/blockchain/database"
	"github.com/hamidoujand/blockchain/foundation/blockchain/mempool/selector"
)

// Mempool represents a cache of transactions organized by account:nonce.
type Mempool struct {
	mu       sync.RWMutex
	pool     map[string]database.BlockTx
	selectFn selector.Func
}

// New constructs a new mempool using the default sort strategy.
func New() (*Mempool, error) {
	selector, err := selector.Retrieve(selector.StrategyTip)
	if err != nil {
		return nil, fmt.Errorf("selector reterive: %w", err)
	}

	mp := Mempool{
		pool:     make(map[string]database.BlockTx),
		selectFn: selector,
	}

	return &mp, nil
}

// Count returns the current number of transaction in the pool.
func (mp *Mempool) Count() int {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	return len(mp.pool)
}

// Upsert adds or replaces a transaction from the mempool.
func (mp *Mempool) Upsert(tx database.BlockTx) error {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	// CORE NOTE: Different blockchains have different algorithms to limit the
	// size of the mempool. Some limit based on the amount of memory being
	// consumed and some may limit based on the number of transaction. If a limit
	// is met, then either the transaction that has the least return on investment
	// or the oldest will be dropped from the pool to make room for new the transaction.

	key := fmt.Sprintf("%s:%d", tx.FromID, tx.Nonce) //unique key for updating a transaction in mempool

	// Ethereum requires a 10% bump in the tip to replace an existing
	// transaction in the mempool and so do we. We want to limit users
	// from this sort of behavior.
	if etx, exists := mp.pool[key]; exists {
		if tx.Tip < uint64(math.Round(float64(etx.Tip)*1.10)) {
			return errors.New("replacing a transaction requires a 10% bump in the tip")
		}
	}

	mp.pool[key] = tx

	return nil
}

// Delete removed a transaction from the mempool.
func (mp *Mempool) Delete(tx database.BlockTx) error {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	key := fmt.Sprintf("%s:%d", tx.FromID, tx.Nonce)

	delete(mp.pool, key)

	return nil
}

// Truncate clears all the transactions from the pool.
func (mp *Mempool) Truncate() {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	mp.pool = make(map[string]database.BlockTx)
}

// PickBest uses the configured sort strategy to return a set of transactions.
// If 0 is passed, all transactions in the mempool will be returned.
func (mp *Mempool) PickBest(howMany ...uint16) []database.BlockTx {
	number := 0
	if len(howMany) > 0 {
		number = int(howMany[0])
	}

	// CORE NOTE: Most blockchains do set a max block size limit and this size
	// will determined which transactions are selected. When picking the best
	// transactions for the next block, the Ardan blockchain is currently not
	// focused on block size but a max number of transactions.
	//
	// When the selection algorithm does need to consider sizing, picking the
	// right transactions that maximize profit gets really hard. On top of this,
	// today a miner gets a mining reward for each mined block. In the future
	// this could go away leaving just fees for the transactions that are
	// selected as the only form of revenue. This will change how transactions
	// need to be selected.

	// Copy all the transactions for each account into separate slices.
	m := make(map[database.AccountID][]database.BlockTx)
	mp.mu.RLock()
	{
		if number == 0 {
			number = len(mp.pool)
		}

		for key, tx := range mp.pool {
			account := database.AccountID(strings.Split(key, ":")[0]) // "id:nonce"
			m[account] = append(m[account], tx)
		}
	}
	mp.mu.RUnlock()

	// The selection algorithms is expecting this slice of transactions
	// organized by account.
	return mp.selectFn(m, number)
}
