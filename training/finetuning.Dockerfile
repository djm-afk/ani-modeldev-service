# Prepared official hiyouga/llamafactory latest AMD64, upstream digest verified.
FROM 10.10.1.66:30003/ani-resource-factor10-20261022/llamafactory@sha256:eb5e106d93e47532e4b72f5995232624b704edea1c1c79468ddcd2ceffa14b4b
ARG ANI_DEVICE_MODE
USER 0:0
COPY --chmod=0755 bin/ani-modeldev-step /ani-modeldev-step
COPY --chmod=0444 training/train_mlp.py training/implementation.py training/implementations.json training/dataset_manifest.py training/structure_limits.json /opt/ani-modeldev/training/
COPY training/finetuning/ /opt/ani-modeldev/training/finetuning/
RUN test "$ANI_DEVICE_MODE" = cpu -o "$ANI_DEVICE_MODE" = gpu
RUN /opt/conda/bin/python -I /opt/ani-modeldev/training/finetuning/runtime-patches/apply.py /app
RUN /opt/conda/bin/python -I -c 'from pathlib import Path; import os; p=Path("/etc/passwd");g=Path("/etc/group"); users=p.read_bytes();groups=g.read_bytes();assert not any(r.split(b":")[0]==b"modeldev" or r.split(b":")[2]==b"10001" for r in users.splitlines() if r and not r.startswith(b"#"));assert not any(r.split(b":")[0]==b"modeldev" or r.split(b":")[2]==b"10001" for r in groups.splitlines() if r and not r.startswith(b"#"));p.write_bytes(users+b"modeldev:x:10001:10001:ModelDev training:/nonexistent:/usr/sbin/nologin\n");g.write_bytes(groups+b"modeldev:x:10001:\n");root=Path("/opt/ani-modeldev");[(os.chmod(v,0o755) if v.is_dir() else os.chmod(v,0o444)) for v in (root,*root.rglob("*"))]'
LABEL ani.modeldev.device_mode=$ANI_DEVICE_MODE
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 WORLD_SIZE=1 HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1
USER 10001:10001
WORKDIR /opt/ani-modeldev
ENTRYPOINT ["/ani-modeldev-step"]
CMD []
