# Build the static executable from the fixed source commit on Fedora.
#
# The image is scratch-based, so it carried no CA bundle. That was fine while
# every outbound call went to an internal endpoint whose CA is mounted at
# /etc/modeldev/ca. Dataset import instead reaches the public hubs over HTTPS,
# and without a root store every such handshake fails as an opaque transport
# error. The bundle is copied from the build stage, never fetched at runtime.
FROM alpine:3.22 AS ca
RUN apk add --no-cache ca-certificates

FROM scratch
ARG SOURCE_REVISION
ARG SOURCE_TREE
LABEL org.opencontainers.image.source="https://github.com/zhangzhe-ctrl/ani-modeldev-service" \
      org.opencontainers.image.revision="${SOURCE_REVISION}" \
      io.ani.source-tree="${SOURCE_TREE}"
COPY --from=ca /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 ani-modeldev-service /ani-modeldev-service
USER 10001:10001
ENTRYPOINT ["/ani-modeldev-service"]
