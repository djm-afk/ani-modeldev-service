FROM docker.io/library/python@sha256:83f339c1be6340ae1096010fdccf6552ac932d8f410d45d206014916bdf37e48
COPY training/gpu-requirements.txt /tmp/requirements.txt
RUN python -m venv /opt/venv && /opt/venv/bin/pip install --no-cache-dir --extra-index-url https://download.pytorch.org/whl/cu124 -r /tmp/requirements.txt
COPY training/gpu-runtime-lock.json /opt/gpu-runtime-lock.json
# Consume the complete resolved lock: a future dependency drift fails the build.
RUN /opt/venv/bin/python -I -c 'import importlib.metadata as m,json,sys; lock=json.load(open("/opt/gpu-runtime-lock.json")); actual={d.metadata["Name"]:d.version for d in m.distributions()}; assert sys.version.split()[0]==lock["python"] and actual==lock["packages"], "GPU runtime dependency lock mismatch"' && /opt/venv/bin/pip check
RUN groupadd --gid 10001 modeldev && useradd --uid 10001 --gid 10001 --no-create-home modeldev
COPY --chmod=0444 training/train_mlp.py training/implementation.py /opt/cpu03/
COPY --chmod=0444 training/implementation.py /opt/implementation/implementation.py
COPY --chmod=0444 contract/cpup01/implementations.json /opt/implementation/implementations.json
COPY training/finetuning/ /opt/finetuning/
# COPY --chmod also affects a newly created destination directory.
RUN ["/opt/venv/bin/python", "-I", "-c", "import os; os.chmod('/opt/cpu03', 0o755); os.chmod('/opt/implementation', 0o755)"]
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 WORLD_SIZE=1 HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 HF_HOME=/tmp/huggingface MPLCONFIGDIR=/tmp/matplotlib
USER 10001:10001
WORKDIR /opt/cpu03
ENTRYPOINT ["/opt/venv/bin/python", "-I", "/opt/cpu03/train_mlp.py"]
