#!/usr/bin/env bash
# Create (or update) the Jenkins pipeline job from jenkins-job.xml.
# Reads credentials from the environment — never hard-code them here.
#   export JENKINS_URL="https://jenkins.example:8080"
#   export JENKINS_USER="you"
#   export JENKINS_TOKEN="api-token"
#   ./deploy/jenkins-create-job.sh [job-name]
set -euo pipefail

: "${JENKINS_URL:?set JENKINS_URL}"
: "${JENKINS_USER:?set JENKINS_USER}"
: "${JENKINS_TOKEN:?set JENKINS_TOKEN}"

JOB="${1:-shixo-msn}"
DIR="$(cd "$(dirname "$0")" && pwd)"
AUTH="$JENKINS_USER:$JENKINS_TOKEN"

# CSRF crumb (required for POSTs on modern Jenkins).
CRUMB_JSON=$(curl -fsS -u "$AUTH" "$JENKINS_URL/crumbIssuer/api/json")
CRUMB_HEADER=$(printf '%s' "$CRUMB_JSON" | python3 -c \
  'import sys,json;d=json.load(sys.stdin);print(d["crumbRequestField"]+":"+d["crumb"])')

# Does the job already exist?
if curl -fsS -o /dev/null -u "$AUTH" "$JENKINS_URL/job/$JOB/api/json" 2>/dev/null; then
  echo "Job '$JOB' exists — updating config..."
  curl -fsS -u "$AUTH" -H "$CRUMB_HEADER" -H "Content-Type: application/xml" \
    --data-binary @"$DIR/jenkins-job.xml" \
    "$JENKINS_URL/job/$JOB/config.xml"
  echo "updated."
else
  echo "Creating job '$JOB'..."
  curl -fsS -u "$AUTH" -H "$CRUMB_HEADER" -H "Content-Type: application/xml" \
    --data-binary @"$DIR/jenkins-job.xml" \
    "$JENKINS_URL/createItem?name=$JOB"
  echo "created."
fi

echo "Trigger a build with:"
echo "  curl -X POST -u \"\$JENKINS_USER:\$JENKINS_TOKEN\" -H \"$CRUMB_HEADER\" $JENKINS_URL/job/$JOB/build"
