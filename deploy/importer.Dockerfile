# The importer is a static Go binary. It reaches the public hubs over HTTPS and
# the platform object store through a mounted credential file, so it needs a
# root CA store; it needs no Python, no shell and no training toolchain.
FROM alpine:3.22 AS ca
RUN apk add --no-cache ca-certificates

FROM scratch
ARG SOURCE_REVISION
ARG SOURCE_TREE
LABEL org.opencontainers.image.source="https://github.com/zhangzhe-ctrl/ani-modeldev-service" \
      org.opencontainers.image.revision="${SOURCE_REVISION}" \
      io.ani.source-tree="${SOURCE_TREE}"
COPY --from=ca /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 ani-modeldev-importer /ani-modeldev-importer
USER 10001:10001
ENTRYPOINT ["/ani-modeldev-importer"]
