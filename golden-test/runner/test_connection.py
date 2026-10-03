import requests

from config_loader import load_config


CONFIG = load_config()

url = f"{CONFIG['docflow']['url']}/healthz"

response = requests.get(url, timeout=10)

print("Status:", response.status_code)
print("Body:", response.text)
