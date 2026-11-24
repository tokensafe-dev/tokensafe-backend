package core

import (
	"reflect"
	"strings"
)

// ReflectStructSchema introspects a Go struct type and returns an agent-ready schema dictionary.
func ReflectStructSchema(v interface{}) map[string]interface{} {
	if v == nil {
		return nil
	}

	typ := reflect.TypeOf(v)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return map[string]interface{}{"type": typ.String()}
	}

	schema := make(map[string]interface{})
	fields := make(map[string]interface{})

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}

		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}

		fieldName := field.Name
		required := true

		if jsonTag != "" {
			parts := strings.Split(jsonTag, ",")
			if parts[0] != "" {
				fieldName = parts[0]
			}
			for _, opt := range parts[1:] {
				if opt == "omitempty" {
					required = false
				}
			}
		}

		if field.Type.Kind() == reflect.Ptr {
			required = false
		}

		fields[fieldName] = map[string]interface{}{
			"type":     field.Type.String(),
			"required": required,
		}
	}

	schema["structName"] = typ.Name()
	schema["fields"] = fields
	return schema
}
