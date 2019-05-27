package keystore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/iden3/go-iden3/crypto/babyjub"
	"github.com/iden3/go-iden3/crypto/mimc7"
	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
	"io"
	"io/ioutil"
	"math/big"
	"os"
	"runtime"
	"sync"
)

// Constants taken from
// https://github.com/ethereum/go-ethereum/blob/master/accounts/keystore/passphrase.go
const (
	// StandardScryptN is the N parameter of Scrypt encryption algorithm, using 256MB
	// memory and taking approximately 1s CPU time on a modern processor.
	StandardScryptN = 1 << 18

	// StandardScryptP is the P parameter of Scrypt encryption algorithm, using 256MB
	// memory and taking approximately 1s CPU time on a modern processor.
	StandardScryptP = 1

	// LightScryptN is the N parameter of Scrypt encryption algorithm, using 4MB
	// memory and taking approximately 100ms CPU time on a modern processor.
	LightScryptN = 1 << 12

	// LightScryptP is the P parameter of Scrypt encryption algorithm, using 4MB
	// memory and taking approximately 100ms CPU time on a modern processor.
	LightScryptP = 6

	scryptR     = 8
	scryptDKLen = 32
)

// KeyStoreParams are the Key Store parameters
type KeyStoreParams struct {
	ScryptN int
	ScryptP int
}

// LightKeyStoreParams are parameters for fast key derivation
var LightKeyStoreParams = KeyStoreParams{
	ScryptN: LightScryptN,
	ScryptP: LightScryptP,
}

// StandardKeyStoreParams are parameters for very secure derivation
var StandardKeyStoreParams = KeyStoreParams{
	ScryptN: StandardScryptN,
	ScryptP: StandardScryptP,
}

// Hex is a byte slice type that can be marshalled and unmarshaled in hex
type Hex []byte

// MarshalText encodes buf as hex
func (buf Hex) MarshalText() ([]byte, error) {
	return []byte(hex.EncodeToString(buf)), nil
}

// String encodes buf as hex
func (buf Hex) String() string {
	return hex.EncodeToString(buf)
}

// UnmarshalText decodes a hex into buf
func (buf *Hex) UnmarshalText(h []byte) error {
	*buf = make([]byte, hex.DecodedLen(len(h)))
	if _, err := hex.Decode(*buf, h); err != nil {
		return err
	}
	return nil
}

type hex32 [32]byte

func (buf hex32) MarshalText() ([]byte, error) {
	return []byte(hex.EncodeToString(buf[:])), nil
}

func (buf hex32) String() string {
	return hex.EncodeToString(buf[:])
}

func (buf *hex32) UnmarshalText(h []byte) error {
	if _, err := hex.Decode(buf[:], h); err != nil {
		return err
	}
	return nil
}

// EncryptedData contains the key derivation parameters and encryption
// parameters with the encrypted data.
type EncryptedData struct {
	Salt          Hex
	ScryptN       int
	ScryptP       int
	Nonce         Hex
	EncryptedData Hex
}

// EncryptedData encrypts data with a key derived from pass
func EncryptData(data, pass []byte, scryptN, scryptP int) (*EncryptedData, error) {
	var salt [32]byte
	if _, err := io.ReadFull(rand.Reader, salt[:]); err != nil {
		panic("reading from crypto/rand failed: " + err.Error())
	}
	derivedKey, err := scrypt.Key(pass, salt[:], scryptN, scryptR, scryptP, scryptDKLen)
	if err != nil {
		return nil, err
	}
	var key [32]byte
	copy(key[:], derivedKey)
	var nonce [24]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		panic("reading from crypto/rand failed: " + err.Error())
	}
	var encryptedData []byte
	encryptedData = secretbox.Seal(encryptedData, data, &nonce, &key)

	return &EncryptedData{
		Salt:          Hex(salt[:]),
		ScryptN:       scryptN,
		ScryptP:       scryptP,
		Nonce:         Hex(nonce[:]),
		EncryptedData: Hex(encryptedData),
	}, nil
}

// DecryptData decrypts the encData with the key derived from pass.
func DecryptData(encData *EncryptedData, pass []byte) ([]byte, error) {
	derivedKey, err := scrypt.Key(pass, encData.Salt[:],
		encData.ScryptN, scryptR, encData.ScryptP, scryptDKLen)
	if err != nil {
		return nil, err
	}
	var key [32]byte
	copy(key[:], derivedKey)
	var nonce [24]byte
	copy(nonce[:], encData.Nonce)
	var data []byte
	data, ok := secretbox.Open(data, encData.EncryptedData, &nonce, &key)
	if !ok {
		return nil, fmt.Errorf("Invalid encrypted data")
	}
	return data, nil
}

// KeysStored is the datastructure of stored keys in the storage.
type KeysStored map[hex32]EncryptedData

// Storage is an interface for a storage container.
type Storage interface {
	Read() ([]byte, error)
	Write(data []byte) error
	Lock() error
	Unlock() error
}

// FileStorage is a storage backed by a file.
type FileStorage struct {
	path string
	lock *flock.Flock
}

// NewFileStorage returns a new FileStorage backed by a file in path.
func NewFileStorage(path string) *FileStorage {
	return &FileStorage{path: path, lock: flock.New(path + ".lock")}
}

// Read reads the file contents.
func (fs *FileStorage) Read() ([]byte, error) {
	return ioutil.ReadFile(fs.path)
}

// Write writes the data to the file.
func (fs *FileStorage) Write(data []byte) error {
	return ioutil.WriteFile(fs.path, data, 0600)
}

// Locks the storage file with a .lock file.
func (fs *FileStorage) Lock() error {
	return fs.lock.Lock()
}

// Unlocks the storage file and removes the .lock file.
func (fs *FileStorage) Unlock() error {
	if err := fs.lock.Unlock(); err != nil {
		return err
	}
	return os.Remove(fs.path + ".lock")
}

// MemStorage is a storage backed by a slice.
type MemStorage []byte

// Read reads the slice contents.
func (ms *MemStorage) Read() ([]byte, error) {
	return []byte(*ms), nil
}

// Write copies the data to the slice.
func (ms *MemStorage) Write(data []byte) error {
	*ms = data
	return nil
}

// Lock does nothing.
func (ms *MemStorage) Lock() error { return nil }

// Unlock does nothing.
func (ms *MemStorage) Unlock() error { return nil }

// KeyStore is the object used to access create keys and sign with them.
type KeyStore struct {
	storage       Storage
	params        KeyStoreParams
	encryptedKeys KeysStored
	cache         map[hex32]*babyjub.PrivKey
	rw            sync.RWMutex
}

// NewKeyStore creates a new key store or opens it if it already exists.
func NewKeyStore(storage Storage, params KeyStoreParams) (*KeyStore, error) {
	if err := storage.Lock(); err != nil {
		return nil, err
	}
	encryptedKeysJSON, err := storage.Read()
	if os.IsNotExist(err) {
		encryptedKeysJSON = []byte{}
	} else if err != nil {
		storage.Unlock()
		return nil, err
	}
	var encryptedKeys KeysStored
	if len(encryptedKeysJSON) == 0 {
		encryptedKeys = make(map[hex32]EncryptedData)
	} else {
		if err := json.Unmarshal(encryptedKeysJSON, &encryptedKeys); err != nil {
			storage.Unlock()
			return nil, err
		}
	}
	ks := &KeyStore{
		storage:       storage,
		params:        params,
		encryptedKeys: encryptedKeys,
		cache:         make(map[hex32]*babyjub.PrivKey),
	}
	runtime.SetFinalizer(ks, func(ks *KeyStore) {
		// When there are no more references to the key store, clear
		// the secret keys in the cache and unlock the locked storage.
		zero := [32]byte{}
		for _, sk := range ks.cache {
			copy(sk[:], zero[:])
		}
		ks.storage.Unlock()
	})
	return ks, nil
}

// Keys returns the compressed public keys of the key storage.
func (ks *KeyStore) Keys() [][32]byte {
	ks.rw.RLock()
	defer ks.rw.RUnlock()
	keys := make([][32]byte, 0, len(ks.encryptedKeys))
	for pk, _ := range ks.encryptedKeys {
		keys = append(keys, pk)
	}
	return keys
}

// NewKey creates a new key in the key store encrypted with pass.
func (ks *KeyStore) NewKey(pass []byte) (*[32]byte, error) {
	sk := babyjub.NewRandPrivKey()
	return ks.ImportKey(sk, pass)
}

// ImportKey imports a secret key into the storage and encrypts it with pass.
func (ks *KeyStore) ImportKey(sk babyjub.PrivKey, pass []byte) (*[32]byte, error) {
	ks.rw.Lock()
	defer ks.rw.Unlock()
	encryptedKey, err := EncryptData(sk[:], pass, ks.params.ScryptN, ks.params.ScryptP)
	if err != nil {
		return nil, err
	}
	pk := sk.Pub()
	pubCompressed := (*babyjub.Point)(pk).Compress()
	ks.encryptedKeys[pubCompressed] = *encryptedKey
	encryptedKeysJSON, err := json.Marshal(ks.encryptedKeys)
	if err != nil {
		return nil, err
	}
	if err := ks.storage.Write(encryptedKeysJSON); err != nil {
		return nil, err
	}
	return &pubCompressed, nil
}

func (ks *KeyStore) ExportKey(pk *[32]byte, pass []byte) (*babyjub.PrivKey, error) {
	if err := ks.UnlockKey(pk, pass); err != nil {
		return nil, err
	}
	return ks.cache[hex32(*pk)], nil
}

// UnlockKey decrypts the key corresponding to the public key pk and loads it
// into the cache.
func (ks *KeyStore) UnlockKey(pk *[32]byte, pass []byte) error {
	ks.rw.Lock()
	defer ks.rw.Unlock()
	hexPk := hex32(*pk)
	encryptedKey, ok := ks.encryptedKeys[hexPk]
	if !ok {
		return fmt.Errorf("Public key not found in the key store")
	}
	skBuf, err := DecryptData(&encryptedKey, pass)
	if err != nil {
		return err
	}
	var sk babyjub.PrivKey
	copy(sk[:], skBuf)
	ks.cache[hexPk] = &sk
	return nil
}

// SignElem uses the key corresponding to the public key pk to sign the field
// element msg.
func (ks *KeyStore) SignElem(pk *[32]byte, msg mimc7.RElem) (*[64]byte, error) {
	ks.rw.RLock()
	defer ks.rw.RUnlock()
	hexPk := hex32(*pk)
	sk, ok := ks.cache[hexPk]
	if !ok {
		return nil, fmt.Errorf("Public key not found in the cache.  Is it unlocked?")
	}
	sig := sk.SignMimc7(msg)
	sigComp := sig.Compress()
	return &sigComp, nil
}

// mimc7HashBytes hashes a msg byte slice by blocks of 31 bytes encoded as
// little-endian.
func mimc7HashBytes(msg []byte) mimc7.RElem {
	n := 31
	msgElems := make([]mimc7.RElem, 0, len(msg)/n+1)
	for i := 0; i < len(msg)/n; i++ {
		v := new(big.Int)
		babyjub.SetBigIntFromLEBytes(v, msg[n*i:n*(i+1)])
		msgElems = append(msgElems, v)
	}
	if len(msg)%n != 0 {
		v := new(big.Int)
		babyjub.SetBigIntFromLEBytes(v, msg[len(msg)/n:])
		msgElems = append(msgElems, v)
	}
	return mimc7.Hash(msgElems, nil)
}

// Sign uses the key corresponding to the public key pk to sign the mimc7 hash
// of the msg byte slice.
func (ks *KeyStore) Sign(pk *[32]byte, msg []byte) (*[64]byte, error) {
	h := mimc7HashBytes(msg)
	return ks.SignElem(pk, h)
}

// VerifySignatureElem verifies that the signature sigComp of the field element
// msg was signed with the public key pkComp.
func VerifySignatureElem(pkComp *[32]byte, msg mimc7.RElem, sigComp *[64]byte) (bool, error) {
	pkPoint, err := babyjub.NewPoint().Decompress(*pkComp)
	if err != nil {
		return false, err
	}
	sig, err := new(babyjub.Signature).Decompress(*sigComp)
	if err != nil {
		return false, err
	}
	pk := babyjub.PubKey(*pkPoint)
	return pk.VerifyMimc7(msg, sig), nil
}

// VerifySignatureElem verifies that the signature sigComp of the mimc7 hash of
// the msg byte slice was signed with the public key pkComp.
func VerifySignature(pkComp *[32]byte, msg []byte, sigComp *[64]byte) (bool, error) {
	h := mimc7HashBytes(msg)
	return VerifySignatureElem(pkComp, h, sigComp)
}
