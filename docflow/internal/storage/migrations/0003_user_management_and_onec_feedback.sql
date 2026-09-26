-- Управление учётными записями (ТЗ §7: доступ по ролям).
-- Без этих полей нельзя ни сменить пароль, ни заблокировать уволенного
-- сотрудника — учётка живёт вечно с паролем, выданным при установке.
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_changed_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT FALSE;

-- Учётка, созданная при установке, обязана сменить пароль при первом входе:
-- начальный пароль печатается в консоль и оседает в истории терминала.
UPDATE users SET must_change_password = TRUE
 WHERE role = 'admin' AND password_changed_at IS NULL;

-- Токены, выпущенные до смены пароля или до блокировки, должны переставать
-- работать немедленно. JWT не отзывается сам, поэтому сверяем момент выпуска
-- с этой отметкой.
ALTER TABLE users ADD COLUMN IF NOT EXISTS tokens_valid_from TIMESTAMPTZ NOT NULL DEFAULT now();

-- Обратная синхронизация статусов из 1С (ТЗ §2, §10).
-- 1С сообщает, что документ проведён/отклонён, и это видно пользователю.
ALTER TABLE documents ADD COLUMN IF NOT EXISTS onec_status TEXT;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS onec_ref TEXT;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS onec_message TEXT;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS onec_updated_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_documents_onec_status
    ON documents (onec_status) WHERE onec_status IS NOT NULL;
