package worker

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"docflow/internal/config"
	"docflow/internal/filestore"
	"docflow/internal/storage"
)

// Housekeeper — фоновая уборка, без которой система тихо деградирует за
// несколько месяцев работы:
//
//  1. Каталог выгрузки в 1С растёт бесконечно. Файлы, которые 1С уже забрала
//     (по ним пришёл статус) либо которые старше срока хранения, переезжают в
//     подкаталог archive/ГГГГ-ММ и там доживают.
//  2. Молчание 1С выглядит как успех. Если приёмник не отчитался за
//     ONEC_ACK_TIMEOUT, в лог уходит предупреждение с количеством документов —
//     это то, что видно в мониторинге и на приёмке.
//  3. Архивные документы (проведённые в 1С) хранятся ARCHIVE_KEEP_DAYS дней,
//     после чего удаляются целиком: запись, зашифрованный файл и выгрузка в
//     папке обмена своей компании. Хранить их дольше незачем — документ уже
//     лежит в учёте, а копия здесь только занимает место.
type Housekeeper struct {
	db       *storage.DB
	files    *filestore.Store
	cfg      config.OneCConfig
	interval time.Duration
	log      *slog.Logger
}

func NewHousekeeper(db *storage.DB, files *filestore.Store, cfg config.OneCConfig, log *slog.Logger) *Housekeeper {
	return &Housekeeper{
		db:       db,
		files:    files,
		cfg:      cfg,
		interval: time.Hour,
		log:      log.With("worker", "housekeeper"),
	}
}

func (h *Housekeeper) Run(ctx context.Context) {
	// Первый прогон вскоре после старта, дальше по расписанию.
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			h.once(ctx)
			timer.Reset(h.interval)
		}
	}
}

func (h *Housekeeper) once(ctx context.Context) {
	h.purgeExpiredArchive(ctx)
	h.archiveOutbox()
	h.reportUnacknowledged(ctx)
}

// purgeExpiredArchive удаляет архивные документы, у которых вышел срок
// хранения. Порядок важен: сначала файлы, потом запись в базе. Если удаление
// файла не удалось, запись остаётся — документ попадёт в следующий прогон, и
// файл не осиротеет в хранилище.
func (h *Housekeeper) purgeExpiredArchive(ctx context.Context) {
	if h.cfg.ArchiveKeepDays <= 0 {
		return
	}
	docs, err := h.db.ExpiredArchived(ctx, h.cfg.ArchiveKeepDays, 500)
	if err != nil {
		h.log.Warn("не удалось выбрать архивные документы к удалению", "error", err)
		return
	}
	if len(docs) == 0 {
		return
	}

	var purged int
	for _, d := range docs {
		if h.files != nil {
			if err := h.files.Remove(d.StorageKey); err != nil {
				h.log.Warn("не удалось удалить файл документа",
					"document_id", d.ID, "error", err)
				continue
			}
		}
		// Выгрузка этого документа в папке обмена — тоже копия персональных
		// данных заказчика. Удаляем её из папки компании и из архива выгрузок,
		// иначе «удалённый» документ остался бы читаемым в сетевой шаре.
		h.removeOutboxFiles(d)

		if err := h.db.DeleteDocument(ctx, d.ID); err != nil {
			h.log.Warn("не удалось удалить запись документа", "document_id", d.ID, "error", err)
			continue
		}
		h.db.Audit(ctx, nil, "purge_archived", "document", d.ID.String(),
			map[string]any{
				"name":        d.OriginalName,
				"archived_at": d.ArchivedAt,
				"keep_days":   h.cfg.ArchiveKeepDays,
			})
		purged++
	}

	if purged > 0 {
		h.log.Info("архивные документы удалены по сроку хранения",
			"count", purged, "keep_days", h.cfg.ArchiveKeepDays)
	}
}

// removeOutboxFiles удаляет XML-выгрузки документа. Имя файла заканчивается
// идентификатором документа (см. onec/file.go), поэтому ищем по суффиксу — и в
// папке компании, и в корне каталога обмена, и в архиве выгрузок.
func (h *Housekeeper) removeOutboxFiles(d storage.ExpiredDocument) {
	if h.cfg.FileDir == "" {
		return
	}
	suffix := "_" + d.ID.String() + ".xml"

	dirs := []string{h.cfg.FileDir}
	if folder := sanitizeFolder(d.CompanyFolder); folder != "" {
		dirs = append(dirs, filepath.Join(h.cfg.FileDir, folder))
	}
	// Архив выгрузок разложен по месяцам (и по папкам компаний внутри них),
	// поэтому его обходим целиком.
	archiveRoot := filepath.Join(h.cfg.FileDir, "archive")
	_ = filepath.WalkDir(archiveRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), suffix) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				h.log.Warn("не удалось удалить выгрузку из архива", "file", path, "error", err)
			}
		}
		return nil
	})

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !os.IsNotExist(err) {
				h.log.Warn("не удалось удалить выгрузку", "file", e.Name(), "error", err)
			}
		}
	}
}

// sanitizeFolder повторяет проверку из onec/file.go: имя папки заводит человек
// руками, и оно не должно уводить удаление за пределы каталога обмена.
func sanitizeFolder(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if name == "." || name == ".." {
		return ""
	}
	return name
}

func (h *Housekeeper) archiveOutbox() {
	if h.cfg.FileDir == "" || h.cfg.OutboxKeepDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -h.cfg.OutboxKeepDays)

	// Выгрузки лежат и в корне каталога обмена (документы без компании), и в
	// папке каждого юрлица. Архивируем и то и другое, сохраняя, из чьей папки
	// файл: иначе архив превратится в общую свалку.
	moved := h.archiveDir(h.cfg.FileDir, "", cutoff)

	entries, err := os.ReadDir(h.cfg.FileDir)
	if err != nil {
		h.log.Warn("не удалось прочитать каталог выгрузки", "dir", h.cfg.FileDir, "error", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "archive" {
			continue
		}
		moved += h.archiveDir(filepath.Join(h.cfg.FileDir, e.Name()), e.Name(), cutoff)
	}

	if moved > 0 {
		h.log.Info("выгрузки убраны в архив", "count", moved, "older_than_days", h.cfg.OutboxKeepDays)
	}
}

// archiveDir убирает старые выгрузки одной папки. company — имя папки юрлица
// (пусто для корня каталога обмена).
func (h *Housekeeper) archiveDir(dir, company string, cutoff time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		h.log.Warn("не удалось прочитать каталог выгрузки", "dir", dir, "error", err)
		return 0
	}

	var moved int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".xml") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}

		dstDir := filepath.Join(h.cfg.FileDir, "archive", info.ModTime().Format("2006-01"))
		if company != "" {
			dstDir = filepath.Join(dstDir, company)
		}
		if err := os.MkdirAll(dstDir, 0o750); err != nil {
			h.log.Warn("не удалось создать каталог архива", "dir", dstDir, "error", err)
			return moved
		}
		if err := os.Rename(filepath.Join(dir, e.Name()), filepath.Join(dstDir, e.Name())); err != nil {
			h.log.Warn("не удалось убрать файл в архив", "file", e.Name(), "error", err)
			continue
		}
		moved++
	}
	return moved
}

func (h *Housekeeper) reportUnacknowledged(ctx context.Context) {
	if h.cfg.Mode == "disabled" || h.cfg.AckTimeout <= 0 {
		return
	}
	n, err := h.db.CountUnacknowledgedExports(ctx, h.cfg.AckTimeout)
	if err != nil {
		h.log.Warn("не удалось посчитать неподтверждённые выгрузки", "error", err)
		return
	}
	if n == 0 {
		return
	}
	// Это ровно тот случай, когда «всё зелёное», а документов в учёте нет.
	h.log.Warn("1С не подтвердила обработку выгруженных документов",
		"count", n, "older_than", h.cfg.AckTimeout.String(),
		"hint", "проверьте, забирает ли 1С файлы из каталога обмена")
}
