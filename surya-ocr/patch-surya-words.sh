#!/bin/bash
# Чинит координаты слов в surya-ocr/server.py.
# Запускать из /opt/docflow-deploy
set -e
F=surya-ocr/server.py
[ -f "$F" ] || { echo "не найден $F — запустите из /opt/docflow-deploy"; exit 1; }
cp -a "$F" "$F.bak.$(date +%Y-%m-%d_%H-%M-%S)"

python3 - "$F" <<'PY'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()

a1 = '_GAP_FACTOR = float(os.getenv("SURYA_GAP_FACTOR", "1.2"))'
assert a1 in s, "якорь 1 не найден"
s = s.replace(a1, a1 + '''

# Доля слов строки, чей бокс шире этой части всей строки, начиная с которой
# координаты от return_words считаются непригодными.
_WORD_DEGEN_SHARE = float(os.getenv("SURYA_WORD_DEGEN_SHARE", "0.6"))''', 1)

a2 = 'def _words_from_line(txt, bbox, words_attr=None):'
assert a2 in s, "якорь 2 не найден"
s = s.replace(a2, '''def _words_degenerate(out, bbox):
    """У слов нет собственных координат: бокс слова примерно равен боксу строки.

    surya с return_words=True на сканах бланков возвращает для каждого слова
    прямоугольник всей строки, отличающийся на 1-3 пикселя. Такие координаты
    хуже, чем никакие: центр у всех слов один, поэтому _text_in_box кладёт всю
    строку в одну графу, а проекция колонок на стороне Go не видит границ
    вовсе. Пропорциональная нарезка бокса строки в этом случае точнее.
    """
    if len(out) < 3:
        return False
    line_w = max(float(bbox[2]) - float(bbox[0]), 1.0)
    wide = sum(1 for _, b in out if (b[2] - b[0]) / line_w > _WORD_DEGEN_SHARE)
    return wide * 2 > len(out)


''' + a2, 1)

a3 = """            out.append((wt, [float(v) for v in wb[:4]]))
        if out:
            return out"""
assert a3 in s, "якорь 3 не найден"
s = s.replace(a3, """            out.append((wt, [float(v) for v in wb[:4]]))
        if out and not _words_degenerate(out, bbox):
            return out
        if out:
            _dbg("WORDS: координаты слов вырождены, режу бокс строки:", txt[:60])
            out = []""", 1)

open(p, 'w', encoding='utf-8').write(s)
print("server.py пропатчен")
PY

python3 -m py_compile "$F" && echo "синтаксис ок"
echo
echo "Дальше:"
echo "  docker compose build surya-ocr && docker compose up -d surya-ocr"
echo "  curl -s localhost:8422/health"
