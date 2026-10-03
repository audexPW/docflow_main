import json
from pathlib import Path

from docflow_client import (
    login,
    upload_document,
    wait_result
)

import subprocess


BASE_DIR = Path(__file__).resolve().parent.parent

REPORTS_DIR = BASE_DIR / "reports"


def save_json(path, data):

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


def run_compare(expected, actual):

    result = subprocess.run(
        [
            "python3",
            "runner/compare.py",
            str(expected),
            str(actual)
        ],
        capture_output=True,
        text=True
    )

    if result.returncode != 0:
        raise Exception(
            result.stderr
        )

    return json.loads(
        result.stdout
    )


def main():

    REPORTS_DIR.mkdir(
        exist_ok=True
    )


    token = login()


    documents = sorted(
        [
            x
            for x in BASE_DIR.iterdir()
            if x.is_dir()
            and x.name.isdigit()
            and len(x.name) == 3
        ]
    )


    summary = {

        "total": len(documents),

        "processed": 0,

        "failed": 0,

        "results": []

    }


    for doc in documents[:1]:

        doc_id = doc.name

        print(
            f"\n===== {doc_id} ====="
        )


        try:

            pdf = (
                doc /
                "original_document.pdf"
            )

            expected = (
                doc /
                "expected.json"
            )


            if not pdf.exists():

                raise Exception(
                    "Нет original_document.pdf"
                )


            actual_result = upload_document(
                token,
                pdf
            )


            result = wait_result(
                token,
                actual_result
            )


            actual_file = (
                REPORTS_DIR /
                f"{doc_id}_actual.json"
            )


            save_json(
                actual_file,
                result
            )


            compare = run_compare(
                expected,
                actual_file
            )


            compare_file = (
                REPORTS_DIR /
                f"{doc_id}_compare.json"
            )


            save_json(
                compare_file,
                compare
            )


            accuracy = (
                compare
                .get("metrics", {})
                .get("accuracy")
            )


            summary["processed"] += 1


            summary["results"].append(
                {
                    "document": doc_id,
                    "accuracy": accuracy,
                    "status": "ok"
                }
            )


            print(
                f"{doc_id}: accuracy={accuracy}"
            )


        except Exception as e:

            summary["failed"] += 1


            summary["results"].append(
                {
                    "document": doc_id,
                    "status": "failed",
                    "error": str(e)
                }
            )


            print(
                f"{doc_id}: ERROR {e}"
            )


    save_json(
        REPORTS_DIR / "summary.json",
        summary
    )


    print("\n=== DONE ===")


if __name__ == "__main__":
    main()
