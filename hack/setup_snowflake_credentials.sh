#!/usr/bin/env bash
#
# setup-snowflake-credentials.sh - Generate and store RSA key pairs for Snowflake
#
# This script generates RSA key pairs following Snowflake's documentation:
# - Private key: PKCS#8 format with PEM delimiters (required by Snowflake driver)
# - Public key: Single line without PEM delimiters (required by ALTER USER command)
#
# Keys are stored in AWS Secrets Manager at paths defined in specs/design.md
# section 3.11.1 (Tenant Isolation via Secret Paths)
#

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Script directory and repo root
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"

# Load .env file if it exists
if [[ -f "${ENV_FILE}" ]]; then
    echo -e "${BLUE}Loading configuration from ${ENV_FILE}${NC}"
    set -a
    # shellcheck disable=SC1090
    source "${ENV_FILE}"
    set +a
else
    echo -e "${YELLOW}Warning: ${ENV_FILE} not found. Using environment variables only.${NC}"
fi

# Configuration from environment
AWS_REGION="${AWS_REGION:-eu-central-1}"
AWS_PROFILE="${AWS_PROFILE:-}"
SNOWFLAKE_ORG="${SNOWFLAKE_ORG:-}"
SNOWFLAKE_ORG_ADMIN_ACCOUNT="${SNOWFLAKE_ORG_ADMIN_ACCOUNT:-orgadmin}"
SAMPLE_CUSTOMER_ACCOUNT="${SAMPLE_CUSTOMER_ACCOUNT:-platform_dev_internal}"
SAMPLE_CUSTOMER_NAMESPACE="${SAMPLE_CUSTOMER_NAMESPACE:-default}"

# Fixed username for all secrets
USERNAME="platform"

# Command line flags
OVERWRITE=false
SKIP_CONFIRMATION=false
DRY_RUN=false
GENERATE_TEST_KEYS=false

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --overwrite)
            OVERWRITE=true
            shift
            ;;
        --yes|-y)
            SKIP_CONFIRMATION=true
            shift
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --generate-test-keys)
            GENERATE_TEST_KEYS=true
            shift
            ;;
        --help|-h)
            cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Generate and store RSA key pairs for Snowflake.

By default, only org admin credentials are generated (production mode).
Use --generate-test-keys to also generate tenant test credentials (development mode).

Options:
  --overwrite              Overwrite existing secrets in AWS Secrets Manager
  --generate-test-keys     Also generate tenant test credentials for development/testing
  --dry-run                Show what would be created without making changes
  --yes, -y                Skip confirmation prompts
  --help, -h               Show this help message

Environment Variables (from .env or environment):
  AWS_REGION                          AWS region for Secrets Manager (default: eu-central-1)
  AWS_PROFILE                         AWS profile to use (optional)
  SNOWFLAKE_ORG                       Snowflake organization name (required)
  SNOWFLAKE_ORG_ADMIN_ACCOUNT         Org admin account name (default: orgadmin)

  The following are only required with --generate-test-keys:
  SAMPLE_CUSTOMER_NAMESPACE            Simulated customer's namespace (default: default)
  SAMPLE_CUSTOMER_ACCOUNT              Simulated customer's account name (default: platform_dev_internal)

Generated Secrets:
  Always:
    - snowflake/org/<org>/<org-admin-account>/org-admin-credentials

  With --generate-test-keys:
    - snowflake/tenant/<org>/<namespace>/<account>/platform-credentials

EOF
            exit 0
            ;;
        *)
            echo -e "${RED}Error: Unknown option $1${NC}"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Validate required configuration
if [[ -z "${SNOWFLAKE_ORG}" ]]; then
    echo -e "${RED}Error: SNOWFLAKE_ORG is required${NC}"
    echo "Set it in .env file or environment variable"
    exit 1
fi

if [[ -z "${AWS_REGION}" ]]; then
    echo -e "${RED}Error: AWS_REGION is required${NC}"
    echo "Set it in .env file or environment variable"
    exit 1
fi

# Check if AWS CLI is available
if ! command -v aws &> /dev/null; then
    echo -e "${RED}Error: AWS CLI not found. Please install it first.${NC}"
    exit 1
fi

# Build AWS CLI command with optional profile
AWS_CMD="aws"
if [[ -n "${AWS_PROFILE}" ]]; then
    AWS_CMD="${AWS_CMD} --profile ${AWS_PROFILE}"
fi
AWS_CMD="${AWS_CMD} --region ${AWS_REGION}"

# Validate AWS session before proceeding
echo -e "${BLUE}Validating AWS session...${NC}"
if ! ${AWS_CMD} sts get-caller-identity &> /dev/null; then
    echo ""
    if [[ -n "${AWS_PROFILE}" ]]; then
        echo -e "${RED}Error: AWS session expired or invalid for profile '${AWS_PROFILE}'${NC}"
        echo -e "${YELLOW}Please run: aws sso login --profile ${AWS_PROFILE}${NC}"
    else
        echo -e "${RED}Error: AWS session expired or invalid${NC}"
        echo -e "${YELLOW}Please ensure AWS credentials are configured${NC}"
    fi
    exit 1
fi
echo -e "${GREEN}✓ AWS session is valid${NC}"
echo ""

if [[ "${DRY_RUN}" == "true" ]]; then
    echo -e "${YELLOW}=== DRY RUN MODE - No changes will be made ===${NC}"
else
    echo -e "${BLUE}=== Snowflake Keys Setup ===${NC}"
fi
echo ""

echo "Configuration:"
echo "  AWS Region:          ${AWS_REGION}"
echo "  AWS Profile:         ${AWS_PROFILE:-<default>}"
echo "  Snowflake Org:       ${SNOWFLAKE_ORG}"
echo "  Org Admin Account:   ${SNOWFLAKE_ORG_ADMIN_ACCOUNT}"
if [[ "${GENERATE_TEST_KEYS}" == "true" ]]; then
    echo "  Customer Namespace:  ${SAMPLE_CUSTOMER_NAMESPACE}"
    echo "  Customer Account:    ${SAMPLE_CUSTOMER_ACCOUNT}"
fi
echo "  Mode:                $([ "${GENERATE_TEST_KEYS}" == "true" ] && echo "Development (with test keys)" || echo "Production (org admin only)")"
echo "  Dry run:             ${DRY_RUN}"
echo "  Overwrite existing:  ${OVERWRITE}"
echo ""

# Define secrets to create (parallel arrays for bash 3.2 compatibility)
ORG_ADMIN_SECRET_PATH="snowflake/org/${SNOWFLAKE_ORG}/${SNOWFLAKE_ORG_ADMIN_ACCOUNT}/org-admin-credentials"

echo "Secrets to create:"
echo "  - ${ORG_ADMIN_SECRET_PATH} (username: ${USERNAME})"

if [[ "${GENERATE_TEST_KEYS}" == "true" ]]; then
    TENANT_SECRET_PATH="snowflake/tenant/${SNOWFLAKE_ORG}/${SAMPLE_CUSTOMER_NAMESPACE}/${SAMPLE_CUSTOMER_ACCOUNT}/platform-credentials"
    echo "  - ${TENANT_SECRET_PATH} (username: ${USERNAME})"
fi
echo ""

# Check which secrets already exist
check_secret_exists() {
    local secret_path="$1"
    if ${AWS_CMD} secretsmanager describe-secret --secret-id "${secret_path}" &>/dev/null; then
        return 0  # exists
    else
        return 1  # does not exist
    fi
}

echo -e "${BLUE}Checking existing secrets...${NC}"

if check_secret_exists "${ORG_ADMIN_SECRET_PATH}"; then
    ORG_ADMIN_EXISTS=true
    echo -e "${YELLOW}  ✓ ${ORG_ADMIN_SECRET_PATH} exists${NC}"
else
    ORG_ADMIN_EXISTS=false
    echo "  ✗ ${ORG_ADMIN_SECRET_PATH} does not exist"
fi

if [[ "${GENERATE_TEST_KEYS}" == "true" ]]; then
    if check_secret_exists "${TENANT_SECRET_PATH}"; then
        TENANT_EXISTS=true
        echo -e "${YELLOW}  ✓ ${TENANT_SECRET_PATH} exists${NC}"
    else
        TENANT_EXISTS=false
        echo "  ✗ ${TENANT_SECRET_PATH} does not exist"
    fi
fi
echo ""

# Without --overwrite, an existing secret is reused (not regenerated) — its
# stored public key is still printed below so Snowflake can be configured.
WILL_GENERATE=false
if [[ "${OVERWRITE}" == "false" ]]; then
    if [[ "${ORG_ADMIN_EXISTS}" == "true" ]]; then
        echo -e "${YELLOW}Note: ${ORG_ADMIN_SECRET_PATH} already exists — reusing it (use --overwrite to replace)${NC}"
    else
        WILL_GENERATE=true
    fi
    if [[ "${GENERATE_TEST_KEYS}" == "true" ]]; then
        if [[ "${TENANT_EXISTS}" == "true" ]]; then
            echo -e "${YELLOW}Note: ${TENANT_SECRET_PATH} already exists — reusing it (use --overwrite to replace)${NC}"
        else
            WILL_GENERATE=true
        fi
    fi
    echo ""
else
    WILL_GENERATE=true
fi

# Confirmation prompt - only needed when something will actually be written
if [[ "${WILL_GENERATE}" == "true" && "${SKIP_CONFIRMATION}" == "false" && "${DRY_RUN}" == "false" ]]; then
    echo -e "${YELLOW}This will generate new RSA key pairs and store them in AWS Secrets Manager.${NC}"
    if [[ "${OVERWRITE}" == "true" ]]; then
        echo -e "${RED}Existing secrets will be OVERWRITTEN.${NC}"
    fi
    read -rp "Continue? [y/N] " response
    if [[ ! "${response}" =~ ^[Yy]$ ]]; then
        echo "Aborted."
        exit 0
    fi
    echo ""
elif [[ "${WILL_GENERATE}" == "false" ]]; then
    echo -e "${YELLOW}All secrets already exist. No new RSA key pairs will be generated — the Snowflake commands below will use the already-stored public keys.${NC}"
    echo ""
fi

# Function to generate RSA key pair and return as variables
generate_rsa_keypair() {
    local username="$1"

    local temp_dir
    temp_dir=$(mktemp -d)
    local private_key_file="${temp_dir}/rsa_key.p8"
    local public_key_file="${temp_dir}/rsa_key.pub"

    echo -e "${BLUE}Generating RSA key pair for ${username}...${NC}"

    # Generate private key in PKCS#8 format (with PEM delimiters)
    openssl genrsa 2048 2>/dev/null | openssl pkcs8 -topk8 -inform PEM -out "${private_key_file}" -nocrypt

    # Generate public key from private key
    openssl rsa -in "${private_key_file}" -pubout -out "${public_key_file}" 2>/dev/null

    # Read private key WITH PEM delimiters (PKCS#8 format required by Snowflake driver)
    GENERATED_PRIVATE_KEY=$(cat "${private_key_file}")

    # Read public key and strip PEM delimiters, convert to single line
    # Required format for Snowflake ALTER USER command
    GENERATED_PUBLIC_KEY=$(grep -v "BEGIN PUBLIC KEY" "${public_key_file}" | grep -v "END PUBLIC KEY" | tr -d '\n')

    # Cleanup temp files
    rm -rf "${temp_dir}"
}

# Function to fetch the public key already stored at a secret path
get_existing_public_key() {
    local secret_path="$1"
    ${AWS_CMD} secretsmanager get-secret-value \
        --secret-id "${secret_path}" \
        --query 'SecretString' \
        --output text | jq -r '.public_key'
}

# Function to create an account-specific secret
create_account_secret() {
    local secret_path="$1"
    local username="$2"
    local account="$3"
    local exists="$4"
    local role="${5:-}"

    local public_key

    if [[ "${exists}" == "true" && "${OVERWRITE}" == "false" ]]; then
        echo -e "${BLUE}Secret already exists: ${secret_path}${NC}"
        echo -e "${YELLOW}  Skipping generation — reusing stored public key (use --overwrite to replace)${NC}"
        public_key=$(get_existing_public_key "${secret_path}")
    else
        echo -e "${BLUE}Creating secret: ${secret_path}${NC}"

        local private_key
        generate_rsa_keypair "${username}"
        public_key="${GENERATED_PUBLIC_KEY}"
        private_key="${GENERATED_PRIVATE_KEY}"

        # Build secret JSON
        local secret_json
        secret_json=$(jq -n \
            --arg username "${username}" \
            --arg public_key "${public_key}" \
            --arg private_key "${private_key}" \
            '{
                username: $username,
                public_key: $public_key,
                private_key: $private_key
            }')

        # Create or update secret
        if [[ "${DRY_RUN}" == "true" ]]; then
            if [[ "${exists}" == "true" ]]; then
                echo -e "${BLUE}  [DRY RUN] Would update existing secret${NC}"
            else
                echo -e "${BLUE}  [DRY RUN] Would create new secret${NC}"
            fi

            # Create display version with truncated private key
            # Get first two lines of private key (delimiter + first line of key data)
            local truncated_private_key
            truncated_private_key=$(echo "${private_key}" | head -n 2)
            truncated_private_key="${truncated_private_key}"$'\n'"...[truncated]"

            local secret_json_display
            secret_json_display=$(jq -n \
                --arg username "${username}" \
                --arg public_key "${public_key}" \
                --arg private_key "${truncated_private_key}" \
                '{
                    username: $username,
                    public_key: $public_key,
                    private_key: $private_key
                }')

            echo -e "${BLUE}  [DRY RUN] Secret JSON:${NC}"
            echo "${secret_json_display}" | sed 's/^/    /'
        else
            if [[ "${exists}" == "true" ]]; then
                ${AWS_CMD} secretsmanager put-secret-value \
                    --secret-id "${secret_path}" \
                    --secret-string "${secret_json}" \
                    > /dev/null
                echo -e "${GREEN}  ✓ Updated existing secret${NC}"
            else
                ${AWS_CMD} secretsmanager create-secret \
                    --name "${secret_path}" \
                    --secret-string "${secret_json}" \
                    > /dev/null
                echo -e "${GREEN}  ✓ Created new secret${NC}"
            fi
        fi
    fi

    # Display instructions for Snowflake
    echo -e "${YELLOW}  → Run in Snowflake (account: ${account}):${NC}"
    if [[ -n "${role}" ]]; then
        echo "     CREATE USER IF NOT EXISTS ${username} TYPE = SERVICE DEFAULT_ROLE = ${role} COMMENT = 'Yukimi platform service user';"
        echo "     GRANT ROLE ${role} TO USER ${username};"
    fi
    echo "     ALTER USER ${username} SET RSA_PUBLIC_KEY='${public_key}';"
    echo ""
}

# Create all secrets
echo -e "${BLUE}=== Creating Secrets ===${NC}"
echo ""

# 1. Org admin credentials
create_account_secret \
    "${ORG_ADMIN_SECRET_PATH}" \
    "${USERNAME}" \
    "${SNOWFLAKE_ORG_ADMIN_ACCOUNT}" \
    "${ORG_ADMIN_EXISTS}" \
    "GLOBALORGADMIN"

# 2. Tenant platform credentials - only if --generate-test-keys
if [[ "${GENERATE_TEST_KEYS}" == "true" ]]; then
    create_account_secret \
        "${TENANT_SECRET_PATH}" \
        "${USERNAME}" \
        "${SAMPLE_CUSTOMER_ACCOUNT}" \
        "${TENANT_EXISTS}" \
        "ACCOUNTADMIN"
fi

if [[ "${DRY_RUN}" == "true" ]]; then
    echo -e "${BLUE}=== Dry Run Complete ===${NC}"
    echo ""
    echo "This was a dry run. No changes were made to AWS Secrets Manager."
    echo ""
    echo "To create the secrets for real, run:"
    if [[ "${GENERATE_TEST_KEYS}" == "true" ]]; then
        echo "  $(basename "$0") --generate-test-keys --yes"
    else
        echo "  $(basename "$0") --yes"
    fi
else
    echo -e "${GREEN}=== Setup Complete ===${NC}"
    echo ""
    echo "Next steps:"
    echo "1. Run the Snowflake commands shown above"
    echo "2. Verify connectivity using the integration tests"
fi
echo ""