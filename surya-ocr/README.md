# surya-ocr — локальный сервис распознавания

FastAPI-обёртка над Surya OCR. Держит модели в памяти, распознаёт присланную
страницу-изображение, отдаёт текст. Только CPU, без интернета после сборки.

## API

- `GET /health` → `{"status","ready","error"}`
- `POST /ocr` (multipart, поле `file` — png/jpg) → `{"text","lines","took_ms"}`

Go-бэкенд DocFlow вызывает `/ocr` по одной странице, когда `OCR_ENGINE=surya`
или `OCR_ENGINE=auto`. PDF растеризует сам бэкенд — сюда всегда приходят картинки.

## Версия и почему так

Surya зафиксирована на **0.17.1** — последняя линейка с отдельными моделями
detection+recognition. Начиная с 0.20 (Surya 2) OCR идёт через VLM
(vllm / llama-server), что на CPU-боксе с 8-12 ГБ RAM непрактично.

## Ресурсы (важно для этого железа)

Всё на CPU. Распознавание одной страницы — от единиц до десятков секунд в
зависимости от плотности текста и числа ядер. Настройки через переменные
окружения (задаются в `docker-compose.yml`):

| Переменная | Смысл | Реком. |
|---|---|---|
| `OCR_THREADS` | число потоков torch/OMP | 4 (оставьте ядра под LLM/БД) |
| `RECOGNITION_BATCH_SIZE` | размер батча распознавания | 16-32 (меньше = меньше RAM) |
| `DETECTOR_BATCH_SIZE` | размер батча детекции | 4-6 |
| `SURYA_ROW_TOL` | допуск группировки строк по Y (px) | 12 |

Инференс сериализован (один запрос за раз) — так предсказуемее по RAM.

## Локальный прогон без Docker

```bash
python -m venv .venv && source .venv/bin/activate
pip install --index-url https://download.pytorch.org/whl/cpu torch==2.7.1
pip install -r requirements.txt
python server.py           # слушает :8422
```
