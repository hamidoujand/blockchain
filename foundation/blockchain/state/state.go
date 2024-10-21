// Package state is the core API for the blockchain and implements all the
// business rules and processing.
package state

import (
	"sync"

	"github.com/hamidoujand/blockchain/foundation/blockchain/database"
	"github.com/hamidoujand/blockchain/foundation/blockchain/genesis"
	"github.com/hamidoujand/blockchain/foundation/blockchain/mempool"
)

// EventHandler defines a function that is called when events
// occur in the processing of persisting blocks.
type EventHandler func(v string, args ...any)

// Config represents the configuration required to start
// the blockchain node.
type Config struct {
	//account that receives mining rewards
	BeneficiaryID database.AccountID
	Genesis       genesis.Genesis
	EvHandler     EventHandler
}

// State manages the blockchain database.
type State struct {
	mu sync.RWMutex

	beneficiaryID database.AccountID
	evHandler     EventHandler

	genesis genesis.Genesis
	mempool *mempool.Mempool
	db      *database.Database
}

// New constructs a new blockchain for data management.
func New(conf Config) (*State, error) {
	// Build a safe event handler function for use.
	ev := func(v string, args ...any) {
		if conf.EvHandler != nil {
			conf.EvHandler(v, args...)
		}
	}

	// Access the storage for the blockchain.
	db, err := database.New(conf.Genesis, ev)
	if err != nil {
		return nil, err
	}

	// Construct a mempool with the specified sort strategy.
	mempool, err := mempool.New()
	if err != nil {
		return nil, err
	}

	// Create the State to provide support for managing the blockchain.
	state := State{
		beneficiaryID: conf.BeneficiaryID,
		evHandler:     ev,
		genesis:       conf.Genesis,
		mempool:       mempool,
		db:            db,
	}

	return &state, nil
}

// Shutdown cleanly brings the node down.
func (s *State) Shutdown() error {
	s.evHandler("state: shutdown: started")
	defer s.evHandler("state: shutdown: completed")
	return nil
}

// MempoolLength returns the current length of the mempool.
func (s *State) MempoolLength() int {
	return s.mempool.Count()
}

// Mempool returns a copy of the mempool.
func (s *State) Mempool() []database.BlockTx {
	return s.mempool.PickBest()
}

// UpsertMempool adds a new transaction to the mempool.
func (s *State) UpsertMempool(tx database.BlockTx) error {
	return s.mempool.Upsert(tx)
}

// Accounts returns a copy of the database accounts.
func (s *State) Accounts() map[database.AccountID]database.Account {
	return s.db.Copy()
}

// Genesis returns a copy of the genesis information.
func (s *State) Genesis() genesis.Genesis {
	return s.genesis
}

// QueryAccount returns a copy of the account from the database.
func (s *State) QueryAccount(account database.AccountID) (database.Account, error) {
	return s.db.Query(account)
}

// UpsertWalletTransaction accepts a transaction from a wallet for inclusion.
func (s *State) UpsertWalletTransaction(signedTx database.SignedTx) error {
	// CORE NOTE: It's up to the wallet to make sure the account has a proper
	// balance and this transaction has a proper nonce. Fees will be taken if
	// this transaction is mined into a block it doesn't have enough money to
	// pay or the nonce isn't the next expected nonce for the account.

	// Check the signed transaction has a proper signature, the from matches the
	// signature, and the from and to fields are properly formatted.
	if err := signedTx.Validate(s.genesis.ChainID); err != nil {
		return err
	}

	const oneUnitOfGas = 1
	tx := database.NewBlockTx(signedTx, s.genesis.GasPrice, oneUnitOfGas)
	if err := s.mempool.Upsert(tx); err != nil {
		return err
	}

	return nil
}
