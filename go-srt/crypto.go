package srt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"errors"
)

// Key material (KM) message fields.
const (
	kmSign       = 0x2029 // "HAI" in PnP Vendor ID big endian
	kmCipherCTR  = 2
	kmSEMpegTS   = 2
	kmSaltLen    = 16
	kekIteration = 2048
)

// KM response states sent back instead of the key material.
const (
	kmStateBadSecret = 4
	kmStateNoSecret  = 3
)

var (
	errBadSecret = errors.New("srt: wrong passphrase")
	errBadKM     = errors.New("srt: malformed key material")
)

// cryptoCtx holds the stream encrypting keys (SEK) of a connection. Both
// directions share them. Keys are even (KK=1) or odd (KK=2); a sender
// switches from one to the other to refresh keys without a gap.
type cryptoCtx struct {
	passphrase string
	keyLen     int
	salt       []byte
	keys       [2]cipher.Block // even, odd
	raw        [2][]byte
}

// newCryptoCtx makes fresh key material for the side that starts the
// exchange (the caller).
func newCryptoCtx(passphrase string, keyLen int) (*cryptoCtx, error) {
	c := &cryptoCtx{passphrase: passphrase, keyLen: keyLen, salt: make([]byte, kmSaltLen)}
	if _, err := rand.Read(c.salt); err != nil {
		return nil, err
	}
	sek := make([]byte, keyLen)
	if _, err := rand.Read(sek); err != nil {
		return nil, err
	}
	return c, c.setKey(0, sek)
}

func (c *cryptoCtx) setKey(i int, sek []byte) error {
	block, err := aes.NewCipher(sek)
	if err != nil {
		return err
	}
	c.keys[i], c.raw[i] = block, sek
	return nil
}

// kek derives the key encrypting key from the passphrase and the low 64
// bits of the salt, as libsrt does.
func (c *cryptoCtx) kek() ([]byte, error) {
	return pbkdf2.Key(sha1.New, c.passphrase, c.salt[kmSaltLen-8:], kekIteration, c.keyLen)
}

// marshalKM builds the KM message announcing the even key.
func (c *cryptoCtx) marshalKM() ([]byte, error) {
	kek, err := c.kek()
	if err != nil {
		return nil, err
	}
	wrapped, err := keyWrap(kek, c.raw[0])
	if err != nil {
		return nil, err
	}
	b := make([]byte, 16, 16+kmSaltLen+len(wrapped))
	b[0] = 0x12 // S=0, V=1, PT=2 (KM message)
	binary.BigEndian.PutUint16(b[1:], kmSign)
	b[3] = 1 // KK: even key
	b[8] = kmCipherCTR
	b[10] = kmSEMpegTS
	b[14] = kmSaltLen / 4
	b[15] = byte(c.keyLen / 4)
	b = append(b, c.salt...)
	return append(b, wrapped...), nil
}

// applyKM unwraps a KM message with the passphrase and installs the keys
// it carries. keyLen is taken from the message, since the peer chose it.
func (c *cryptoCtx) applyKM(b []byte) error {
	if len(b) < 16 || b[0] != 0x12 || binary.BigEndian.Uint16(b[1:]) != kmSign || b[8] != kmCipherCTR {
		return errBadKM
	}
	kk := int(b[3] & 0x03)
	saltLen, keyLen := int(b[14])*4, int(b[15])*4
	if kk == 0 || saltLen != kmSaltLen || (keyLen != 16 && keyLen != 24 && keyLen != 32) {
		return errBadKM
	}
	n := 1
	if kk == 3 {
		n = 2
	}
	if len(b) < 16+saltLen+n*keyLen+8 {
		return errBadKM
	}
	c.salt = append([]byte(nil), b[16:16+saltLen]...)
	c.keyLen = keyLen
	kek, err := c.kek()
	if err != nil {
		return err
	}
	keys, err := keyUnwrap(kek, b[16+saltLen:16+saltLen+n*keyLen+8])
	if err != nil {
		return errBadSecret
	}
	switch kk {
	case 1:
		return c.setKey(0, keys)
	case 2:
		return c.setKey(1, keys)
	default:
		if err := c.setKey(0, keys[:keyLen]); err != nil {
			return err
		}
		return c.setKey(1, keys[keyLen:])
	}
}

// xorPayload encrypts or decrypts a data packet payload in place: AES-CTR
// with the counter block made of the salt, the packet sequence number and
// a 16 bit block counter.
func (c *cryptoCtx) xorPayload(keyFlags byte, seq uint32, payload []byte) error {
	idx := int(keyFlags) - 1
	if idx < 0 || idx > 1 || c.keys[idx] == nil {
		return errors.New("srt: packet encrypted with a key we do not have")
	}
	var iv [16]byte
	binary.BigEndian.PutUint32(iv[10:], seq)
	for i := 0; i < 14; i++ {
		iv[i] ^= c.salt[i]
	}
	cipher.NewCTR(c.keys[idx], iv[:]).XORKeyStream(payload, payload)
	return nil
}

var keyWrapIV = [8]byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}

// keyWrap is the AES key wrap of RFC 3394.
func keyWrap(kek, plain []byte) ([]byte, error) {
	if len(plain)%8 != 0 || len(plain) < 16 {
		return nil, errors.New("srt: key to wrap must be a multiple of 8 bytes")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := len(plain) / 8
	out := make([]byte, 8+len(plain))
	copy(out[8:], plain)
	a := keyWrapIV
	var buf [16]byte
	for j := 0; j < 6; j++ {
		for i := 1; i <= n; i++ {
			copy(buf[:8], a[:])
			copy(buf[8:], out[8*i:8*i+8])
			block.Encrypt(buf[:], buf[:])
			t := uint64(n*j + i)
			binary.BigEndian.PutUint64(a[:], binary.BigEndian.Uint64(buf[:8])^t)
			copy(out[8*i:], buf[8:])
		}
	}
	copy(out, a[:])
	return out, nil
}

// keyUnwrap undoes keyWrap, failing when the integrity check value does
// not come out, which is what a wrong passphrase looks like.
func keyUnwrap(kek, wrapped []byte) ([]byte, error) {
	if len(wrapped)%8 != 0 || len(wrapped) < 24 {
		return nil, errBadKM
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := len(wrapped)/8 - 1
	out := make([]byte, len(wrapped)-8)
	copy(out, wrapped[8:])
	var a [8]byte
	copy(a[:], wrapped[:8])
	var buf [16]byte
	for j := 5; j >= 0; j-- {
		for i := n; i >= 1; i-- {
			t := uint64(n*j + i)
			binary.BigEndian.PutUint64(buf[:8], binary.BigEndian.Uint64(a[:])^t)
			copy(buf[8:], out[8*(i-1):8*i])
			block.Decrypt(buf[:], buf[:])
			copy(a[:], buf[:8])
			copy(out[8*(i-1):], buf[8:])
		}
	}
	if subtle.ConstantTimeCompare(a[:], keyWrapIV[:]) != 1 {
		return nil, errBadSecret
	}
	return out, nil
}
