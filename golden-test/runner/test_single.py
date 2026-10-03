import requests
import time
import json
from pathlib import Path

from config_loader import load_config


# Настройки
CONFIG = load_config()
BASE_URL = CONFIG["docflow"]["url"]
LOGIN = CONFIG["auth"]["login"]
PASSWORD = CONFIG["auth"]["password"]

DOCUMENT_PATH = Path("/app/001/original_document.pdf")
RESULT_PATH = Path("/app/reports/test_001_actual.json")


def login():
    response = requests.post(
        f"{BASE_URL}/api/auth/login",
        json={
            "login": LOGIN,
            "password": PASSWORD
        },
        timeout=30
    )

    response.raise_for_status()

    data = response.json()

    print("Авторизация успешна")

    return data["token"]


def upload_document(token):
    headers = {
        "Authorization": f"Bearer {token}"
    }

    with open(DOCUMENT_PATH, "rb") as file:
        response = requests.post(
            f"{BASE_URL}/api/documents",
            headers=headers,
            files={
                "file": file
            },
            timeout=60
        )

    response.raise_for_status()

    data = response.json()

    print("Документ загружен:")
    print(data)

    return data["id"]


def wait_result(token, document_id):
    headers = {
        "Authorization": f"Bearer {token}"
    }

    for i in range(60):

        response = requests.get(
            f"{BASE_URL}/api/documents/{document_id}",
            headers=headers,
            timeout=30
        )

        response.raise_for_status()

        data = response.json()

        status = data.get("status")

        print(
            f"Попытка {i+1}/60. Статус: {status}"
        )

        if status not in [
            "received",
            "processing"
        ]:
            return data

        time.sleep(5)

    raise Exception(
        "Документ не обработан за 5 минут"
    )


def main():

    token = login()

    document_id = upload_document(token)

    result = wait_result(
        token,
        document_id
    )

    RESULT_PATH.parent.mkdir(
        exist_ok=True
    )

    with open(
        RESULT_PATH,
        "w",
        encoding="utf-8"
    ) as f:
        json.dump(
            result,
            f,
            ensure_ascii=False,
            indent=2
        )

    print()
    print("Результат сохранён:")
    print(RESULT_PATH)


if __name__ == "__main__":
    main()
