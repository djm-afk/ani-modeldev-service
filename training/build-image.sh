#!/usr/bin/env bash
# Fedora only. Build from immutable Git source and previously locked local wheels.
set -euo pipefail
if [[ $# != 3 ]]; then
    printf 'usage: bash training/build-image.sh FULL_SOURCE_SHA WHEELHOUSE NEW_RUN_DIRECTORY\n' >&2
    exit 2
fi
source_sha=$1
wheelhouse=$(realpath -e -- "$2")
run_dir=$3
[[ $source_sha =~ ^[0-9a-f]{40}$ ]]
[[ $run_dir == /* && ! -e $run_dir && ! -L $run_dir ]]
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
source_root=$(git -C "$script_dir" rev-parse --show-toplevel)
[[ $(git -C "$source_root" rev-parse HEAD) == "$source_sha" ]]
git -C "$source_root" diff --exit-code HEAD -- training/ contract/cpup01/implementations.json
umask 077
mkdir -- "$run_dir"
run_dir=$(realpath -e -- "$run_dir")
exec > >(tee "$run_dir/build.log") 2>&1
trap 'build_exit=$?; printf "%s\n" "$build_exit" > "$run_dir/build.exit"' EXIT
printf '%s\n' "$source_sha" > "$run_dir/source.sha"
date -u +%FT%TZ > "$run_dir/started-at.txt"
podman --version > "$run_dir/podman-version.txt"
mkdir -- "$run_dir/context"
git -C "$source_root" archive --format=tar "$source_sha" training/ contract/cpup01/implementations.json > "$run_dir/source.tar"
tar -xf "$run_dir/source.tar" -C "$run_dir/context"
mkdir -- "$run_dir/context/wheelhouse"
wheel_count=0
while read -r digest filename; do
    [[ $digest =~ ^[0-9a-f]{64}$ && $filename =~ ^[A-Za-z0-9_.+-]+\.whl$ ]]
    [[ -f "$wheelhouse/$filename" && ! -L "$wheelhouse/$filename" ]]
    cp -- "$wheelhouse/$filename" "$run_dir/context/wheelhouse/$filename"
    wheel_count=$((wheel_count + 1))
done < "$run_dir/context/training/wheelhouse.sha256"
[[ $wheel_count -eq 10 ]]
(
    cd -- "$run_dir/context/wheelhouse"
    sha256sum --check ../training/wheelhouse.sha256
) > "$run_dir/wheels-verified.txt"
sha256sum "$run_dir/source.tar" > "$run_dir/source-archive.sha256"
image_tag="localhost/ani-cpu03:${source_sha:0:12}"
command=(podman build --pull=never --network none --http-proxy=false --platform linux/amd64
    --cpu-period 100000 --cpu-quota 200000 --memory 2g --memory-swap 2g
    --cap-drop all --security-opt no-new-privileges --jobs 1
    --label "org.opencontainers.image.revision=$source_sha"
    --iidfile "$run_dir/image.id" --tag "$image_tag"
    --file "$run_dir/context/training/Dockerfile" "$run_dir/context")
printf '%q ' "${command[@]}" > "$run_dir/build-command.txt"
printf '\n' >> "$run_dir/build-command.txt"
"${command[@]}"
image_id=$(cat "$run_dir/image.id")
podman image inspect "$image_id" > "$run_dir/image-inspect.json"
printf '%s\n' "$image_tag" > "$run_dir/image-tag.txt"
printf 'Built local image %s from %s; registry push has not run.\n' "$image_id" "$source_sha"
