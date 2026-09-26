#!/usr/bin/env bash
# Складывает каталог трассировки в архив для отправки.
set -euo pipefail
OUT="trace-$(date +%Y%m%d-%H%M%S).tar.gz"
tar -czf "$OUT" trace
echo "Готово: $(pwd)/$OUT ($(du -h "$OUT" | cut -f1))"
