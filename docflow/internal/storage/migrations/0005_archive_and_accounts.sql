-- Архив проведённых в 1С документов и срок их хранения.
--
-- Раньше документ, проведённый в 1С, оставался в общем списке навсегда: список
-- рос, а бухгалтеру приходилось глазами отделять «уже в учёте» от «в работе».
-- Теперь 1С сообщает о проведении, документ получает отметку archived_at и
-- уезжает на отдельную страницу «Архив», а через ARCHIVE_KEEP_DAYS удаляется
-- вместе с файлом и своей выгрузкой в папке компании.

ALTER TABLE documents ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_documents_archived
    ON documents (archived_at) WHERE archived_at IS NOT NULL;

-- Документы, по которым 1С уже отчиталась о проведении до этой доработки,
-- переносим в архив задним числом — от момента ответа 1С, а не от «сейчас»:
-- иначе трёхнедельный срок хранения начался бы заново.
UPDATE documents
   SET archived_at = coalesce(onec_updated_at, updated_at)
 WHERE archived_at IS NULL
   AND onec_status IN ('posted', 'accepted');

-- Удаление документа не должно спотыкаться о ссылку из очереди выгрузки.
-- Без каскада первая же попытка удалить архивный документ упала бы на
-- нарушении внешнего ключа, и уборка молча перестала бы работать.
ALTER TABLE export_jobs DROP CONSTRAINT IF EXISTS export_jobs_document_id_fkey;
ALTER TABLE export_jobs ADD CONSTRAINT export_jobs_document_id_fkey
    FOREIGN KEY (document_id) REFERENCES documents(id) ON DELETE CASCADE;
