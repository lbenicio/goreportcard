#!/bin/sh
set -eu

go mod download

go install golang.org/x/lint/golint
go install github.com/fzipp/gocyclo/cmd/gocyclo
go install github.com/gordonklaus/ineffassign
go install github.com/kisielk/errcheck
go install github.com/client9/misspell/cmd/misspell
go install honnef.co/go/tools/cmd/staticcheck
