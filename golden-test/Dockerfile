FROM python:3.12-slim

WORKDIR /app

COPY runner /app/runner
COPY config /app/config

RUN pip install --no-cache-dir requests pyyaml

CMD ["python", "runner/run_secondary.py"]
