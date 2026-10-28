#!/usr/bin/env bash
# Fedora only. Add platform programs to the immutable official runtime.
set -euo pipefail
if [[ $# != 3 ]]; then
    printf 'usage: bash training/build-image.sh FULL_SOURCE_SHA cpu|gpu NEW_RUN_DIRECTORY\n' >&2
    exit 2
fi
source_sha=$1
device_mode=$2
run_dir=$3
[[ $source_sha =~ ^[0-9a-f]{40}$ ]]
[[ $device_mode == cpu || $device_mode == gpu ]]
[[ $run_dir == /* && ! -e $run_dir && ! -L $run_dir ]]
: "${ANI_IMAGE_STORAGE_ROOT:?set the task-owned Podman storage directory}"
: "${ANI_IMAGE_RUNROOT:?set the task-owned Podman run directory}"
: "${ANI_IMAGE_TMPDIR:?set the task-owned image temporary directory}"
for path in "$ANI_IMAGE_STORAGE_ROOT" "$ANI_IMAGE_RUNROOT" "$ANI_IMAGE_TMPDIR"; do
    [[ $path == /* && -d $path && ! -L $path ]]
done
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
source_root=$(git -C "$script_dir" rev-parse --show-toplevel)
git -C "$source_root" cat-file -e "$source_sha^{commit}"
umask 077
mkdir -- "$run_dir"
run_dir=$(realpath -e -- "$run_dir")
exec > >(tee "$run_dir/build.log") 2>&1
trap 'build_exit=$?; printf "%s\n" "$build_exit" > "$run_dir/build.exit"' EXIT
printf '%s\n' "$source_sha" > "$run_dir/source.sha"
git -C "$source_root" rev-parse "$source_sha^{tree}" > "$run_dir/source-tree.sha"
date -u +%FT%TZ > "$run_dir/started-at.txt"
export TMPDIR=$ANI_IMAGE_TMPDIR
podman=(podman --root "$ANI_IMAGE_STORAGE_ROOT" --runroot "$ANI_IMAGE_RUNROOT" --storage-driver overlay)
"${podman[@]}" --version > "$run_dir/podman-version.txt"
mkdir -- "$run_dir/context"
git -C "$source_root" archive --format=tar "$source_sha" > "$run_dir/source.tar"
tar -xf "$run_dir/source.tar" -C "$run_dir/context"
sha256sum "$run_dir/source.tar" > "$run_dir/source-archive.sha256"
base=$(awk '$1 == "FROM" {print $2}' "$run_dir/context/training/finetuning.Dockerfile")
[[ $base =~ @sha256:[0-9a-f]{64}$ && $base != *$'\n'* ]]
"${podman[@]}" image inspect "$base" > "$run_dir/official-base-inspect.json"
mkdir -- "$run_dir/context/bin"
(
    cd -- "$run_dir/context"
    GOWORK=off GOMAXPROCS=2 GOFLAGS=-p=2 GOMEMLIMIT=2GiB CGO_ENABLED=0 \
        go build -buildvcs=false -trimpath -o bin/ani-modeldev-step ./cmd/ani-modeldev-step
)
sha256sum "$run_dir/context/bin/ani-modeldev-step" > "$run_dir/step-binary.sha256"
# The archive contains only Git source. Include the binary just built from it.
printf '%s\n' .git .scratch .private .tools .work .tmp '**/__pycache__' '**/*.pyc' > "$run_dir/build.ignore"
image_tag="localhost/ani-modeldev-training-${device_mode}:${source_sha:0:12}"
command=("${podman[@]}" build --pull=never --network none --http-proxy=false --platform linux/amd64
    --cpu-period 100000 --cpu-quota 200000 --memory 2g --memory-swap 2g
    --cap-drop all --security-opt no-new-privileges --jobs 1
    --build-arg "ANI_DEVICE_MODE=$device_mode"
    --label "org.opencontainers.image.revision=$source_sha"
    --iidfile "$run_dir/image.id" --tag "$image_tag"
    --ignorefile "$run_dir/build.ignore"
    --file "$run_dir/context/training/finetuning.Dockerfile" "$run_dir/context")
printf '%q ' "${command[@]}" > "$run_dir/build-command.txt"
printf '\n' >> "$run_dir/build-command.txt"
"${command[@]}"
image_id=$(cat "$run_dir/image.id")
"${podman[@]}" image inspect "$image_id" > "$run_dir/image-inspect.json"
printf '%s\n' "$image_tag" > "$run_dir/image-tag.txt"
printf 'Built local %s image %s from %s; registry publication and qualification remain separate.\n' "$device_mode" "$image_id" "$source_sha"
