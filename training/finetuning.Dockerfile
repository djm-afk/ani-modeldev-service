FROM docker.io/library/python@sha256:83f339c1be6340ae1096010fdccf6552ac932d8f410d45d206014916bdf37e48
COPY training/finetuning-requirements.txt /tmp/requirements.txt
RUN python -m venv /opt/venv && /opt/venv/bin/pip install --no-cache-dir --extra-index-url https://download.pytorch.org/whl/cpu -r /tmp/requirements.txt
RUN /opt/venv/bin/pip install --no-cache-dir pyarrow==20.0.0
RUN groupadd --gid 10001 modeldev && useradd --uid 10001 --gid 10001 --no-create-home modeldev
COPY training/finetuning/ /opt/finetuning/
COPY --chmod=0444 training/implementation.py /opt/implementation/implementation.py
COPY --chmod=0444 contract/cpup01/implementations.json /opt/implementation/implementations.json
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 OMP_NUM_THREADS=4 MKL_NUM_THREADS=4 CUDA_VISIBLE_DEVICES="" WORLD_SIZE=1 HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 HF_HOME=/tmp/huggingface MPLCONFIGDIR=/tmp/matplotlib
USER 10001:10001
WORKDIR /opt/finetuning
ENTRYPOINT ["/opt/venv/bin/python", "-I", "/opt/finetuning/finetune_entry.py"]
