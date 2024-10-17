package signature

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/crypto"
)

// wolfID is an arbitrary number for signing messages. This will make it
// clear that the signature comes from the Wolf blockchain.
// Ethereum and Bitcoin do this as well, but they use the value of 27.
const wolfID = 29

// Sign uses the specified private key to sign the data.
func Sign(value any, privateKey *ecdsa.PrivateKey) (v, r, s *big.Int, err error) {
	//stamp the value
	// Prepare the data for signing.
	data, err := stamp(value)
	if err != nil {
		return nil, nil, nil, err
	}

	// Sign the hash with the private key to produce a signature.
	sig, err := crypto.Sign(data, privateKey)
	if err != nil {
		return nil, nil, nil, err
	}

	//turn into v r s format
	v, r, s = toSignatureValues(sig)
	return v, r, s, nil
}

// stamp returns a hash of 32 bytes that represents this data with
// the Wolf stamp embedded into the final hash.
func stamp(value any) ([]byte, error) {

	// Marshal the data.
	v, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	// This stamp is used so signatures we produce when signing data
	// are always unique to the Wolf blockchain.
	stamp := []byte(fmt.Sprintf("\x19Wolf Signed Message:\n%d", len(v)))

	// Hash the stamp and txHash together in a final 32 byte array
	// that represents the data.
	data := crypto.Keccak256(stamp, v)

	return data, nil
}

// toSignatureValues converts the signature into the r, s, v values.
func toSignatureValues(sig []byte) (v, r, s *big.Int) {
	r = big.NewInt(0).SetBytes(sig[:32])
	s = big.NewInt(0).SetBytes(sig[32:64])
	v = big.NewInt(0).SetBytes([]byte{sig[64] + wolfID}) // 0 + 29 OR 1 + 29 our own signature on the signature.

	return v, r, s
}
