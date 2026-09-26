-- Справочник контрагентов по УНП (разбор от 10.09.2026, пункт 7).
--
-- Одна компания приезжала в документах пятью написаниями, и в 1С из этого
-- выходило пять контрагентов вместо одного. Ключ записи — УНП, а не строка
-- наименования; новое написание при известном номере ложится синонимом.
-- Справочник свой у каждой компании заказчика (одна компания — одна база 1С).
--
-- Пополняется, когда документ уходит в 1С (EnqueueExport): человек его видел
-- или он прошёл все проверки. Первичное наполнение — из уже выгруженных
-- документов, делается бэкендом при старте (SeedCounterparties): там нужна
-- сверка наименований, которую SQL не сделает.

CREATE TABLE IF NOT EXISTS counterparties (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id UUID REFERENCES companies(id) ON DELETE CASCADE,
    unp        TEXT NOT NULL,
    name       TEXT NOT NULL,
    synonyms   TEXT[] NOT NULL DEFAULT '{}',
    source     TEXT NOT NULL DEFAULT 'export',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_counterparties_company_unp
    ON counterparties (COALESCE(company_id, '00000000-0000-0000-0000-000000000000'::uuid), unp);
