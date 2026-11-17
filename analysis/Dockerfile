# Model API (FastAPI + your trained XGBoost model). Build from the project root:
#   docker build -f analysis/Dockerfile -t creditrisk-ml .
FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
# XGBoost needs the OpenMP runtime, which the slim image does not include.
RUN apt-get update \
 && apt-get install -y --no-install-recommends libgomp1 \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY analysis/requirements.txt analysis/requirements.txt
RUN pip install --no-cache-dir -r analysis/requirements.txt
COPY analysis/serve.py analysis/xgb_model.joblib analysis/best_threshold analysis/
RUN useradd --system --uid 10001 app
USER app
EXPOSE 8100
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8100/docs')" || exit 1
CMD ["uvicorn", "analysis.serve:app", "--host", "0.0.0.0", "--port", "8100"]