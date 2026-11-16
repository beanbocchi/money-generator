FROM python:3.12.12-slim
# Pinned: a sidecar that runs unattended should not pick up a new SDK on rebuild.
RUN pip install --no-cache-dir openai==3.40.0
COPY newsgate.py /app/newsgate.py
USER 1000:1000
ENTRYPOINT ["python3", "-I", "/app/newsgate.py"]
