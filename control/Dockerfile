# MDD Sim Gateway control-plane image: FastAPI manager + React WebUI, in one container.
#
# Build context = repo ROOT (build with: docker build -f control/Dockerfile -t mdd-sim-gateway/control .)
# Multi-arch (amd64/arm64) and fully from source: the WebUI is compiled in stage 1, Python
# deps are installed in stage 2 — no Node or Python toolchain needed on the host.
#
# Runtime model: runs PRIVILEGED with the host Docker socket (/var/run/docker.sock) and the
# host pcscd socket (/run/pcscd) bind-mounted; it launches/《manages》sibling engine containers
# via the Docker SDK and reads the physical reader via pyscard. The repo layout is mirrored
# under /app so run.py (`from app import ...`) resolves.

# ---------- stage 1: build the WebUI ----------
# The output is architecture-independent static content. Keep this stage on the native build
# platform so an amd64 GitHub runner does not run npm under ARM64 QEMU during release builds.
FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:c610fcdfb1d5b4740dd70c284ed3cb16bb857e0f7166196e36a5501df7a3aa32 AS webui
WORKDIR /webui
# Install deps against the lockfile first (better layer caching), then build.
COPY webui/package.json webui/package-lock.json ./
RUN npm ci
COPY webui/ ./
RUN npm run build

# ---------- stage 2: python runtime ----------
FROM python:3.12-slim@sha256:57cd7c3a7a273101a6485ba99423ee568157882804b1124b4dd04266317710de
ARG MDD_VERSION=dev
# pcsc-lite client library, built from source and pinned to the SAME version as the host pcscd
# and the engine image (PCSC_VERSION). pyscard loads libpcsclite.so.1 at runtime to detect the
# reader via the mounted host /run/pcscd socket — matching versions avoids protocol mismatch.
ARG PCSC_VERSION=2.3.3
ARG PCSC_SHA256=00b667aa71504ed1d39a48ad377de048c70dbe47229e8c48a3239ab62979c70f
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends \
        swig gcc python3-dev pkg-config \
        wget curl unzip ca-certificates meson ninja-build flex perl \
        modemmanager libpcsclite-dev libpcsclite1 pcscd libccid vsmartcard-vpcd libcurl4 \
    && cd /tmp \
    && ARCH="${TARGETARCH:-$(uname -m)}" \
    && if [ "$ARCH" = "arm64" ] || [ "$ARCH" = "aarch64" ]; then \
           LPAC_NAME="lpac-linux-aarch64.zip"; \
           SINGBOX_NAME="sing-box-1.11.4-linux-arm64.tar.gz"; \
       else \
           LPAC_NAME="lpac-linux-x86_64.zip"; \
           SINGBOX_NAME="sing-box-1.11.4-linux-amd64.tar.gz"; \
       fi \
    && curl -sSL -o lpac.zip "https://github.com/estkme-group/lpac/releases/download/v2.3.0/${LPAC_NAME}" \
    && unzip -q -o lpac.zip -d /tmp/lpac_extracted \
    && mv /tmp/lpac_extracted/lpac /usr/local/bin/lpac \
    && chmod +x /usr/local/bin/lpac \
    && rm -rf /tmp/lpac* \
    && curl -sSL -o singbox.tar.gz "https://github.com/SagerNet/sing-box/releases/download/v1.11.4/${SINGBOX_NAME}" \
    && tar xzf singbox.tar.gz \
    && mv sing-box-*/sing-box /usr/local/bin/sing-box \
    && chmod +x /usr/local/bin/sing-box \
    && rm -rf sing-box* singbox.tar.gz \
    && rm -rf /var/lib/apt/lists/*



WORKDIR /app/control
COPY control/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir -r requirements.txt \
    && apt-get purge -y swig gcc python3-dev pkg-config meson ninja-build flex \
    && apt-get autoremove -y

# App code + built WebUI, mirroring the repo layout under /app.
COPY control/ /app/control/
COPY host/ /app/host/
COPY VERSION /app/VERSION
COPY --from=webui /webui/dist /app/webui/dist


RUN chmod +x /app/control/docker-entrypoint.sh

# Defaults; the installer overrides MDD_DATA/MDD_HOST_DATA/MDD_ADVERTISE_ADDR/port.
ENV MDD_WEBUI=/app/webui/dist \
    MDD_DATA=/data \
    MDD_HTTP_PORT=8443 \
    PYTHONUNBUFFERED=1

EXPOSE 8443 35963
LABEL io.mdd-sim-gateway.managed="true" \
      org.opencontainers.image.version="${MDD_VERSION}" \
      org.opencontainers.image.title="MDD Sim Gateway control plane"
ENTRYPOINT ["/app/control/docker-entrypoint.sh"]

