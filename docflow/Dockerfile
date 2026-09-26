FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/docflow ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/backupcheck ./cmd/backupcheck \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/accuracy ./cmd/accuracy

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        tesseract-ocr \
        tesseract-ocr-rus \
        tesseract-ocr-eng \
        tesseract-ocr-osd \
        poppler-utils \
        imagemagick \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# ImageMagick по умолчанию запрещает часть форматов через policy.xml;
# для наших нужд (png/jpg/pdf-растеризация) ограничения не мешают, но
# оставляем комментарий на случай отладки прав доступа к PDF.

WORKDIR /app
COPY --from=build /out/docflow /app/docflow
# Проверка восстановимости резервных копий (ТЗ §7), см. verify-backup.sh
COPY --from=build /out/backupcheck /app/backupcheck
# Замер точности на выборке заказчика (ТЗ §10), см. ACCEPTANCE.md
COPY --from=build /out/accuracy /app/accuracy

RUN useradd --system --uid 10001 docflow \
    && mkdir -p /var/lib/docflow/files /var/lib/docflow/onec_outbox \
    && chown -R docflow:docflow /var/lib/docflow
USER docflow

EXPOSE 8080
ENTRYPOINT ["/app/docflow"]
