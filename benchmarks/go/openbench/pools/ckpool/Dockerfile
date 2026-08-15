FROM debian:trixie-slim AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential yasm libzmq3-dev autoconf automake libtool pkgconf \
        ca-certificates git && rm -rf /var/lib/apt/lists/*
WORKDIR /src
ARG CKPOOL_REPO=https://bitbucket.org/ckolivas/ckpool.git
ARG CKPOOL_REF=master
RUN git clone "$CKPOOL_REPO" . && git checkout "$CKPOOL_REF" \
    && ./autogen.sh && ./configure && make -j"$(nproc)"

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends libzmq5 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /src/src/ckpool /usr/local/bin/ckpool
RUN useradd -m -d /ckpool ckpool && mkdir -p /config /ckpool/logs \
    && chown -R ckpool:ckpool /ckpool /config
USER ckpool
WORKDIR /ckpool
EXPOSE 3333
ENTRYPOINT ["ckpool"]
# LOG_ERR (3) keeps errors enabled. Deliberately omit -L, which is CKPool's
# per-share disk logging switch.
CMD ["-B", "-l", "3", "-c", "/config/ckpool.conf"]
