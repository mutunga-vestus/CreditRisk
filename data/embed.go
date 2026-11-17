package data

import _ "embed"

// Schema is applied automatically when the server starts.
//
//go:embed schema.sql
var Schema string
