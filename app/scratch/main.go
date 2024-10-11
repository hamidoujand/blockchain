package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// Tx is the transactional information between two parties.
type Tx struct {
	FromID string `json:"from"`
	ToID   string `json:"to"`
	Value  uint64 `json:"value"`
}

func main() {
	if err := run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func run() error {

	tx := Tx{
		FromID: "Hamid",
		ToID:   "Wolf",
		Value:  10,
	}
	private, err := crypto.LoadECDSA("zblock/accounts/kennedy.ecdsa")
	if err != nil {
		return fmt.Errorf("load ECDSA key: %w", err)
	}

	data, err := json.Marshal(tx)
	if err != nil {
		return fmt.Errorf("json: %w", err)
	}

	digest := crypto.Keccak256(data) //stamp it

	sig, err := crypto.Sign(digest, private)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	hex := hexutil.Encode(sig)
	fmt.Printf("%s\n", hex)
	return nil
}
