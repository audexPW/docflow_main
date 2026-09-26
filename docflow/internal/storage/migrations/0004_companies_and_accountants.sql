-- Юрлица заказчика (компании), роль главбуха и согласование документов.
--
-- Раньше система была одноконтурной: все документы лежали в общей куче и
-- уезжали в одну папку обмена. Теперь у каждой компании своя папка, свои
-- пользователи и свой главбух — заходящий под компанией А видит только А.

CREATE TABLE IF NOT EXISTS companies (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name              TEXT NOT NULL,
    unp               TEXT NOT NULL DEFAULT '',
    -- Имя папки обмена внутри ONEC_FILE_DIR. Создаётся администратором вместе
    -- с компанией (руками, как и договаривались), дальше система только пишет
    -- в неё выгрузки.
    folder            TEXT NOT NULL UNIQUE,
    -- Документы этой компании уходят в 1С только после согласования главбухом.
    -- Выключено — уходят автоматом, как и до этой доработки.
    approval_required BOOLEAN NOT NULL DEFAULT FALSE,
    is_active         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Пользователь принадлежит одной компании (клиент и оператор). Пусто —
-- сотрудник аудиторской фирмы без привязки: админ, а также оператор,
-- обслуживающий всех.
ALTER TABLE users ADD COLUMN IF NOT EXISTS company_id UUID REFERENCES companies(id);
CREATE INDEX IF NOT EXISTS idx_users_company ON users (company_id);

-- Роль главбуха. Ограничение пересоздаём: старое знало только три роли.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('client', 'operator', 'accountant', 'admin'));

-- Закрепление главбуха за компаниями. Отдельной таблицей, а не полем в users:
-- переназначение — это добавление и снятие строки, история прав видна, и один
-- главбух при необходимости ведёт несколько юрлиц.
CREATE TABLE IF NOT EXISTS company_accountants (
    company_id  UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_company_accountants_user ON company_accountants (user_id);

-- Документ относится к компании. Проставляется при загрузке по учётке
-- загрузившего (или выбором оператора, если он работает за нескольких).
ALTER TABLE documents ADD COLUMN IF NOT EXISTS company_id UUID REFERENCES companies(id);
CREATE INDEX IF NOT EXISTS idx_documents_company ON documents (company_id);

-- Отметка согласования главбухом.
ALTER TABLE documents ADD COLUMN IF NOT EXISTS approved_by UUID REFERENCES users(id);
ALTER TABLE documents ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS approval_note TEXT NOT NULL DEFAULT '';
