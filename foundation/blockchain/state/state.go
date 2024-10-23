// Package state is the core API for the blockchain and implements all the
// business rules and processing.
package state

import (
	"context"
	"errors"
	"sync"

	"github.com/hamidoujand/blockchain/foundation/blockchain/database"
	"github.com/hamidoujand/blockchain/foundation/blockchain/genesis"
	"github.com/hamidoujand/blockchain/foundation/blockchain/mempool"
)

// ErrNoTransactions is returned when a block is requested to be created
// and there are not enough transactions.
var ErrNoTransactions = errors.New("no transactions in mempool")

// EventHandler defines a function that is called when events
// occur in the processing of persisting blocks.
type EventHandler func(v string, args ...any)

// Worker interface represents the behavior required to be implemented by any
// package providing support for mining, peer updates, and transaction sharing.
type Worker interface {
	Shutdown()
	SignalStartMining()
	SignalCancelMining()
}

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

	Worker Worker
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
	// The Worker is not set here. The call to worker.Run will assign itself
	// and start everything up and running for the node.
	return &state, nil
}

// Shutdown cleanly brings the node down.
func (s *State) Shutdown() error {
	s.evHandler("state: shutdown: started")
	defer s.evHandler("state: shutdown: completed")

	// Make sure the database file is properly closed.
	// defer func() {
	// 	s.db.Close()
	// }()

	// Stop all blockchain writing activity.
	s.Worker.Shutdown()

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

	s.Worker.SignalStartMining()

	return nil
}

// MineNewBlock attempts to create a new block with a proper hash that can become
// the next block in the chain.
func (s *State) MineNewBlock(ctx context.Context) (database.Block, error) {
	defer s.evHandler("viewer: MineNewBlock: MINING: completed")

	s.evHandler("state: MineNewBlock: MINING: check mempool count")

	// Are there enough transactions in the pool.
	if s.mempool.Count() == 0 {
		return database.Block{}, ErrNoTransactions
	}

	// Pick the best transactions from the mempool.
	trans := s.mempool.PickBest(s.genesis.TransPerBlock)

	difficulty := s.genesis.Difficulty

	// Attempt to create a new block by solving the POW puzzle. This can be cancelled.
	block, err := database.POW(ctx, database.POWArgs{
		BeneficiaryID: s.beneficiaryID,
		Difficulty:    difficulty,
		MiningReward:  s.genesis.MiningReward,
		PrevBlock:     s.db.LatestBlock(),
		StateRoot:     s.db.HashState(),
		Trans:         trans,
		EvHandler:     s.evHandler,
	})
	if err != nil {
		return database.Block{}, err
	}

	s.evHandler("state: MineNewBlock: MINING: validate and update database")

	// Validate the block and then update the blockchain database.
	if err := s.validateUpdateDatabase(block); err != nil {
		return database.Block{}, err
	}

	return block, nil
}

// validateUpdateDatabase takes the block and validates the block against the
// consensus rules. If the block passes, then the state of the node is updated
// including adding the block to disk.
func (s *State) validateUpdateDatabase(block database.Block) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.evHandler("state: validateUpdateDatabase: validate block")

	// CORE NOTE: I could add logic to determine if this block was mined by this
	// node or a peer. If the block is mined by this node, even if a peer beat
	// me to this function for the same block number, I could replace the peer
	// block with my own and attempt to have other peers accept my block instead.

	if err := block.ValidateBlock(s.db.LatestBlock(), s.db.HashState(), s.evHandler); err != nil {
		return err
	}

	s.evHandler("state: validateUpdateDatabase: write to disk")

	// // Write the new block to the chain on disk.
	// if err := s.db.Write(block); err != nil {
	// 	return err
	// }
	s.db.UpdateLatestBlock(block)

	s.evHandler("state: validateUpdateDatabase: update accounts and remove from mempool")

	// Process the transactions and update the accounts.
	for _, tx := range block.MerkleTree.Values() {
		s.evHandler("state: validateUpdateDatabase: tx[%s] update and remove", tx)

		// Remove this transaction from the mempool.
		s.mempool.Delete(tx)

		// Apply the balance changes based on this transaction.
		if err := s.db.ApplyTransaction(block, tx); err != nil {
			s.evHandler("state: validateUpdateDatabase: WARNING : %s", err)
			continue
		}
	}

	// Send an event about this new block.
	// s.blockEvent(block)

	return nil
}

// LatestBlock returns a copy the current latest block.
func (s *State) LatestBlock() database.Block {
	return s.db.LatestBlock()
}
