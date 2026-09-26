package filestore

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Store хранит файлы на диске в зашифрованном виде. Каждый файл шифруется
// AES-256-GCM, nonce пишется в начало файла.
type Store struct {
	root string
	gcm  cipher.AEAD
}

func New(root string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	return &Store{root: root, gcm: gcm}, nil
}

type SaveResult struct {
	Key    string
	SHA256 string
	Size   int64
}

// Save читает исходный поток, считает sha256 от открытого содержимого и
// сохраняет зашифрованную копию. Возвращает ключ для последующего доступа.
func (s *Store) Save(r io.Reader) (SaveResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return SaveResult{}, err
	}
	sum := sha256.Sum256(data)

	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return SaveResult{}, err
	}
	ciphertext := s.gcm.Seal(nonce, nonce, data, nil)

	key := relKey()
	full := filepath.Join(s.root, key)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return SaveResult{}, err
	}
	if err := os.WriteFile(full, ciphertext, 0o640); err != nil {
		return SaveResult{}, err
	}

	return SaveResult{
		Key:    key,
		SHA256: hex.EncodeToString(sum[:]),
		Size:   int64(len(data)),
	}, nil
}

func (s *Store) Open(key string) (io.ReadCloser, error) {
	data, err := s.ReadAll(key)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *Store) ReadAll(key string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(s.root, key))
	if err != nil {
		return nil, err
	}
	ns := s.gcm.NonceSize()
	if len(raw) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := raw[:ns], raw[ns:]
	plaintext, err := s.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

// Remove удаляет файл из хранилища. Отсутствие файла ошибкой не считаем:
// уборка по сроку хранения не должна останавливаться из-за документа, файл
// которого уже был удалён вручную или потерян при переносе стенда.
func (s *Store) Remove(key string) error {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	// Ключ приходит из базы, но защититься от выхода за корень хранилища
	// дешевле, чем разбираться потом, что удалилось.
	path := filepath.Join(s.root, filepath.Clean("/"+key))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ExtractTo расшифровывает файл во временную директорию и возвращает путь.
// Используется распознавателем, которому нужен файл на диске (tesseract и т.п.).
func (s *Store) ExtractTo(key, dir, filename string) (string, error) {
	data, err := s.ReadAll(key)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return "", err
	}
	return path, nil
}

func relKey() string {
	now := time.Now().UTC()
	return filepath.Join(
		fmt.Sprintf("%04d", now.Year()),
		fmt.Sprintf("%02d", now.Month()),
		uuid.NewString()+".enc",
	)
}
