sagepipe
=======

[![Test Status](https://github.com/Songmu/sagepipe/actions/workflows/test.yaml/badge.svg?branch=main)][actions]
[![Coverage Status](https://codecov.io/gh/Songmu/sagepipe/branch/main/graph/badge.svg)][codecov]
[![MIT License](https://img.shields.io/github/license/Songmu/sagepipe)][license]
[![PkgGoDev](https://pkg.go.dev/badge/github.com/Songmu/sagepipe)][PkgGoDev]

[actions]: https://github.com/Songmu/sagepipe/actions?workflow=test
[codecov]: https://codecov.io/gh/Songmu/sagepipe
[license]: https://github.com/Songmu/sagepipe/blob/main/LICENSE
[PkgGoDev]: https://pkg.go.dev/github.com/Songmu/sagepipe

sagepipe short description

## Synopsis

```go
// simple usage here
```

## Description

## Installation

```console
# Install the latest version. (Install it into ./bin/ by default).
% curl -sfL https://raw.githubusercontent.com/Songmu/sagepipe/main/install.sh | sh -s

# Specify installation directory ($(go env GOPATH)/bin/) and version.
% curl -sfL https://raw.githubusercontent.com/Songmu/sagepipe/main/install.sh | sh -s -- -b $(go env GOPATH)/bin [vX.Y.Z]

# In alpine linux (as it does not come with curl by default)
% wget -O - -q https://raw.githubusercontent.com/Songmu/sagepipe/main/install.sh | sh -s [vX.Y.Z]

# go install
% go install github.com/Songmu/sagepipe/cmd/sagepipe@latest
```

## Author

[Songmu](https://github.com/Songmu)
