package db

import (
	"reflect"
	"strings"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// FieldInfo describes a model field for L2 contract disclosure.
type FieldInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	DBColumn   string `json:"dbColumn,omitempty"`
	JSONTag    string `json:"jsonTag,omitempty"`
	IsPrimary  bool   `json:"isPrimary,omitempty"`
	IsNullable bool   `json:"isNullable,omitempty"`
}

// TableContract describes a database model table contract.
type TableContract struct {
	TableName string      `json:"tableName"`
	Fields    []FieldInfo `json:"fields"`
}

// ReflectModel introspects a Go struct instance or pointer and generates an L2 contract.
func ReflectModel(model interface{}) TableContract {
	val := reflect.ValueOf(model)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	typ := val.Type()
	tableName := typ.Name()

	// Check if model implements TableName() string convention
	if method := val.MethodByName("TableName"); method.IsValid() && method.Type().NumIn() == 0 && method.Type().NumOut() == 1 {
		res := method.Call(nil)
		if str, ok := res[0].Interface().(string); ok && str != "" {
			tableName = str
		}
	}

	fields := make([]FieldInfo, 0, typ.NumField())

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		dbTag := field.Tag.Get("db")
		jsonTag := field.Tag.Get("json")
		gormTag := field.Tag.Get("gorm")

		fInfo := FieldInfo{
			Name:       field.Name,
			Type:       field.Type.String(),
			DBColumn:   cleanTag(dbTag),
			JSONTag:    cleanTag(jsonTag),
			IsPrimary:  strings.Contains(gormTag, "primaryKey") || cleanTag(dbTag) == "id" || strings.ToLower(field.Name) == "id",
			IsNullable: field.Type.Kind() == reflect.Ptr,
		}

		if fInfo.DBColumn == "" {
			fInfo.DBColumn = strings.ToLower(field.Name)
		}

		fields = append(fields, fInfo)
	}

	return TableContract{
		TableName: tableName,
		Fields:    fields,
	}
}

// AsContractData maps TableContract into standard protocol.L2ContractData.
func (t TableContract) AsContractData() *protocol.L2ContractData {
	fieldsMap := make(map[string]interface{})
	for _, f := range t.Fields {
		fieldsMap[f.Name] = map[string]interface{}{
			"type":      f.Type,
			"column":    f.DBColumn,
			"primary":   f.IsPrimary,
			"nullable":  f.IsNullable,
		}
	}

	return &protocol.L2ContractData{
		Target: t.TableName,
		RequestDTO: map[string]interface{}{
			"table":  t.TableName,
			"fields": fieldsMap,
		},
	}
}

func cleanTag(tag string) string {
	parts := strings.Split(tag, ",")
	if len(parts) > 0 {
		return parts[0]
	}
	return tag
}
