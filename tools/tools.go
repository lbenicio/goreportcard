//go:build tools
// +build tools

package tools

import (
	_ "github.com/client9/misspell/cmd/misspell"
	_ "github.com/fzipp/gocyclo/cmd/gocyclo"
	_ "github.com/gordonklaus/ineffassign"
	_ "github.com/kisielk/errcheck"
	_ "golang.org/x/lint/golint"
	_ "honnef.co/go/tools/cmd/staticcheck"
)
