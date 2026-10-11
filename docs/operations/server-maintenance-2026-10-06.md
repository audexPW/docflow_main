# DocFlow — контролируемое обслуживание production-сервера

Дата выполнения: 06.10.2026.
Дата документирования: 11.10.2026.

## Назначение

Зафиксировать проверенный порядок безопасной перезагрузки DocFlow,
резервного копирования и диагностики инфраструктуры.

Это исторический протокол. Перед повторением обязательно
проверять фактическое состояние текущего сервера.

## Исходный риск

Production использовал Ubuntu 26.04 LTS, kernel 7.0.0-29-generic
и NVIDIA 595.84 на GeForce RTX 5060 Ti.

Установленное ядро 7.0.0-34-generic не имело проверенного
NVIDIA-модуля. Массовое обновление системы могло удалить
работающий модуль для kernel 29.

Принято решение отложить обновление пакетов, закрепить
загрузку на kernel 29 и выполнить контролируемый reboot.

## Проверка резервной копии

Штатный verify-backup.sh восстанавливает PostgreSQL
во временный контейнер и проверяет зашифрованные файлы.

Обнаружена ошибка прав временной распаковки:
процесс backupcheck (UID 10001) не мог прочитать файлы
и ошибочно классифицировал их как отсутствующие.

Исправление в verify-backup.sh:
- временным каталогам разрешён traversal;
- временным файлам разрешено чтение;
- исходный архив и production storage не изменяются.

Проверенная резервная копия:
backups/20261006T112652Z

Фактический результат:
- PostgreSQL restore: PASS;
- documents: 1166;
- missing files: 0;
- corrupt files: 0;
- decryption/checksum: PASS;
- внешняя копия сверена по SHA256.

Сам backup и конфиденциальные файлы в Git не добавляются.

Полное восстановление на отдельном чистом сервере
пока не проверялось.

## Контролируемый reboot

GRUB_DEFAULT закреплён за kernel 7.0.0-29-generic.

Перед reboot проверены:
- резервная копия и её восстановимость;
- наличие kernel/initrd и NVIDIA-модуля;
- GRUB и UEFI;
- LAN, Tailscale и SSH;
- Docker и политика restart;
- доступность PostgreSQL и приложения.

После reboot подтверждены:
- kernel 7.0.0-29-generic;
- NVIDIA 595.84;
- отсутствие failed systemd units;
- автоматический запуск шести Docker-контейнеров;
- доступность PostgreSQL;
- административный доступ через LAN и Tailscale.

## Обнаруженная проблема nginx

После reboot:
- /login отвечал HTTP 200;
- /healthz отвечал HTTP 502;
- backend был healthy и напрямую возвращал HTTP 200.

Причина: nginx продолжал использовать старый Docker-IP backend
после повторного запуска backend при ранней недоступности PostgreSQL.

После проверки конфигурации применён точечный reload:

    docker compose exec -T frontend nginx -t
    docker compose exec -T frontend nginx -s reload

Результат: /healthz local/public = HTTP 200.

Это временная процедура восстановления.
Автоматическое обновление upstream DNS пока не реализовано.

## Команды контрольной проверки

    uname -r
    nvidia-smi
    systemctl --failed
    systemctl is-active docker tailscaled ssh
    docker compose ps
    docker compose exec -T postgres pg_isready
    curl -fsS http://127.0.0.1/healthz
    curl -fsS https://audex.pw/healthz
    tailscale status

Для просмотра логов:

    docker compose logs --tail=200 docflow-backend
    docker compose logs --tail=200 frontend
    docker compose logs --tail=200 llm surya-ocr

## Ограничения и незавершённые работы

Требуют отдельного исправления и тестирования:
1. динамическое разрешение Docker DNS в nginx;
2. правильный порядок запуска PostgreSQL и backend;
3. поддержка ai-surya в deploy.sh;
4. безопасное восстановление через restore.sh;
5. полный DR-test на отдельном сервере;
6. проверка orphan-файлов и legacy Cloudflare;
7. отдельный план обновления Ubuntu/kernel/NVIDIA.

Нельзя считать HTTP 200 доказательством полного
бизнесового сценария DocFlow -> OCR -> LLM -> XML -> 1С.

## Основные источники

Полный технический протокол в Notion:
https://app.notion.com/p/3f6a1f8c48668198a370fdf0ebff1bc9

Единая техническая задача на доработки:
https://app.notion.com/p/3f6a1f8c486681629dc6f35ff611f6c6
