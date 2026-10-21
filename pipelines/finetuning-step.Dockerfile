# The workload base and static component binary are independently pinned.
ARG WORKLOAD_IMAGE
FROM ${WORKLOAD_IMAGE}
ARG SOURCE_REVISION
ARG SOURCE_TREE
LABEL org.opencontainers.image.source="https://github.com/zhangzhe-ctrl/ani-modeldev-service" \
      org.opencontainers.image.revision="${SOURCE_REVISION}" \
      io.ani.source-tree="${SOURCE_TREE}"
COPY --chmod=755 ani-modeldev-step /ani-modeldev-step
USER 10001:10001
ENTRYPOINT ["/ani-modeldev-step"]
