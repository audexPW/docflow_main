// backupcheck проверяет, что из резервной копии действительно можно
// восстановиться (ТЗ §7: «резервные копии … с проверкой, что из них реально
// можно восстановиться»).
//
// Инструмент запускается против ВОССТАНОВЛЕННОЙ копии, а не против боевого
// стенда, и проверяет три вещи:
//
//  1. база поднялась, схема на месте, ключевые таблицы читаются;
//  2. для каждого документа файл лежит в хранилище;
//  3. файл расшифровывается ключом FILES_ENCRYPTION_KEY и его sha256
//     совпадает с тем, что записан в базе.
//
// Третий пункт — главный: он ловит и битый архив, и — что важнее —
// восстановление файлов вместе с чужим/устаревшим ключом шифрования, когда
// формально всё «восстановилось», а документы не читаются.
//
// Использование (см. verify-backup.sh):
//
//	backupcheck                # проверить всё
//	backupcheck -sample 50     # выборочно, 50 документов
//
// Переменные окружения: DATABASE_URL, FILES_DIR, FILES_ENCRYPTION_KEY.
package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"docflow/internal/filestore"

	_ "github.com/lib/pq"
)

func main() {
	sample := flag.Int("sample", 0, "проверить не больше N документов (0 — все)")
	timeout := flag.Duration("timeout", 2*time.Minute, "таймаут ожидания базы")
	flag.Parse()

	if err := run(*sample, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "ПРОВЕРКА НЕ ПРОЙДЕНА: %v\n", err)
		os.Exit(1)
	}
}

func run(sample int, timeout time.Duration) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("не задан DATABASE_URL")
	}
	filesDir := os.Getenv("FILES_DIR")
	if filesDir == "" {
		filesDir = "/var/lib/docflow/files"
	}
	keyHex := strings.TrimSpace(os.Getenv("FILES_ENCRYPTION_KEY"))
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return fmt.Errorf("FILES_ENCRYPTION_KEY должен быть 32 байта в hex (64 символа)")
	}

	store, err := filestore.New(filesDir, key)
	if err != nil {
		return fmt.Errorf("хранилище файлов: %w", err)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := waitForDB(db, timeout); err != nil {
		return err
	}

	// 1. Схема на месте.
	for _, table := range []string{"users", "documents", "export_jobs", "audit_log", "device_tokens"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			return fmt.Errorf("таблица %s не читается: %w", table, err)
		}
		fmt.Printf("  таблица %-14s записей: %d\n", table, n)
	}

	var users int
	if err := db.QueryRow(`SELECT count(*) FROM users`).Scan(&users); err != nil {
		return err
	}
	if users == 0 {
		return fmt.Errorf("в восстановленной базе нет ни одного пользователя — похоже, дамп пустой")
	}

	// 2–3. Файлы на месте, расшифровываются, контрольные суммы сходятся.
	q := `SELECT id, storage_key, sha256, size_bytes FROM documents ORDER BY created_at`
	if sample > 0 {
		q = fmt.Sprintf(`SELECT id, storage_key, sha256, size_bytes FROM documents ORDER BY random() LIMIT %d`, sample)
	}
	rows, err := db.Query(q)
	if err != nil {
		return err
	}
	defer rows.Close()

	var checked, missing, corrupt int
	var problems []string
	for rows.Next() {
		var id, storageKey, wantSum string
		var size int64
		if err := rows.Scan(&id, &storageKey, &wantSum, &size); err != nil {
			return err
		}
		checked++

		if _, err := os.Stat(filepath.Join(filesDir, storageKey)); err != nil {
			missing++
			problems = append(problems, fmt.Sprintf("документ %s: файла нет в хранилище (%s)", id, storageKey))
			continue
		}

		data, err := store.ReadAll(storageKey)
		if err != nil {
			corrupt++
			problems = append(problems, fmt.Sprintf("документ %s: не расшифровывается (%v)", id, err))
			continue
		}

		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != wantSum {
			corrupt++
			problems = append(problems, fmt.Sprintf("документ %s: sha256 не совпадает (в базе %s, в файле %s)", id, wantSum, got))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	fmt.Printf("  проверено документов: %d, отсутствует файлов: %d, повреждено: %d\n", checked, missing, corrupt)
	if len(problems) > 0 {
		limit := len(problems)
		if limit > 10 {
			limit = 10
		}
		for _, p := range problems[:limit] {
			fmt.Fprintf(os.Stderr, "  ! %s\n", p)
		}
		if len(problems) > limit {
			fmt.Fprintf(os.Stderr, "  … и ещё %d\n", len(problems)-limit)
		}
		return fmt.Errorf("проблем: %d из %d документов", len(problems), checked)
	}

	fmt.Println("  восстановление подтверждено: база читается, файлы расшифровываются, суммы сходятся")
	return nil
}

func waitForDB(db *sql.DB, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := db.Ping(); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return fmt.Errorf("база не поднялась за %s: %w", timeout, err)
		}
		time.Sleep(time.Second)
	}
}
