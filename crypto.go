package goocp

import (
	"encoding/binary"

	"github.com/pkg/errors"
	"golang.org/x/crypto/salsa20"
)

var (
	ErrInvalidKey   = errors.New("invalid keys")
	ErrInvalidNonce = errors.New("invalid nonce")
)

func incrNonce(nonce []byte) {
	n := binary.BigEndian.Uint64(nonce[:])
	n++
	binary.BigEndian.PutUint64(nonce[:], n)
}

type PacketCryptoSalas struct {
	key   [32]byte
	nonce [8]byte
}

func NewPacketCryptoSalas() *PacketCryptoSalas {
	salas := &PacketCryptoSalas{}
	return salas
}

func (pcs *PacketCryptoSalas) SetKey(key []byte) error {
	if len(key) < 32 {
		return ErrInvalidKey
	}

	copy(pcs.key[:], key)
	return nil
}

func (pcs *PacketCryptoSalas) GetLocalNonce() []byte {
	return pcs.nonce[:]
}

func (pcs *PacketCryptoSalas) SetLocalNonce(nonce []byte) error {
	if len(nonce) < 8 {
		return ErrInvalidNonce
	}

	copy(pcs.nonce[:], nonce)
	return nil
}

func (pcs *PacketCryptoSalas) Encrypto(dst, src []byte) {
	salsa20.XORKeyStream(dst[8:], src[8:], pcs.nonce[:], &pcs.key)
	// write local encrypto nonce
	copy(dst[:8], pcs.nonce[:])
}

func (pcs *PacketCryptoSalas) Decrypto(dst, src []byte) {
	salsa20.XORKeyStream(dst[8:], src[8:], src[:8], &pcs.key)
}
