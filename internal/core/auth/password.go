package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidHash    = errors.New("auth: password hash is malformed")
	ErrWrongAlgorithm = errors.New("auth: unsupported password hash algorithm")
)

type HashParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

func DefaultHashParams() HashParams {
	parallelism := uint8(1)
	if cores := runtime.NumCPU(); cores > 1 {
		parallelism = 2
	}
	return HashParams{
		Memory:      19456,
		Iterations:  2,
		Parallelism: parallelism,
		SaltLength:  16,
		KeyLength:   32,
	}
}

func HashPassword(password string, params HashParams) (string, error) {
	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt,
		params.Iterations, params.Memory, params.Parallelism, params.KeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, params.Memory, params.Iterations, params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func VerifyPassword(encoded, password string) (bool, error) {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(password), salt,
		params.Iterations, params.Memory, params.Parallelism, params.KeyLength)

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func decodeHash(encoded string) (HashParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return HashParams{}, nil, nil, ErrWrongAlgorithm
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return HashParams{}, nil, nil, ErrWrongAlgorithm
	}

	var params HashParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d",
		&params.Memory, &params.Iterations, &params.Parallelism); err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(key))

	return params, salt, key, nil
}

var decoyHash string

func init() {
	hash, err := HashPassword("tavora-decoy", DefaultHashParams())
	if err != nil {
		panic(err)
	}
	decoyHash = hash
}

func burnTime(password string) {
	_, _ = VerifyPassword(decoyHash, password)
}
