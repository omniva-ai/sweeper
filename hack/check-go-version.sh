#!/usr/bin/env bash
# Copyright 2026 Omniva.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MISE_TOML="${MISE_TOML:-${root}/mise.toml}"
GO_MOD="${GO_MOD:-${root}/go.mod}"
DOCKERFILE="${DOCKERFILE:-${root}/Dockerfile}"

for f in "${MISE_TOML}" "${GO_MOD}" "${DOCKERFILE}"; do
	if [[ ! -r "${f}" ]]; then
		echo "error: cannot read ${f}" >&2
		exit 1
	fi
done

mise_go="$(awk '
	/^\[/ { in_tools = ($0 ~ /^\[tools\][[:space:]]*$/); next }
	in_tools && /^go[[:space:]]*=/ {
		sub(/^go[[:space:]]*=[[:space:]]*"/, ""); sub(/".*$/, ""); print; exit
	}
' "${MISE_TOML}")"
gomod_go="$(awk '$1 == "go" { print $2; exit }' "${GO_MOD}")"
docker_go="$(sed -nE 's/^FROM[[:space:]].*golang:([^[:space:]@]+).*/\1/p' "${DOCKERFILE}" | head -n 1)"

if [[ -z "${mise_go}" || -z "${gomod_go}" || -z "${docker_go}" ]]; then
	echo "error: cannot find the Go version in every file:" >&2
	fail=1
elif [[ "${mise_go}" != "${gomod_go}" || "${mise_go}" != "${docker_go}" ]]; then
	echo "error: Go versions differ:" >&2
	fail=1
else
	echo "Go ${mise_go} in mise.toml, go.mod and Dockerfile"
	exit 0
fi

{
	echo "  ${MISE_TOML}: ${mise_go:-<missing>}"
	echo "  ${GO_MOD}: ${gomod_go:-<missing>}"
	echo "  ${DOCKERFILE}: ${docker_go:-<missing>}"
	echo "Set the same Go version in all three files. See CONTRIBUTING.md."
} >&2
exit "${fail}"
