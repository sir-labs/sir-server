FROM python:3.11-slim

COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

WORKDIR /app
COPY pyproject.toml uv.lock ./
COPY watcher/ watcher/

RUN uv pip install --system -e ./watcher

CMD ["python", "-m", "watcher.main"]
