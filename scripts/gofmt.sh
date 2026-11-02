#!/bin/bash

go_files=()
while IFS= read -r -d '' file; do
    case "$file" in
        *vendor*|*_repos*|*testrepo*) continue ;;
    esac
    go_files+=("$file")
done < <(find . -name '*.go' -print0)

need_gofmt=""
if ((${#go_files[@]})); then
    need_gofmt="$(gofmt -s -l "${go_files[@]}")"
fi

if [[ -n "$need_gofmt" ]]; then
    echo "These files fail gofmt -s:"
    echo "$need_gofmt"
    exit 1
fi


