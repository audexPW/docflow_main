package main

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"docflow/internal/config"
	"docflow/internal/filestore"

	_ "github.com/lib/pq"
)

type Meta struct {
	ID           string `json:"id"`
	OriginalName string `json:"original_name"`
	DocType      string `json:"doc_type"`
	StorageKey   string `json:"storage_key"`
	Status       string `json:"status"`
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}

	store, err := filestore.New(cfg.Files.Dir, cfg.Files.Key)
	if err != nil {
		panic(err)
	}

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		panic(err)
	}

	input := "/opt/docflow-deploy/golden_selection.csv"
	output := "/opt/docflow-deploy/golden_dataset"

	if err := os.MkdirAll(filepath.Join(output, "documents"), 0755); err != nil {
		panic(err)
	}

	f, err := os.Open(input)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	reader := csv.NewReader(f)

	records, err := reader.ReadAll()
	if err != nil {
		panic(err)
	}

	for i, row := range records[1:] {
		if len(row) < 6 {
			continue
		}

		id := row[0]
		name := row[1]
		docType := row[2]
		storageKey := row[3]
		status := row[4]

		dir := filepath.Join(
			output,
			"documents",
			fmt.Sprintf("%03d", i+1),
		)

		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(err)
		}

		// Сохраняем оригинальный документ
		data, err := store.ReadAll(storageKey)
		if err != nil {
			fmt.Printf("ERROR decrypt %s: %v\n", id, err)
			continue
		}

		if err := os.WriteFile(
			filepath.Join(dir, name),
			data,
			0644,
		); err != nil {
			panic(err)
		}

		// Сохраняем метаданные
		meta := Meta{
			ID:           id,
			OriginalName: name,
			DocType:      docType,
			StorageKey:   storageKey,
			Status:       status,
		}

		metaJSON, err := json.MarshalIndent(meta, "", "  ")
		if err != nil {
			panic(err)
		}

		if err := os.WriteFile(
			filepath.Join(dir, "metadata.json"),
			metaJSON,
			0644,
		); err != nil {
			panic(err)
		}

		// Сохраняем текущий результат DocFlow
		var recognition json.RawMessage

		err = db.QueryRow(
			`SELECT recognition FROM documents WHERE id = $1`,
			id,
		).Scan(&recognition)

		if err != nil {
			fmt.Printf(
				"WARN recognition %s: %v\n",
				id,
				err,
			)
		} else {
			if err := os.WriteFile(
				filepath.Join(dir, "docflow_result.json"),
				recognition,
				0644,
			); err != nil {
				panic(err)
			}
		}

		fmt.Printf(
			"OK %d %s\n",
			i+1,
			name,
		)
	}

	fmt.Println("done")
}
