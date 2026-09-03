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

# AWS Credentials Bridge for Local Development
#
# Bridges the AWS credential gap for local development: the developer's host machine
# has an active AWS CLI session, but pods running inside KIND have no access to it.
# This script exports the current credentials (including SSO temporary tokens) and
# installs them as a Kubernetes secret in the cluster so the yukimi controller can
# reach AWS Secrets Manager.

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

# Load .env file if it exists (ignore error if file doesn't exist - CI may use env vars)
if [ -f "${ENV_FILE}" ]; then
    echo_info "Loading .env file from ${ENV_FILE}"
    set -a
    source "${ENV_FILE}" 2>/dev/null || true
    set +a
else
    echo_info ".env file not found at ${ENV_FILE} - using environment variables"
fi

# Get AWS region (required)
if [ -z "$AWS_REGION" ]; then
    echo_error "AWS_REGION not set - please set it in environment or .env file"
fi

# Configuration
# AWS_PROFILE is optional - only use if explicitly set
NAMESPACE="${NAMESPACE:-crossplane-system}"
SECRET_NAME="${SECRET_NAME:-aws-credentials}"
KUBECTL="${KUBECTL:-kubectl}"

if [ -n "$AWS_PROFILE" ]; then
    echo_info "Setting up AWS credentials for profile: ${AWS_PROFILE}"
else
    echo_info "Setting up AWS credentials (no profile specified, using default)"
fi

# Check if AWS CLI is available
if ! command -v aws &> /dev/null; then
    echo_error "AWS CLI not found. Please install it first."
fi

# Check if kubectl is available
if ! command -v "${KUBECTL}" &> /dev/null; then
    echo_error "kubectl not found. Please install it first."
fi

# Build AWS CLI profile flag if profile is set
PROFILE_FLAG=""
if [ -n "$AWS_PROFILE" ]; then
    PROFILE_FLAG="--profile ${AWS_PROFILE}"
fi

# Verify AWS session is valid
echo_info "Checking AWS session..."
if ! aws sts get-caller-identity $PROFILE_FLAG &> /dev/null; then
    if [ -n "$AWS_PROFILE" ]; then
        echo_warn "AWS session expired or invalid. Please run: aws sso login --profile ${AWS_PROFILE}"
    else
        echo_warn "AWS session expired or invalid. Please ensure AWS credentials are configured."
    fi
    exit 1
fi

echo_success "AWS session is valid"

# Get AWS credentials from the profile
echo_info "Extracting AWS credentials..."
AWS_ACCESS_KEY_ID=$(aws configure get aws_access_key_id $PROFILE_FLAG 2>/dev/null || echo "")
AWS_SECRET_ACCESS_KEY=$(aws configure get aws_secret_access_key $PROFILE_FLAG 2>/dev/null || echo "")
AWS_SESSION_TOKEN=$(aws configure get aws_session_token $PROFILE_FLAG 2>/dev/null || echo "")

# For SSO profiles, we need to get temporary credentials
if [ -z "$AWS_ACCESS_KEY_ID" ]; then
    echo_info "SSO profile detected, getting temporary credentials..."

    # Use aws configure export-credentials to get temporary creds
    CREDS_JSON=$(aws configure export-credentials $PROFILE_FLAG --format process 2>/dev/null || echo "")

    if [ -z "$CREDS_JSON" ]; then
        if [ -n "$AWS_PROFILE" ]; then
            echo_error "Failed to export credentials. Please ensure 'aws sso login --profile ${AWS_PROFILE}' has been run."
        else
            echo_error "Failed to export credentials. Please ensure AWS credentials are configured."
        fi
    fi

    AWS_ACCESS_KEY_ID=$(echo "$CREDS_JSON" | jq -r '.AccessKeyId')
    AWS_SECRET_ACCESS_KEY=$(echo "$CREDS_JSON" | jq -r '.SecretAccessKey')
    AWS_SESSION_TOKEN=$(echo "$CREDS_JSON" | jq -r '.SessionToken // empty')
fi

# Verify we have credentials
if [ -z "$AWS_ACCESS_KEY_ID" ] || [ -z "$AWS_SECRET_ACCESS_KEY" ]; then
    if [ -n "$AWS_PROFILE" ]; then
        echo_error "Failed to extract AWS credentials from profile ${AWS_PROFILE}"
    else
        echo_error "Failed to extract AWS credentials"
    fi
fi

echo_success "Successfully extracted AWS credentials"

# AWS_REGION is already set from environment (validated at start)
echo_info "Using AWS region: ${AWS_REGION}"

# Create credentials file in AWS format
CREDENTIALS_FILE=$(cat <<EOF
[default]
aws_access_key_id = ${AWS_ACCESS_KEY_ID}
aws_secret_access_key = ${AWS_SECRET_ACCESS_KEY}
EOF
)

# Add session token if present
if [ -n "$AWS_SESSION_TOKEN" ]; then
    CREDENTIALS_FILE=$(cat <<EOF
${CREDENTIALS_FILE}
aws_session_token = ${AWS_SESSION_TOKEN}
EOF
)
fi

# Create namespace if it doesn't exist
if ! ${KUBECTL} get namespace "${NAMESPACE}" &> /dev/null; then
    echo_info "Creating namespace ${NAMESPACE}..."
    ${KUBECTL} create namespace "${NAMESPACE}"
fi

# Delete existing secret if it exists
if ${KUBECTL} get secret "${SECRET_NAME}" -n "${NAMESPACE}" &> /dev/null; then
    echo_info "Deleting existing secret ${SECRET_NAME}..."
    ${KUBECTL} delete secret "${SECRET_NAME}" -n "${NAMESPACE}"
fi

# Create Kubernetes secret with AWS credentials
echo_info "Creating Kubernetes secret ${SECRET_NAME} in namespace ${NAMESPACE}..."
${KUBECTL} create secret generic "${SECRET_NAME}" \
    -n "${NAMESPACE}" \
    --from-literal=credentials="${CREDENTIALS_FILE}"

echo_success "✓ AWS credentials secret created successfully"
echo_info "Secret: ${NAMESPACE}/${SECRET_NAME}"
if [ -n "$AWS_PROFILE" ]; then
    echo_info "Profile: ${AWS_PROFILE}"
fi
echo_info "Region: ${AWS_REGION}"
