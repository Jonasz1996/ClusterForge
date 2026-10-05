// Package gen bevat de uit api/openapi.yaml gegenereerde types en routering.
package gen

//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../../../api/openapi.yaml
