//go:build !go1.27

package gophlog

import "reflect"

// Before Go 1.27 a string-kind map key was resolved by value, ignoring any
// TextMarshaler it implemented.
func mapKeyPrefersText(k reflect.Value) bool {
	return k.Kind() != reflect.String && k.Type().Implements(textMarshalerTyp)
}
