import json
from pathlib import Path

from config_loader import load_config

from docflow_client import (
    login,
    upload_document,
    wait_result,
    delete_document
)


BASE_DIR = Path(__file__).resolve().parent.parent

CONFIG = load_config()

REPORT_DIR = (
    BASE_DIR /
    CONFIG["reports"]["path"]
)

CLEANUP_ENABLED = (
    CONFIG
    .get("cleanup", {})
    .get("enabled", False)
)


def save_json(path, data):
    path.parent.mkdir(
        parents=True,
        exist_ok=True
    )

    with open(
        path,
        "w",
        encoding="utf-8"
    ) as f:
        json.dump(
            data,
            f,
            ensure_ascii=False,
            indent=2
        )


def main():
    REPORT_DIR.mkdir(
        parents=True,
        exist_ok=True
    )

    token = login()

    documents = [
        f"{i:03d}"
        for i in range(1, 92)
    ]

    summary = {
        "target": CONFIG["name"],
        "total": len(documents),
        "processed": 0,
        "failed": 0,
        "cleanup_enabled": CLEANUP_ENABLED,
        "results": []
    }

    for doc_id in documents:
        print(
            f"\n===== {doc_id} ====="
        )

        actual_path = (
            REPORT_DIR /
            f"{doc_id}_actual.json"
        )

        document_id = None

        item_result = {
            "document": doc_id,
            "document_id": None,
            "status": None,
            "cleanup": None
        }

        try:
            document_dir = (
                BASE_DIR /
                doc_id
            )

            original_files = sorted(
                document_dir.glob(
                    "original_document.*"
                )
            )

            if not original_files:
                raise FileNotFoundError(
                    f"Нет original_document.* в {document_dir}"
                )

            document_path = (
                original_files[0]
            )

            print(
                f"Исходный файл: {document_path.name}"
            )

            document_id = upload_document(
                token,
                document_path
            )

            item_result["document_id"] = (
                document_id
            )

            result = wait_result(
                token,
                document_id
            )

            save_json(
                actual_path,
                result
            )

            item_result["status"] = (
                result.get("status")
            )

            summary["processed"] += 1

            print(
                f"Actual сохранён: {actual_path}"
            )

        except Exception as e:
            summary["failed"] += 1

            item_result["status"] = (
                "failed"
            )

            item_result["error"] = (
                str(e)
            )

            print(
                f"Ошибка {doc_id}: {e}"
            )

        finally:
            if (
                CLEANUP_ENABLED
                and document_id is not None
            ):
                try:
                    delete_document(
                        token,
                        document_id
                    )

                    item_result["cleanup"] = (
                        "deleted"
                    )

                except Exception as cleanup_error:
                    item_result["cleanup"] = (
                        "failed"
                    )

                    item_result[
                        "cleanup_error"
                    ] = str(
                        cleanup_error
                    )

                    print(
                        f"ОШИБКА CLEANUP {doc_id}: "
                        f"{cleanup_error}"
                    )

            elif not CLEANUP_ENABLED:
                item_result["cleanup"] = (
                    "disabled"
                )

            summary["results"].append(
                item_result
            )

    save_json(
        REPORT_DIR / "summary.json",
        summary
    )

    print(
        "\n===== DONE ====="
    )

    print(
        f"Всего: {summary['total']}"
    )

    print(
        f"Обработано: {summary['processed']}"
    )

    print(
        f"Ошибок: {summary['failed']}"
    )


if __name__ == "__main__":
    main()
