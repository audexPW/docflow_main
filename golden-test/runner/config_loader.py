import yaml
from pathlib import Path


def load_config(path="config/secondary.yaml"):

    config_path = Path(path)

    if not config_path.exists():
        raise FileNotFoundError(
            f"Config not found: {config_path}"
        )

    with open(
        config_path,
        "r",
        encoding="utf-8"
    ) as f:
        return yaml.safe_load(f)
