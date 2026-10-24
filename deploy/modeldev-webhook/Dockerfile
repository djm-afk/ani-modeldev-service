# Build the static binary from the verified service source revision first.
FROM scratch
ARG SOURCE_REVISION
ARG SOURCE_TREE
LABEL org.opencontainers.image.source="https://github.com/zhangzhe-ctrl/ani-modeldev-service" \
      org.opencontainers.image.revision="${SOURCE_REVISION}" \
      io.ani.source-tree="${SOURCE_TREE}"
COPY --chown=10001:10001 ani-modeldev-webhook /usr/local/bin/ani-modeldev-webhook
USER 10001:10001
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/ani-modeldev-webhook"]
CMD ["--config", "/etc/modeldev-webhook/config.json"]
