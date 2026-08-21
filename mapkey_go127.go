//go:build go1.27

package gophlog

import "reflect"

// Go 1.27 resolves a map key's TextMarshaler before its string kind.
func mapKeyPrefersText(k reflect.Value) bool {
	return k.Type().Implements(textMarshalerTyp)
}
