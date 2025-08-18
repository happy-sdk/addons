// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package ipc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// Decrypt AES-GCM encrypted data
func Decrypt(key cipher.Block, data []byte) ([]byte, error) {
	aesGCM, err := cipher.NewGCM(key)
	if err != nil {
		return nil, err
	}

	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("request data too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// Encrypt data using AES-GCM
func Encrypt(key cipher.Block, data []byte) ([]byte, error) {
	aesGCM, err := cipher.NewGCM(key)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err

	}

	ciphertext := aesGCM.Seal(nonce, nonce, data, nil)
	return ciphertext, nil
}

func NewCipher(key string) (cipher.Block, error) {
	keyBytes := []byte(key)

	if len(keyBytes) != EncryptionKeyLenght {
		return nil, fmt.Errorf("encryption key validation failed: expected %d bytes, got %d",
			EncryptionKeyLenght, len(keyBytes))
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, err
	}
	return block, nil
}
