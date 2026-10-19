ARG WORKLOAD_IMAGE
FROM ${WORKLOAD_IMAGE}
COPY core-flow-driver /core-flow-driver
USER 10001:10001
ENTRYPOINT ["/core-flow-driver", "-test.run=^TestFinetuningCoreFlow$", "-test.v", "-test.timeout=3h5m"]
