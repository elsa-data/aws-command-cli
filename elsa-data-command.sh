#!/bin/bash

#
# Executes admin commands inside a deployed Elsa Data environment
#

# the namespace must exist as a CloudMap namespace in the account of deployment
# it will be created by the infrastructure stack
CLOUD_MAP_NAMESPACE="elsa-data"

# the command channel registers itself via this service name
CLOUD_MAP_SERVICE="Command"

# our usage help
helpText() {
  printf "Usage: %s: [-n namespace] [-s service] \"command; command...\"\n" "$0"
}

# fail immediately on any error
set -o errexit

while getopts n:s: name; do
  case $name in
  n) CLOUD_MAP_NAMESPACE="$OPTARG" ;;
  s) CLOUD_MAP_SERVICE="$OPTARG" ;;
  ?)
    helpText
    exit 2
    ;;
  esac
done

shift $((OPTIND - 1))

if [ "x$1" == "x" ]; then
  helpText
  exit 2
fi

# when deployed our CDK stack registers the lambda into the namespace
LAMBDA_ARN=$(aws servicediscovery discover-instances \
  --namespace-name "$CLOUD_MAP_NAMESPACE" \
  --service-name "$CLOUD_MAP_SERVICE" \
  --output text --query "Instances[].Attributes.lambdaArn")

printf "Executing %s in %s/%s using %s\n" "$1" "$CLOUD_MAP_NAMESPACE" "$CLOUD_MAP_SERVICE" "$LAMBDA_ARN"
printf "(command executions can take a while e.g. minutes - this CLI tool will wait)\n"

# annoyingly the aws lambda invoke *only* writes data into a file - can't output it to stdout
# so we make a temp file to hold the result and set a trap to delete it
temp_file=$(mktemp)

trap "rm -f $temp_file" 0 2 3 15

# create a JSON string of the payload - but with jq doing any quoting etc
LAMBDA_PAYLOAD_STRING=$(jq -n --arg cmd "$1" '{cmd: $cmd}')

# our lambda knows how to pass cmd line strings to a spun up Elsa Data container just for CMD invoking
aws lambda invoke --function-name "$LAMBDA_ARN" \
  --cli-read-timeout 600 \
  --cli-binary-format raw-in-base64-out \
  --payload "$LAMBDA_PAYLOAD_STRING" \
  "$temp_file"

# the lambda returns details of where all its logs went
LG=$(jq <"$temp_file" -r '.logGroupName')
LS=$(jq <"$temp_file" -r '.logStreamName')

echo $LG
echo $LS

# and now we can print the log output (which is the admin command output)
aws logs tail "$LG" --log-stream-names "$LS" | cut -d' ' -f3-

