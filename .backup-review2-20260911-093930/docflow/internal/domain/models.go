package domain

import (
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleClient Role = "client"
	// RoleOperator — сотрудник офиса: фотографирует и загружает документы за
	// клиента, который пришёл в офис лично, и правит распознанное.
	RoleOperator Role = "operator"
	// RoleAccountant — главбух. Закреплён администратором за конкретными
	// компаниями и видит только их документы: согласовывает или отклоняет.
	RoleAccountant Role = "accountant"
	RoleAdmin      Role = "admin"
)

func (r Role) Valid() bool {
	switch r {
	case RoleClient, RoleOperator, RoleAccountant, RoleAdmin:
		return true
	}
	return false
}

// Company — юридическое лицо заказчика. У каждой компании своя папка обмена
// внутри ONEC_FILE_DIR: документы разных юрлиц не смешиваются, и 1С забирает
// их каждый из своей папки. Заводится администратором вручную.
type Company struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	UNP    string    `json:"unp"`
	Folder string    `json:"folder"`
	// ApprovalRequired — документы этой компании уходят в 1С только после
	// согласования главбухом. Выключено — уходят автоматом, как раньше.
	ApprovalRequired bool      `json:"approval_required"`
	IsActive         bool      `json:"is_active"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type User struct {
	ID                 uuid.UUID  `json:"id"`
	CompanyID          *uuid.UUID `json:"company_id,omitempty"`
	Login              string     `json:"login"`
	PasswordHash       string     `json:"-"`
	Role               Role       `json:"role"`
	IsActive           bool       `json:"is_active"`
	MustChangePassword bool       `json:"must_change_password"`
	TokensValidFrom    time.Time  `json:"-"`
	CreatedAt          time.Time  `json:"created_at"`
}

// Статусы документа в общем жизненном цикле обработки.
type Status string

const (
	StatusReceived    Status = "received"     // принят, ждёт распознавания
	StatusProcessing  Status = "processing"   // в работе у воркера
	StatusNeedsReview Status = "needs_review" // распознан, ждёт проверки оператором
	StatusNeedsInput  Status = "needs_input"  // распознан частично, ждёт ручного ввода полей клиентом
	// StatusNeedsApproval — ждёт согласования главбухом, закреплённым за
	// компанией документа. Включается флагом компании approval_required.
	StatusNeedsApproval Status = "needs_approval"
	// StatusRejected — главбух отклонил документ; в 1С он не уходит.
	StatusRejected  Status = "rejected"
	StatusConfirmed Status = "confirmed" // подтверждён, поставлен в очередь на 1С
	StatusExported  Status = "exported"  // ушёл в 1С
	StatusFailed    Status = "failed"    // ошибка распознавания
)

// Field — одно извлечённое поле. Source показывает, откуда взято значение:
// rule — регулярки/шаблоны, model — сервис структуризации, manual — правка оператора.
type Field struct {
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
}

// LineItem — строка табличной части документа (номенклатура прихода). 1С
// раскладывает эти строки в табличную часть документа-приёмника.
type LineItem struct {
	Name   string `json:"name"`             // наименование товара/услуги
	Qty    string `json:"qty,omitempty"`    // количество
	Unit   string `json:"unit,omitempty"`   // единица измерения
	Price  string `json:"price,omitempty"`  // цена за единицу
	Amount string `json:"amount,omitempty"` // сумма по строке (итог, с НДС)
	// AmountNoVAT — та же строка без налога. В бланке это отдельная графа
	// («Сумма», «Стоимость без НДС»), и без неё приёмник в 1С вынужден
	// вычитать НДС сам, а при округлении расходится с документом.
	AmountNoVAT string `json:"amount_no_vat,omitempty"`
	VAT    string `json:"vat,omitempty"`    // ставка/сумма НДС
	// Account — счёт бухгалтерского учёта для этой строки (10, 41, 20, 26…).
	// Подбирается системой по содержимому строки, чтобы бухгалтеру не
	// приходилось выбирать счёт руками в 1С для каждой позиции.
	Account string `json:"account,omitempty"`
}

// TableColumn — колонка распознанной табличной части. Role — назначение
// колонки (name/qty/unit/price/amount/vat/other), Title — подпись из шапки.
type TableColumn struct {
	Index int    `json:"index"`
	Title string `json:"title,omitempty"`
	Role  string `json:"role,omitempty"`
}

// TableCell — ячейка строки таблицы.
type TableCell struct {
	Column int    `json:"column"`
	Role   string `json:"role,omitempty"`
	Value  string `json:"value"`
}

// TableRow — строка таблицы целиком, ячейками.
type TableRow struct {
	Index int         `json:"index"`
	Cells []TableCell `json:"cells"`
}

// FreeTable — табличная часть в том виде, в каком она напечатана в документе:
// колонки те, что стоят в шапке оригинала, и ровно в том же порядке. Никакой
// фиксированной схемы «наименование/кол-во/цена/сумма/НДС» здесь нет — в счёте
// -протоколе колонок восемь, в накладной четырнадцать, в акте три, и любая
// попытка свести их к шести приводит к тому, что сумма встаёт в графу цены.
//
// Roles заполняется отдельно и необязательно: это подсказка для выгрузки в 1С
// (какая колонка — количество, какая — сумма), а не структура самой таблицы.
type FreeTable struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	// Totals — строка «Итого» как отдельный ряд той же ширины.
	Totals []string `json:"totals,omitempty"`
	// Roles — сопоставление индекса колонки и её роли для 1С:
	// name/qty/unit/price/amount/vat/vat_rate/other. Пусто — значит роль не
	// определена, и 1С получит колонку просто по заголовку.
	Roles  []string `json:"roles,omitempty"`
	Source string   `json:"source,omitempty"`
}

// Table — табличная часть документа как таблица, а не как набор строк текста.
// Именно эта структура уходит в 1С: приёмник получает колонки и ячейки, а не
// склеенную строку, которую ему пришлось бы разбирать заново.
type Table struct {
	Columns []TableColumn `json:"columns"`
	Rows    []TableRow    `json:"rows"`
	// Source — чем разобрана таблица: layout (по координатам OCR),
	// rules (по шапке в плоском тексте), model (локальной моделью).
	Source string `json:"source,omitempty"`
	// TotalsRow — строка «Итого», если она была в таблице.
	TotalsRow []TableCell `json:"totals_row,omitempty"`
	// RawCells — та же таблица без приведения к ролям: колонки и ячейки как в
	// оригинале. Именно её показывает интерфейс и именно она уходит в 1С,
	// когда состав колонок документа не совпадает с типовым.
	RawCells *FreeTable `json:"raw,omitempty"`
}

// Recognition — результат разбора документа.
type Recognition struct {
	DocType           string           `json:"doc_type"`
	DocTypeConfidence float64          `json:"doc_type_confidence"`
	Fields            map[string]Field `json:"fields"`
	// Lines — строки табличной части (могут быть пустыми, если распознаётся
	// только шапка). Заполняются по мере развития распознавания таблиц.
	Lines []LineItem `json:"lines,omitempty"`
	// Table — та же табличная часть, но с сохранённой структурой колонок.
	Table *Table `json:"table,omitempty"`
	// Missing — обязательные поля, которые не удалось распознать. Пусто —
	// значит документ распознан полностью и может уйти в 1С автоматически.
	Missing []string `json:"missing,omitempty"`
}

type Document struct {
	ID      uuid.UUID `json:"id"`
	OwnerID uuid.UUID `json:"owner_id"`
	// CompanyID — юрлицо, к которому относится документ. От него зависит и
	// папка выгрузки, и то, какой главбух его увидит.
	CompanyID    *uuid.UUID   `json:"company_id,omitempty"`
	CompanyName  string       `json:"company_name,omitempty"`
	OriginalName string       `json:"original_name"`
	ContentType  string       `json:"content_type"`
	SizeBytes    int64        `json:"size_bytes"`
	StorageKey   string       `json:"-"`
	SHA256       string       `json:"sha256"`
	Status       Status       `json:"status"`
	Recognition  *Recognition `json:"recognition,omitempty"`
	OCRText      string       `json:"ocr_text,omitempty"`
	Error        string       `json:"error,omitempty"`

	// Обратная связь из 1С (ТЗ §2, §10): что стало с документом после выгрузки.
	OneCStatus  string `json:"onec_status,omitempty"`
	OneCRef     string `json:"onec_ref,omitempty"`
	OneCMessage string `json:"onec_message,omitempty"`

	// Согласование главбухом (когда включено флагом компании).
	ApprovedBy   *uuid.UUID `json:"approved_by,omitempty"`
	ApprovedAt   *time.Time `json:"approved_at,omitempty"`
	ApprovalNote string     `json:"approval_note,omitempty"`

	// ArchivedAt — момент, когда 1С подтвердила проведение документа. С этой
	// отметки документ уходит на страницу «Архив» и начинается отсчёт срока
	// хранения, после которого он удаляется вместе с файлом.
	ArchivedAt *time.Time `json:"archived_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ExportStatus string

const (
	ExportPending ExportStatus = "pending"
	ExportSending ExportStatus = "sending"
	ExportDone    ExportStatus = "done"
	ExportFailed  ExportStatus = "failed"
)

// ExportKind различает, что именно выгружается в 1С.
type ExportKind string

const (
	ExportKindCreate  ExportKind = "create"  // полный документ (всё распознано или проверено оператором)
	ExportKindPartial ExportKind = "partial" // распознанные поля при неполном документе; остаток дошлётся
	ExportKindUpdate  ExportKind = "update"  // досыл вручную заполненных полей к ранее отправленному документу
)

type ExportJob struct {
	ID            uuid.UUID    `json:"id"`
	DocumentID    uuid.UUID    `json:"document_id"`
	Kind          ExportKind   `json:"kind"`
	Status        ExportStatus `json:"status"`
	Attempts      int          `json:"attempts"`
	LastError     string       `json:"last_error,omitempty"`
	NextAttemptAt time.Time    `json:"next_attempt_at"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

// DeviceToken — токен мобильного устройства для серверных push-уведомлений.
type DeviceToken struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	Platform  string    `json:"platform"` // android | ios | web
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
