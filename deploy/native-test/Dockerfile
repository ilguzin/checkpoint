# Test harness only: installed VPS services do not use containers.
FROM ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55
COPY --from=golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN sed -i 's|http://|https://|g' /etc/apt/sources.list.d/ubuntu.sources \
    && apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    nginx libnginx-mod-stream openssl python3 ca-certificates systemd ufw iproute2 \
    && rm -rf /var/lib/apt/lists/*
COPY scripts/setup.sh scripts/setup_test.py /test/
COPY scripts/native-smoke.py /test/native-smoke.py
CMD ["python3", "/test/native-smoke.py"]
