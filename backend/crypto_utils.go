package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
)

var aesKey []byte

func init() {
	keyPath := getPath("config", "aes.key")
	data, err := os.ReadFile(keyPath)
	if err == nil && len(data) == 32 {
		aesKey = data
		return
	}

	oldKey := []byte("proxygw-secret-key-32-bytes-long")
	dbPath := getPath("config", "proxygw.db")
	if _, err := os.Stat(dbPath); err == nil {
		aesKey = oldKey
	} else {
		aesKey = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
			aesKey = oldKey
		}
	}
	os.WriteFile(keyPath, aesKey, 0600)
}

func EncryptAES(text string) string {
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return text
	}
	b := base64.StdEncoding.EncodeToString([]byte(text))
	ciphertext := make([]byte, aes.BlockSize+len(b))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return text
	}
	cfb := cipher.NewCFBEncrypter(block, iv)
	cfb.XORKeyStream(ciphertext[aes.BlockSize:], []byte(b))
	return "ENC:" + base64.StdEncoding.EncodeToString(ciphertext)
}

func DecryptAES(text string) string {
	if len(text) < 4 || text[:4] != "ENC:" {
		return text
	}
	text = text[4:]
	ciphertext, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(ciphertext) < aes.BlockSize {
		return text
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return text
	}
	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]
	cfb := cipher.NewCFBDecrypter(block, iv)
	cfb.XORKeyStream(ciphertext, ciphertext)
	data, err := base64.StdEncoding.DecodeString(string(ciphertext))
	if err != nil {
		return text
	}
	return string(data)
}
