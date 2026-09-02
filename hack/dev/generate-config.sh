#!/usr/bin/env bash

# Copyright 2026 The Yukimi Authors.
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

set -e

# Local Config Generator
#
# Renders base.yaml and backplane.yaml - read by internal/config/base and
# internal/config/backplane at startup - from hack/helpers/config/*.yaml.tmpl
# using .env values, into _output/config/. Not yet consumed by anything:
# cmd/provider/main.go has no --configDir flag yet.

# Colors for output
BLU='\033[0;34m'
GRN='\033[0;32m'
YLW='\033[0;33m'
RED='\033[0;31m'
NOC='\033[0m'

echo_info() {
    printf "${BLU}%s${NOC}\n" "$1"
}

echo_success() {
    printf "${GRN}%s${NOC}\n" "$1"
}

echo_warn() {
    printf "${YLW}%s${NOC}\n" "$1"
}

echo_error() {
    printf "${RED}%s${NOC}\n" "$1"
    exit 1
}

# Determine script directory and project root
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
ENV_FILE="${PROJECT_ROOT}/.env"
TEMPLATE_DIR="${PROJECT_ROOT}/hack/helpers/config"
CONFIG_DIR="${CONFIG_DIR:-${PROJECT_ROOT}/_output/config}"
GOMPLATE_BIN="${GOMPLATE:-gomplate}"

# Load .env file if it exists (ignore error if file doesn't exist - CI may use env vars)
if [ -f "${ENV_FILE}" ]; then
    echo_info "Loading .env file from ${ENV_FILE}"
    set -a
    source "${ENV_FILE}" 2>/dev/null || true
    set +a
else
    echo_info ".env file not found at ${ENV_FILE} - using environment variables"
fi

# Check gomplate is available
if ! command -v "${GOMPLATE_BIN}" &> /dev/null; then
    echo_error "gomplate not found. Run 'make generate-config' (which installs it), or install gomplate yourself and re-run this script."
fi

# Verify required base.yaml inputs are set
REQUIRED_VARS=(SNOWFLAKE_ORG SNOWFLAKE_ORG_ADMIN_ACCOUNT SNOWFLAKE_ORG_ADMIN_ACCOUNT_LOCATOR SNOWFLAKE_ORG_ADMIN_ACCOUNT_REGION AWS_REGION)
MISSING_VARS=()
for var in "${REQUIRED_VARS[@]}"; do
    if [ -z "${!var}" ]; then
        MISSING_VARS+=("$var")
    fi
done
if [ ${#MISSING_VARS[@]} -gt 0 ]; then
    echo_error "Missing required variable(s) in .env: ${MISSING_VARS[*]} - see .env.example"
fi

echo_info "Creating ${CONFIG_DIR}..."
mkdir -p "${CONFIG_DIR}"

echo_info "Rendering base.yaml..."
"${GOMPLATE_BIN}" < "${TEMPLATE_DIR}/base.yaml.tmpl" > "${CONFIG_DIR}/base.yaml"

echo_info "Rendering backplane.yaml..."
"${GOMPLATE_BIN}" < "${TEMPLATE_DIR}/backplane.yaml.tmpl" > "${CONFIG_DIR}/backplane.yaml"

echo_success "✓ Local-dev config files generated"
echo_info "base.yaml:      ${CONFIG_DIR}/base.yaml"
echo_info "backplane.yaml: ${CONFIG_DIR}/backplane.yaml"
echo_info "Region marked available in backplane.yaml: ${SNOWFLAKE_ORG_ADMIN_ACCOUNT_REGION}"
