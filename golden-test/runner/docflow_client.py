import requests
import time

from config_loader import load_config


CONFIG = load_config()

BASE_URL = CONFIG["docflow"]["url"]

LOGIN = CONFIG["auth"]["login"]
PASSWORD = CONFIG["auth"]["password"]


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

    print(f"Авторизация успешна: {CONFIG['name']}")

    return data["token"]


def upload_document(token, document_path):
    headers = {
        "Authorization": f"Bearer {token}"
    }

    with open(document_path, "rb") as file:
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

    print(f"Загружен документ: {data['id']}")

    return data["id"]


def wait_result(token, document_id):
    headers = {
        "Authorization": f"Bearer {token}"
    }

    for i in range(180):
        response = requests.get(
            f"{BASE_URL}/api/documents/{document_id}",
            headers=headers,
            timeout=30
        )

        response.raise_for_status()

        data = response.json()

        status = data.get("status")

        print(
            f"Попытка {i + 1}/60. Статус: {status}"
        )

        if status not in [
            "received",
            "processing"
        ]:
            return data

        time.sleep(5)

    raise Exception(
        "Документ не обработан за 15 минут"
    )


def delete_document(token, document_id):
    headers = {
        "Authorization": f"Bearer {token}"
    }

    response = requests.delete(
        f"{BASE_URL}/api/test/documents/{document_id}",
        headers=headers,
        timeout=30
    )

    response.raise_for_status()

    data = response.json()

    if data.get("status") != "deleted":
        raise Exception(
            f"Неожиданный ответ cleanup: {data}"
        )

    print(
        f"Cleanup выполнен: {document_id}"
    )

    return data
