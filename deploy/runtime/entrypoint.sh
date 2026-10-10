#!/bin/sh
# Container entrypoint: exports the secret files mounted in /run/secrets/steward as
# environment variables (YAGPDB only reads its configuration from the environment),
# then runs the bot.
#
# A file named like a variable (YAGPDB_BOTTOKEN) becomes that variable; empty files
# are ignored, so optional integrations stay off until their file is filled.
# google-credentials.json is passed by path as GOOGLE_APPLICATION_CREDENTIALS.
set -eu

secrets_dir=/run/secrets/steward

if [ -d "$secrets_dir" ]; then
  for file in "$secrets_dir"/*; do
    [ -f "$file" ] && [ -s "$file" ] || continue
    name=${file##*/}
    case "$name" in
      google-credentials.json)
        export GOOGLE_APPLICATION_CREDENTIALS="$file"
        continue
        ;;
      [A-Z]*) ;;
      *) continue ;;
    esac
    case "$name" in *[!A-Z0-9_]*) continue ;; esac
    value=$(cat "$file")
    export "$name=$value"
  done
fi

# The upstream blog posts are not shipped: YAGPDB loads ./posts, left empty.
mkdir -p /data/posts

exec /app/steward "$@"
