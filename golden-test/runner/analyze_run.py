import csv
import json
from collections import Counter, defaultdict
from pathlib import Path

from compare import (
    compare_fields,
    get_actual_value,
    load_json,
    normalize,
)


BASE_DIR = Path(__file__).resolve().parent.parent
REPORT_DIR = BASE_DIR / "reports" / "secondary"

ANALYSIS_JSON = REPORT_DIR / "analysis.json"
DOCUMENTS_CSV = REPORT_DIR / "analysis_documents.csv"
FIELDS_CSV = REPORT_DIR / "analysis_fields.csv"


def get_source_file(doc_id):
    files = sorted(
        (BASE_DIR / doc_id).glob("original_document.*")
    )

    if not files:
        return None

    return files[0]


def pct(a, b):
    if not b:
        return 0.0

    return round(a / b * 100, 2)


def main():
    documents = []

    statuses = Counter()
    extensions = Counter()

    unexpected_field_counts = Counter()

    field_stats = defaultdict(
        lambda: {
            "validated": 0,
            "matched": 0,
            "missing": 0,
            "mismatch": 0,
            "skipped": 0,
            "extra_on_null": 0,
        }
    )

    totals = {
        "documents": 0,
        "validated_fields": 0,
        "matched": 0,
        "missing": 0,
        "mismatch": 0,
        "skipped": 0,
        "extra_on_null": 0,
        "unexpected_actual_fields": 0,
        "doc_type_matches": 0,
    }

    for number in range(1, 92):
        doc_id = f"{number:03d}"

        expected_path = (
            BASE_DIR /
            doc_id /
            "expected.json"
        )

        actual_path = (
            REPORT_DIR /
            f"{doc_id}_actual.json"
        )

        if not expected_path.exists():
            print(
                f"{doc_id}: expected.json отсутствует"
            )
            continue

        if not actual_path.exists():
            print(
                f"{doc_id}: actual.json отсутствует"
            )
            continue

        expected = load_json(expected_path)
        actual = load_json(actual_path)

        recognition = (
            actual.get("recognition") or {}
        )

        expected_fields = (
            expected.get("fields") or {}
        )

        actual_fields = (
            recognition.get("fields") or {}
        )

        comparison = compare_fields(
            expected_fields,
            actual_fields
        )

        validated = len(
            comparison["validated"]
        )

        matched = len(
            comparison["matched"]
        )

        missing = len(
            comparison["missing"]
        )

        mismatch = len(
            comparison["mismatch"]
        )

        skipped = len(
            comparison["skipped"]
        )

        extra_on_null = len(
            comparison["extra"]
        )

        accuracy = pct(
            matched,
            validated
        )

        expected_doc_type = (
            expected.get("doc_type")
        )

        actual_doc_type = (
            recognition.get("doc_type")
        )

        doc_type_match = (
            expected_doc_type ==
            actual_doc_type
        )

        status = actual.get(
            "status",
            "unknown"
        )

        statuses[status] += 1

        source_file = get_source_file(
            doc_id
        )

        extension = (
            source_file.suffix.lower().lstrip(".")
            if source_file
            else "unknown"
        )

        extensions[extension] += 1

        unexpected_actual = []

        for field_name, field_data in actual_fields.items():

            if field_name in expected_fields:
                continue

            value = normalize(
                field_data.get("value")
                if isinstance(field_data, dict)
                else None
            )

            if value is None:
                continue

            unexpected_actual.append({
                "field": field_name,
                "actual": value,
            })

            unexpected_field_counts[
                field_name
            ] += 1

        for field_name, expected_data in expected_fields.items():

            expected_value = normalize(
                expected_data.get("value")
            )

            actual_value = normalize(
                get_actual_value(
                    actual_fields,
                    field_name
                )
            )

            stats = field_stats[
                field_name
            ]

            if expected_value is None:

                stats["skipped"] += 1

                if actual_value is not None:
                    stats[
                        "extra_on_null"
                    ] += 1

                continue

            stats["validated"] += 1

            if actual_value == expected_value:
                stats["matched"] += 1

            elif actual_value is None:
                stats["missing"] += 1

            else:
                stats["mismatch"] += 1

        totals["documents"] += 1
        totals["validated_fields"] += validated
        totals["matched"] += matched
        totals["missing"] += missing
        totals["mismatch"] += mismatch
        totals["skipped"] += skipped
        totals["extra_on_null"] += extra_on_null

        totals[
            "unexpected_actual_fields"
        ] += len(
            unexpected_actual
        )

        if doc_type_match:
            totals[
                "doc_type_matches"
            ] += 1

        documents.append({
            "document": doc_id,
            "source_type": extension,
            "status": status,

            "expected_doc_type":
                expected_doc_type,

            "actual_doc_type":
                actual_doc_type,

            "doc_type_match":
                doc_type_match,

            "validated_fields":
                validated,

            "matched":
                matched,

            "missing":
                missing,

            "mismatch":
                mismatch,

            "extra_on_null":
                extra_on_null,

            "unexpected_actual_fields":
                len(unexpected_actual),

            "accuracy":
                accuracy,

            "missing_details":
                comparison["missing"],

            "mismatch_details":
                comparison["mismatch"],

            "extra_details":
                comparison["extra"],

            "unexpected_actual_details":
                unexpected_actual,
        })

    totals["field_accuracy"] = pct(
        totals["matched"],
        totals["validated_fields"]
    )

    totals["doc_type_accuracy"] = pct(
        totals["doc_type_matches"],
        totals["documents"]
    )

    accuracies = [
        d["accuracy"]
        for d in documents
        if d["validated_fields"] > 0
    ]

    totals["average_document_accuracy"] = (
        round(
            sum(accuracies) /
            len(accuracies),
            2
        )
        if accuracies
        else 0.0
    )

    field_rows = []

    for field_name, stats in field_stats.items():

        field_accuracy = pct(
            stats["matched"],
            stats["validated"]
        )

        field_rows.append({
            "field": field_name,
            **stats,
            "accuracy": field_accuracy,
            "errors":
                stats["missing"] +
                stats["mismatch"],
        })

    field_rows.sort(
        key=lambda x: (
            -x["errors"],
            x["accuracy"],
            x["field"]
        )
    )

    documents_sorted = sorted(
        documents,
        key=lambda x: (
            x["accuracy"],
            -x["validated_fields"],
            x["document"]
        )
    )

    analysis = {
        "methodology": {
            "scored": [
                "doc_type",
                "expected.fields with non-null expected value",
            ],
            "not_scored": [
                "extended",
                "lines",
                "unexpected actual fields not present in expected.fields",
            ],
            "note":
                "Unexpected fields are diagnostic only and do not reduce field_accuracy.",
        },

        "totals": totals,

        "statuses": dict(
            statuses
        ),

        "source_types": dict(
            extensions
        ),

        "unexpected_field_counts": dict(
            unexpected_field_counts.most_common()
        ),

        "fields": field_rows,

        "documents": documents_sorted,
    }

    ANALYSIS_JSON.write_text(
        json.dumps(
            analysis,
            ensure_ascii=False,
            indent=2
        ),
        encoding="utf-8"
    )

    with DOCUMENTS_CSV.open(
        "w",
        encoding="utf-8-sig",
        newline=""
    ) as f:

        columns = [
            "document",
            "source_type",
            "status",
            "expected_doc_type",
            "actual_doc_type",
            "doc_type_match",
            "validated_fields",
            "matched",
            "missing",
            "mismatch",
            "extra_on_null",
            "unexpected_actual_fields",
            "accuracy",
        ]

        writer = csv.DictWriter(
            f,
            fieldnames=columns
        )

        writer.writeheader()

        for row in documents_sorted:

            writer.writerow({
                key: row.get(key)
                for key in columns
            })

    with FIELDS_CSV.open(
        "w",
        encoding="utf-8-sig",
        newline=""
    ) as f:

        columns = [
            "field",
            "validated",
            "matched",
            "missing",
            "mismatch",
            "skipped",
            "extra_on_null",
            "accuracy",
            "errors",
        ]

        writer = csv.DictWriter(
            f,
            fieldnames=columns
        )

        writer.writeheader()

        writer.writerows(
            field_rows
        )

    print()
    print("===== GOLDEN ANALYSIS =====")
    print(
        "Документов:",
        totals["documents"]
    )
    print(
        "Статусы:",
        dict(statuses)
    )
    print(
        "Типы файлов:",
        dict(extensions)
    )
    print()

    print(
        "Проверяемых полей:",
        totals["validated_fields"]
    )
    print(
        "Совпало:",
        totals["matched"]
    )
    print(
        "Missing:",
        totals["missing"]
    )
    print(
        "Mismatch:",
        totals["mismatch"]
    )
    print(
        "Extra на null:",
        totals["extra_on_null"]
    )
    print(
        "Unexpected actual:",
        totals[
            "unexpected_actual_fields"
        ]
    )

    print()
    print(
        "FIELD ACCURACY:",
        f'{totals["field_accuracy"]}%'
    )
    print(
        "AVG DOCUMENT ACCURACY:",
        f'{totals["average_document_accuracy"]}%'
    )
    print(
        "DOC TYPE ACCURACY:",
        f'{totals["doc_type_accuracy"]}%'
    )

    print()
    print("=== 15 проблемных полей ===")

    for row in field_rows[:15]:

        print(
            row["field"],
            "| accuracy:",
            f'{row["accuracy"]}%',
            "| validated:",
            row["validated"],
            "| missing:",
            row["missing"],
            "| mismatch:",
            row["mismatch"],
            "| extra:",
            row["extra_on_null"],
        )

    print()
    print("=== 15 худших документов ===")

    for row in documents_sorted[:15]:

        print(
            row["document"],
            "|",
            row["source_type"],
            "|",
            row["status"],
            "| accuracy:",
            f'{row["accuracy"]}%',
            "|",
            f'{row["matched"]}/{row["validated_fields"]}',
        )

    print()
    print("Сохранено:")
    print(ANALYSIS_JSON)
    print(DOCUMENTS_CSV)
    print(FIELDS_CSV)


if __name__ == "__main__":
    main()
