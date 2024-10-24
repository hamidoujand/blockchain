package database

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/hamidoujand/blockchain/foundation/blockchain/genesis"
)

// Storage interface represents the behavior required to be implemented by any
// package providing support for reading and writing the blockchain.
type Storage interface {
	Write(blockData BlockData) error
	GetBlock(num uint64) (BlockData, error)
	ForEach() Iterator
	Close() error
	Reset() error
}

// Iterator interface represents the behavior required to be implemented by any
// package providing support to iterate over the blocks.
type Iterator interface {
	Next() (BlockData, error)
	Done() bool
}

// Database manages data related to accounts who have transacted on the blockchain.
type Database struct {
	mu          sync.RWMutex
	latestBlock Block //storing at the db level what is the last block we have on disk.
	genesis     genesis.Genesis
	accounts    map[AccountID]Account
	storage     Storage
}

// DatabaseIterator provides support for iterating over the blocks in the
// blockchain database using the configured storage option.
type DatabaseIterator struct {
	iterator Iterator
}

// Next retrieves the next block from disk.
func (di *DatabaseIterator) Next() (Block, error) {
	blockData, err := di.iterator.Next()
	if err != nil {
		return Block{}, err
	}

	return ToBlock(blockData)
}

// Done returns the end of chain value.
func (di *DatabaseIterator) Done() bool {
	return di.iterator.Done()
}

// New constructs a new database and applies account genesis information and
// reads/writes the blockchain database on disk if a dbPath is provided.
func New(genesis genesis.Genesis, storage Storage, evHandler func(v string, args ...any)) (*Database, error) {
	db := Database{
		genesis:  genesis,
		storage:  storage,
		accounts: make(map[AccountID]Account),
	}

	// Update the database with account balance information from genesis.
	for accountStr, balance := range genesis.Balances {
		accountID, err := ToAccountID(accountStr)
		if err != nil {
			return nil, err
		}
		db.accounts[accountID] = Account{
			AccountID: accountID,
			Balance:   balance,
		}
		evHandler("account: %s  balance: %d", accountStr, balance)
	}

	// Read all the blocks from storage.
	iter := db.ForEach()
	for block, err := iter.Next(); !iter.Done(); block, err = iter.Next() {
		if err != nil {
			return nil, err
		}

		// Validate the block values and cryptographic audit trail.
		if err := block.ValidateBlock(db.latestBlock, db.HashState(), evHandler); err != nil {
			return nil, err
		}

		// Update the database with the transaction information.
		for _, tx := range block.MerkleTree.Values() {
			db.ApplyTransaction(block, tx)
		}
		db.ApplyMiningReward(block)

		// Update the current latest block.
		db.latestBlock = block
	}

	return &db, nil
}

// Remove deletes an account from the database.
func (db *Database) Remove(accountID AccountID) {
	db.mu.Lock()
	defer db.mu.Unlock()

	delete(db.accounts, accountID)
}

// Query retrieves an account from the database.
func (db *Database) Query(accountID AccountID) (Account, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	acount, exists := db.accounts[accountID]
	if !exists {
		return Account{}, errors.New("account does not exist")
	}

	return acount, nil
}

// Copy makes a copy of the current accounts in the database.
func (db *Database) Copy() map[AccountID]Account {
	db.mu.RLock()
	defer db.mu.RUnlock()

	accounts := make(map[AccountID]Account)
	for accountID, account := range db.accounts {
		accounts[accountID] = account
	}
	return accounts
}

// LatestBlock returns the latest block.
func (db *Database) LatestBlock() Block {
	db.mu.RLock()
	defer db.mu.RUnlock()

	return db.latestBlock
}

// UpdateLatestBlock provides safe access to update the latest block.
func (db *Database) UpdateLatestBlock(block Block) {
	db.mu.Lock()
	defer db.mu.Unlock()

	db.latestBlock = block
}

// HashState returns a hash based on the contents of the accounts and
// their balances. This is added to each block and checked by peers.
func (db *Database) HashState() string {
	accounts := make([]Account, 0, len(db.accounts))
	db.mu.RLock()
	{
		for _, account := range db.accounts {
			accounts = append(accounts, account)
		}
	}
	db.mu.RUnlock()
	//we need them in the same order all the time.
	sort.Sort(byAccount(accounts))

	//hash them
	data, err := json.Marshal(accounts)
	if err != nil {
		return ZeroHash
	}
	hash := sha256.Sum256(data)
	return hexutil.Encode(hash[:])
}

// ApplyTransaction performs the business logic for applying a transaction
// to the database.
func (db *Database) ApplyTransaction(block Block, tx BlockTx) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	// Capture these accounts from the database.
	from, exists := db.accounts[tx.FromID]
	if !exists {
		from = newAccount(tx.FromID, 0)
	}

	to, exists := db.accounts[tx.ToID]
	if !exists {
		to = newAccount(tx.ToID, 0)
	}

	bnfc, exists := db.accounts[block.Header.BeneficiaryID]
	if !exists {
		bnfc = newAccount(block.Header.BeneficiaryID, 0)
	}

	// The account needs to pay the gas fee regardless. Take the
	// remaining balance if the account doesn't hold enough for the
	// full amount of gas. This is the only way to stop bad actors.
	gasFee := tx.GasPrice * tx.GasUnits
	if gasFee > from.Balance {
		gasFee = from.Balance
	}
	from.Balance -= gasFee
	bnfc.Balance += gasFee

	// Make sure these changes get applied.
	db.accounts[tx.FromID] = from
	db.accounts[block.Header.BeneficiaryID] = bnfc

	if tx.Nonce != (from.Nonce + 1) {
		return fmt.Errorf("transaction invalid, wrong nonce, got %d, exp %d", tx.Nonce, from.Nonce+1)
	}

	if from.Balance == 0 || from.Balance < (tx.Value+tx.Tip) {
		return fmt.Errorf("transaction invalid, insufficient funds, bal %d, needed %d", from.Balance, (tx.Value + tx.Tip))
	}

	// Update the balances between the two parties.
	from.Balance -= tx.Value
	to.Balance += tx.Value

	// Give the beneficiary the tip.
	from.Balance -= tx.Tip
	bnfc.Balance += tx.Tip

	// Update the nonce for the next transaction check.
	from.Nonce = tx.Nonce

	// Update the final changes to these accounts.
	db.accounts[tx.FromID] = from
	db.accounts[tx.ToID] = to
	db.accounts[block.Header.BeneficiaryID] = bnfc

	return nil
}

// ApplyMiningReward gives the specififed account the mining reward.
func (db *Database) ApplyMiningReward(block Block) {
	db.mu.Lock()
	defer db.mu.Unlock()

	account := db.accounts[block.Header.BeneficiaryID]
	account.Balance += block.Header.MiningReward

	db.accounts[block.Header.BeneficiaryID] = account
}

// ForEach returns an iterator to walk through all the blocks
// starting with block number 1.
func (db *Database) ForEach() DatabaseIterator {
	return DatabaseIterator{iterator: db.storage.ForEach()}
}

// Write adds a new block to the chain.
func (db *Database) Write(block Block) error {
	return db.storage.Write(NewBlockData(block))
}

// GetBlock searches the blockchain on disk to locate and return the
// contents of the specified block by number.
func (db *Database) GetBlock(num uint64) (Block, error) {
	blockData, err := db.storage.GetBlock(num)
	if err != nil {
		return Block{}, err
	}

	return ToBlock(blockData)
}
