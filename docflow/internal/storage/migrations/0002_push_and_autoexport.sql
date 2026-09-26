-- Токены устройств для серверных push-уведомлений (FCM/APNs).
CREATE TABLE IF NOT EXISTS device_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform   TEXT NOT NULL CHECK (platform IN ('android', 'ios', 'web')),
    token      TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_device_tokens_user ON device_tokens (user_id);

-- Вид выгрузки: create — полный, partial — частичный (не все поля распознаны),
-- update — досыл вручную заполненных полей. 1С сопоставляет по SourceId.
ALTER TABLE export_jobs
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'create';
