# VoWiFi Engine: pinned sysmocom Asterisk + userspace SWu IKEv2/IPsec + physical USIM.
# Builder and runtime deliberately share one dated Debian stable rootfs. Source trees,
# compilers, headers and package caches never cross into the runtime stage.
ARG DEBIAN_BASE=debian:trixie-20260824-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132

FROM ${DEBIAN_BASE} AS builder
ARG DEBIAN_SNAPSHOT=20260824T000000Z
ARG PCSC_VERSION=2.3.3
ARG PCSC_SHA256=00b667aa71504ed1d39a48ad377de048c70dbe47229e8c48a3239ab62979c70f
ENV DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Debian's signed snapshot makes every apt dependency stable across rebuilds and architectures.
# HTTP avoids deployment-proxy TLS interception; apt still verifies the archive Release signature.
RUN printf '%s\n' \
      "deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT} trixie main" \
      "deb [check-valid-until=no] http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT} trixie-security main" \
      > /etc/apt/sources.list \
    && rm -f /etc/apt/sources.list.d/debian.sources \
    && apt-get -o APT::Update::Error-Mode=any update \
    && apt-get install -y --no-install-recommends \
      autoconf automake binutils bison build-essential ca-certificates curl file flex gettext git \
      gperf libcap-dev libcodec2-dev libcurl4-openssl-dev libedit-dev libffi-dev libgsm1-dev \
      libical-dev libjansson-dev libmnl-dev libncurses-dev libnewt-dev libogg-dev \
      libopencore-amrnb-dev libopencore-amrwb-dev libopus-dev libpcsclite-dev libpopt-dev \
      libresample1-dev libsamplerate0-dev libspeex-dev libspeexdsp-dev libspandsp-dev \
      libsqlite3-dev libsrtp2-dev libssl-dev libsystemd-dev libtool libunbound-dev \
      liburiparser-dev libuuid1 uuid-dev libvo-amrwbenc-dev libvorbis-dev libxml2-dev \
      libxslt1-dev m4 meson ninja-build patch perl pkg-config python3 python3-dev python3-pip \
      python3-setuptools python3-venv subversion swig wget xmlstarlet xz-utils \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /home/asterisk-build

# The Engine is a PC/SC client of the host pcscd. Keep this version identical to Control and
# the host installer; upgrading just this stage would break the wire protocol contract.
RUN curl -fL --retry 3 --retry-delay 5 -o /tmp/pcsc.tar.gz \
      "https://github.com/LudovicRousseau/PCSC/archive/refs/tags/${PCSC_VERSION}.tar.gz" \
    && echo "${PCSC_SHA256}  /tmp/pcsc.tar.gz" | sha256sum -c - \
    && tar -C /tmp -xf /tmp/pcsc.tar.gz \
    && meson setup "/tmp/PCSC-${PCSC_VERSION}/builddir" "/tmp/PCSC-${PCSC_VERSION}" \
      --prefix=/usr -Dpolkit=false -Dlibudev=false -Dlibsystemd=false \
      -Dusb=false -Dserial=false \
    && ninja -C "/tmp/PCSC-${PCSC_VERSION}/builddir" \
    && ninja -C "/tmp/PCSC-${PCSC_VERSION}/builddir" install \
    && DESTDIR=/engine-root ninja -C "/tmp/PCSC-${PCSC_VERSION}/builddir" install \
    && ldconfig \
    && rm -rf /tmp/pcsc.tar.gz "/tmp/PCSC-${PCSC_VERSION}"

# sysmocom pjproject + Asterisk (VoLTE/VoWiFi/IMS-AKA/SMS-over-IMS fork). Every source
# revision and local patch is unchanged from the previously validated Engine image.
COPY patches/asterisk/ /home/asterisk-build/patches/asterisk/
RUN set -eu; \
    checkout_with_retry() { \
        url="$1"; destination="$2"; revision="$3"; attempt=1; \
        while [ "$attempt" -le 3 ]; do \
            rm -rf "$destination"; \
            git init -q "$destination"; \
            git -C "$destination" remote add origin "$url"; \
            if git -C "$destination" -c http.version=HTTP/1.1 fetch --depth=1 origin "$revision" && \
                git -C "$destination" checkout --detach FETCH_HEAD; then return 0; fi; \
            echo "checkout attempt ${attempt}/3 failed for ${revision}; retrying" >&2; \
            attempt=$((attempt + 1)); sleep 5; \
        done; \
        return 1; \
    }; \
    fetch_with_retry() { \
        repository="$1"; revision="$2"; depth="$3"; attempt=1; \
        while [ "$attempt" -le 3 ]; do \
            if git -C "$repository" -c http.version=HTTP/1.1 fetch --depth="$depth" origin "$revision"; then return 0; fi; \
            echo "fetch attempt ${attempt}/3 failed for ${revision}; retrying" >&2; \
            attempt=$((attempt + 1)); sleep 5; \
        done; \
        return 1; \
    }; \
    checkout_with_retry https://gitea.sysmocom.de/sysmocom/pjproject.git pjproject 20537ab1e6beb2baf9ec15599e0217a601cf7ce5 && \
    checkout_with_retry https://gitea.sysmocom.de/sysmocom/asterisk.git asterisk d231cb2c658545773fcd5eb9de787219b9ef6566 && \
    cd asterisk && \
    git config user.email "build@vowifi" && git config user.name "vowifi build" && \
    fetch_with_retry . f1b60dcd9568c4045512fd0d8b619b9fb91a7f35 2 && \
    git cherry-pick f1b60dcd9568c4045512fd0d8b619b9fb91a7f35 && \
    for p in /home/asterisk-build/patches/asterisk/*.py; do echo "applying $p"; python3 "$p"; done && \
    ws_patch=/home/asterisk-build/patches/asterisk/chan_websocket/0001-Backport-Asterisk-media-WebSocket-support-to-sysmoco.patch && \
    echo "376de91aff682a31c3f865476415419d947364b200e9c21b20f1ffb58bf884aa  $ws_patch" | sha256sum -c - && \
    git apply --check "$ws_patch" && git apply "$ws_patch" && \
    ws_lifecycle_patch=/home/asterisk-build/patches/asterisk/chan_websocket/0002-Fix-masqueraded-owner-and-EOF-lifecycle.patch && \
    echo "fd024503eaf44d69ced45b2dfd144040fcc06481299760bad06dcfdbb798420b  $ws_lifecycle_patch" | sha256sum -c - && \
    git apply --check "$ws_lifecycle_patch" && git apply "$ws_lifecycle_patch" && \
    ln -s ../../../pjproject third-party/pjproject/source && \
    sh bootstrap.sh && \
    ./configure --enable-binary-modules --prefix=/usr --sysconfdir=/etc --localstatedir=/var && \
    make menuselect/menuselect menuselect-tree menuselect.makeopts && \
    ./menuselect/menuselect --enable codec_opus --enable chan_websocket \
      --enable res_websocket_client --enable res_http_websocket \
      --enable app_mdd_answer_bridged --disable BUILD_NATIVE menuselect.makeopts && \
    ! grep '^MENUSELECT_CODECS=' menuselect.makeopts | tr ' ' '\n' | grep -qx codec_opus && \
    make -j"$(nproc)" && \
    make DESTDIR=/engine-root install

# Keep only runtime payloads. Asterisk creates cache/log/run/spool directories during install,
# but those belong to each container incarnation and must not collide with Debian's /var/run
# symlink or become image state.
RUN rm -rf \
      /engine-root/etc /engine-root/usr/include /engine-root/usr/share/doc \
      /engine-root/usr/share/man /engine-root/usr/share/metainfo \
      /engine-root/usr/bin/pcsc-spy /engine-root/usr/sbin/pcscd \
      /engine-root/var/cache /engine-root/var/log /engine-root/var/run \
      /engine-root/var/spool \
    && find /engine-root/usr/lib -type f \( -name '*.a' -o -name '*.la' \) -delete

# One copied venv keeps Python modules independent from the runtime distro package set. Direct
# and transitive versions are fixed in requirements.txt; the card helper remains commit-pinned.
COPY requirements.txt /home/asterisk-build/requirements.txt
RUN python3 -m venv /opt/mdd-venv \
    && /opt/mdd-venv/bin/pip install --no-cache-dir -r /home/asterisk-build/requirements.txt \
    && /opt/mdd-venv/bin/pip install --no-cache-dir \
      "git+https://github.com/mitshell/card.git@790a8fb0b8fbc0b06f568feba51aeef28352145e"

# Resolve Debian owners of every library used by Asterisk modules and Python native extensions.
COPY collect-runtime-packages.sh /usr/local/bin/collect-runtime-packages.sh
RUN bash /usr/local/bin/collect-runtime-packages.sh /runtime-packages.txt \
      /engine-root /opt/mdd-venv \
    && test -s /runtime-packages.txt


FROM ${DEBIAN_BASE} AS runtime
ARG DEBIAN_SNAPSHOT=20260824T000000Z
ENV DEBIAN_FRONTEND=noninteractive \
    VIRTUAL_ENV=/opt/mdd-venv \
    PATH=/opt/mdd-venv/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    PYTHONUNBUFFERED=1
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

COPY --from=builder /runtime-packages.txt /tmp/runtime-packages.txt
RUN printf '%s\n' \
      "deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT} trixie main" \
      "deb [check-valid-until=no] http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT} trixie-security main" \
      > /etc/apt/sources.list \
    && rm -f /etc/apt/sources.list.d/debian.sources \
    && apt-get -o APT::Update::Error-Mode=any update \
    && apt-get install -y --no-install-recommends \
      bash ca-certificates coreutils dnsutils hostname iproute2 iputils-ping net-tools procps \
      python3 tcpdump $(cat /tmp/runtime-packages.txt) \
    && rm -rf /var/lib/apt/lists/* /tmp/runtime-packages.txt

COPY --from=builder /engine-root/usr/ /usr/
COPY --from=builder /engine-root/var/lib/ /var/lib/
COPY --from=builder /opt/mdd-venv /opt/mdd-venv

COPY pin_keeper.py /usr/local/bin/pin_keeper.py
COPY ami_usim.py /usr/local/bin/ami_usim.py
COPY swu_ike.py /usr/local/bin/swu_ike.py
COPY pcscf_state.py /usr/local/bin/pcscf_state.py
COPY admission_gate.py /usr/local/bin/admission_gate.py
COPY log_capture.py /usr/local/bin/log_capture.py
COPY render.py /usr/local/bin/render.py
COPY notify.py /usr/local/bin/notify.py
COPY config_fetch.py /usr/local/bin/config_fetch.py
COPY templates/ /opt/mdd-sim-gateway/templates/
COPY entrypoint.sh   /entrypoint.sh
COPY engine-runtime.sh /engine-runtime.sh
RUN chmod 0755 /usr/local/bin/*.py /entrypoint.sh /engine-runtime.sh \
    && install -d -m 0700 /config /logs /run/mdd-sim-gateway /etc/asterisk \
      /var/cache/asterisk /var/log/asterisk /var/spool/asterisk /run/asterisk \
    && ldconfig
RUN python3 -c 'import Crypto, cryptography, jinja2, panoramisk, requests, serial, smartcard'
RUN ! command -v gcc >/dev/null \
    && ! command -v git >/dev/null
RUN set -eux; \
    test -x /usr/sbin/asterisk; \
    test -f /usr/lib/asterisk/modules/chan_pjsip.so; \
    test -f /usr/lib/asterisk/modules/codec_amr.so; \
    test -f /usr/lib/asterisk/modules/codec_opus.so; \
    test -f /usr/lib/asterisk/modules/chan_websocket.so; \
    test -f /usr/lib/asterisk/modules/res_websocket_client.so; \
    test -f /usr/lib/asterisk/modules/res_mdd_admission.so; \
    test -f /usr/lib/asterisk/modules/app_mdd_answer_bridged.so

ARG RUNTIME_FP=""
ARG BASE_FP=""
LABEL io.mdd-sim-gateway.managed="true" \
      io.mdd-sim-gateway.runtime-fp="${RUNTIME_FP}" \
      io.mdd-sim-gateway.base-fp="${BASE_FP}" \
      io.mdd-sim-gateway.admission-abi="mdd-admission-v1" \
      io.mdd-sim-gateway.media-websocket="mdd-media-ws-v1" \
      io.mdd-sim-gateway.browser-outbound="mdd-browser-outbound-v1" \
      io.mdd-sim-gateway.browser-inbound="mdd-browser-inbound-v1" \
      io.mdd-sim-gateway.config-service="mdd-config-unix-v1" \
      org.opencontainers.image.base.name="debian:trixie-20260824-slim" \
      org.opencontainers.image.title="MDD Sim Gateway VoWiFi engine"
ENTRYPOINT ["/entrypoint.sh"]
