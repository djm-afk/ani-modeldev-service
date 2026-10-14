# Build the static executable from the same pushed source as the frozen workload.
# The base must be the M0-verified OCI manifest, never a floating tag.
ARG WORKLOAD_IMAGE
FROM ${WORKLOAD_IMAGE}
COPY --chmod=755 ani-modeldev-step /ani-modeldev-step
USER 10001:10001
ENTRYPOINT ["/ani-modeldev-step"]
