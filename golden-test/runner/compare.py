import json
import sys
from pathlib import Path


def load_json(path):
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def normalize(value):
    if value is None:
        return None

    if isinstance(value, str):
        return value.strip()

    return value


def get_actual_value(actual_fields, key):
    field = actual_fields.get(key)

    if not field:
        return None

    return field.get("value")


def compare_fields(expected_fields, actual_fields):

    result = {
        "validated": [],
        "matched": [],
        "missing": [],
        "mismatch": [],
        "skipped": [],
        "extra": []
    }

    for key, expected_field in expected_fields.items():

        expected_value = normalize(
            expected_field.get("value")
        )

        actual_value = normalize(
            get_actual_value(actual_fields, key)
        )

        # Поле есть в контракте, но эталонного значения нет
        if expected_value is None:
            result["skipped"].append(key)

            # Если DocFlow что-то придумал дополнительно
            if actual_value is not None:
                result["extra"].append({
                    "field": key,
                    "actual": actual_value
                })

            continue


        # Поле участвует в проверке
        result["validated"].append(key)


        # Совпадение
        if expected_value == actual_value:
            result["matched"].append(key)
            continue


        # Значение ожидалось, но не найдено
        if actual_value is None:
            result["missing"].append({
                "field": key,
                "expected": expected_value
            })
            continue


        # Значения отличаются
        result["mismatch"].append({
            "field": key,
            "expected": expected_value,
            "actual": actual_value
        })


    return result


def main():

    if len(sys.argv) != 3:
        print(
            "usage: python compare.py expected.json actual.json"
        )
        sys.exit(1)


    expected_path = Path(sys.argv[1])
    actual_path = Path(sys.argv[2])


    expected = load_json(expected_path)
    actual = load_json(actual_path)


    recognition = actual.get(
        "recognition",
        {}
    )


    expected_fields = expected.get(
        "fields",
        {}
    )

    actual_fields = recognition.get(
        "fields",
        {}
    )


    fields_result = compare_fields(
        expected_fields,
        actual_fields
    )


    expected_doc_type = expected.get(
        "doc_type"
    )

    actual_doc_type = recognition.get(
        "doc_type"
    )


    validated_count = len(
        fields_result["validated"]
    )

    matched_count = len(
        fields_result["matched"]
    )


    report = {

        "doc_type": {
            "expected": expected_doc_type,
            "actual": actual_doc_type,
            "match": expected_doc_type == actual_doc_type
        },


        "fields": fields_result,


        "metrics": {

            "validated_fields": validated_count,

            "matched": matched_count,

            "missing": len(
                fields_result["missing"]
            ),

            "mismatch": len(
                fields_result["mismatch"]
            ),

            "skipped": len(
                fields_result["skipped"]
            ),

            "extra": len(
                fields_result["extra"]
            ),

            "accuracy": round(
                matched_count / validated_count * 100,
                2
            ) if validated_count else 0
        }
    }


    print(
        json.dumps(
            report,
            ensure_ascii=False,
            indent=2
        )
    )


if __name__ == "__main__":
    main()
