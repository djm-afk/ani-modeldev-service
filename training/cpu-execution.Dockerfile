# Existing immutable CPU runtime qualified by the actual Fedora A/B/C workloads.
# The repository owns all programs and the single machine parameter contract.
FROM localhost/ani-finetuning@sha256:6a5dffdc41828d51b8de9b6ec45ad413bb98e82f9bbc43fd8e63a0499e2442c3
COPY --chmod=0444 training/train_mlp.py training/implementation.py /opt/cpu03/
COPY --chmod=0444 training/implementation.py /opt/implementation/implementation.py
COPY --chmod=0444 contract/cpup01/implementations.json /opt/implementation/implementations.json
COPY training/finetuning/ /opt/finetuning/
# COPY --chmod also affects a newly created destination directory.
USER 0:0
RUN ["/opt/venv/bin/python", "-I", "-c", "import os; os.chmod('/opt/cpu03', 0o755); os.chmod('/opt/implementation', 0o755)"]
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 CUDA_VISIBLE_DEVICES="" WORLD_SIZE=1 HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 HF_HOME=/tmp/huggingface MPLCONFIGDIR=/tmp/matplotlib
USER 10001:10001
WORKDIR /opt/cpu03
ENTRYPOINT ["/opt/venv/bin/python", "-I", "/opt/cpu03/train_mlp.py"]
