package konfig

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jinzhu/copier"
	"github.com/spf13/cast"
)

const (
	// TagKey is the tag key to unmarshal config values to bound value
	TagKey = "konfig"
	// KeySep is the separator for config keys
	KeySep = "."
)

var (
	// ErrIncorrectValue is the error thrown when trying to bind an invalid type to a config store
	ErrIncorrectValue = errors.New("Bind takes a map[string]interface{} or a struct")
	// ErrIncorrectStructValue is the error thrown when trying to bind a non struct value with the BindStructStrict method
	ErrIncorrectStructValue = errors.New("BindStructStrict takes a struct")
)

type value struct {
	s     *S
	v     *atomic.Value
	vt    reflect.Type
	mut   *sync.Mutex
	isMap bool
}

func sorted(source map[string]interface{}) (result []string) {
	for key := range source {
		result = append(result, key)
	}
	sort.Strings(result)
	return
}

// Value returns the value bound to the root config store
func Value() interface{} {
	return instance().Value()
}

// Bind binds a value to the root config store
func Bind(v interface{}) {
	instance().Bind(v)
}

// BindStructStrict binds a value to the root config store and adds the exposed keys as strict keys
func BindStructStrict(v interface{}) {
	instance().BindStructStrict(v)
}

// Value returns the value bound to the config store
func (c *S) Value() interface{} {
	return c.v.v.Load()
}

// Bind binds a value (either a map[string]interface{} or a struct) to the config store.
// When config values are set on the config store, they are also set on the bound value.
func (c *S) Bind(v interface{}) {
	var t = reflect.TypeOf(v)
	var k = t.Kind()
	//  if it is neither a map nor a struct
	if k != reflect.Map && k != reflect.Struct {
		panic(ErrIncorrectValue)
	}
	// if it is a map check map[string]interface{}
	if k == reflect.Map &&
		(t.Key().Kind() != reflect.String || t.Elem().Kind() != reflect.Interface) {
		panic(ErrIncorrectValue)
	}

	var val = &value{
		s:     c,
		isMap: k == reflect.Map,
		mut:   &sync.Mutex{},
	}

	val.vt = t

	// create a new pointer to the given value and store it
	var atomicValue atomic.Value
	var n = reflect.Zero(val.vt)
	atomicValue.Store(n.Interface())

	val.v = &atomicValue

	c.v = val
}

// BindStructStrict binds a value (must a struct) to the config store and adds the exposed fields as strick keys.
func (c *S) BindStructStrict(v interface{}) {
	var t = reflect.TypeOf(v)
	var k = t.Kind()
	//  if it not a struct
	if k != reflect.Struct {
		panic(ErrIncorrectStructValue)
	}

	keys := getStructKeys(t, "")
	c.Strict(keys...)
	c.Bind(v)
}

func getStructKeys(t reflect.Type, prefix string) []string {
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		var fieldValue = t.Field(i)
		var tag = fieldValue.Tag.Get(TagKey)

		if tag == "-" {
			continue
		}

		// use field name when konfig tag is not specified
		if tag == "" {
			if fieldValue.Name == "" {
				tag = ",embed"
			} else {
				tag = strings.ToLower(fieldValue.Name)
			}
		}

		if fieldValue.Type.Kind() == reflect.Struct {
			var prefix string
			if tag == ",embed" {
				prefix = ""
			} else {
				prefix = tag + KeySep
			}
			structKeys := getStructKeys(fieldValue.Type, prefix)
			keys = append(keys, structKeys...)

			// don't add the parent tag
			continue
		}

		keys = append(keys, prefix+tag)
	}

	return keys
}

func (val *value) set(k string, v interface{}) {
	val.mut.Lock()
	defer val.mut.Unlock()

	var configValue = val.v.Load()

	// if value is a map
	// store things in a map
	if val.isMap {
		var mapV = configValue.(map[string]interface{})
		var nMap = make(map[string]interface{})

		for _, kk := range sorted(mapV) {
			nMap[kk] = mapV[kk]
		}

		nMap[k] = v

		val.v.Store(nMap)
		return
	}

	// make a copy
	var t = reflect.TypeOf(configValue)
	var nVal = reflect.New(t)

	copier.Copy(nVal.Interface(), configValue)

	val.setStruct(k, v, nVal)

	val.v.Store(nVal.Elem().Interface())
}

func (val *value) setValues(x s) {
	val.mut.Lock()
	defer val.mut.Unlock()

	var configValue = val.v.Load()

	// if value is a map
	// store things in a map
	if val.isMap {
		var mapV = configValue.(map[string]interface{})
		var nMap = make(map[string]interface{})

		for _, kk := range sorted(mapV) {
			nMap[kk] = mapV[kk]
		}

		for _, kk := range sorted(x) {
			nMap[kk] = x[kk]
		}

		val.v.Store(nMap)
		return
	}

	// make a copy
	var t = reflect.TypeOf(configValue)
	var nVal = reflect.New(t)

	for _, kk := range sorted(x) {
		val.setStruct(kk, x[kk], nVal)
	}

	val.v.Store(nVal.Elem().Interface())
}

func (val *value) setStruct(k string, v interface{}, targetValue reflect.Value) bool {

	// is a struct, find matching tag
	var valTypePtr = targetValue.Type()
	var valType = valTypePtr.Elem()
	var valValuePtr = targetValue
	var valValue = valValuePtr.Elem()
	var set bool

	for i := 0; i < valType.NumField(); i++ {
		var fieldValue = valType.Field(i)
		var fieldName = fieldValue.Name
		var tag = fieldValue.Tag.Get(TagKey)

		// use field name when konfig tag is not specified
		if tag == "" && fieldValue.Name == "" {
			tag = ",embed"
		}

		// check tag, if it matches key
		// assign v to field
		if tag == k || strings.EqualFold(fieldName, k) {
			var field = valValue.FieldByName(fieldValue.Name)
			if field.CanSet() {
				if !unmarshal(field, v) {
					result := castValue(field.Interface(), v)
					if field.CanAddr() && result == nil {
						field.Set(reflect.Zero(field.Type()))
					} else {
						field.Set(reflect.ValueOf(result))
					}
				}
			}
			set = true
			continue

			// else if key has tag in prefix
		} else if tag == ",embed" ||
			strings.HasPrefix(k, tag+KeySep) ||
			strings.HasPrefix(strings.ToLower(k), strings.ToLower(fieldName)+KeySep) {

			var nK string

			if tag == ",embed" {
				nK = k
			} else if strings.HasPrefix(k, tag+KeySep) {
				nK = k[len(tag+KeySep):]
			} else {
				nK = k[len(fieldName+KeySep):]
			}

			switch fieldValue.Type.Kind() {
			// Is a map.
			// Only map[string]someStruct is supported.
			// The idea is to be able to store lists of key value where the keys are not known.
			case reflect.Map:
				var keyKind = fieldValue.Type.Key().Kind()
				var eltKind = fieldValue.Type.Elem().Kind()
				// if map key is a string and elem is a struct
				// else we skip this field
				if keyKind == reflect.String {
					var structType reflect.Type
					var ptr bool
					switch {
					case eltKind == reflect.Ptr && fieldValue.Type.Elem().Elem().Kind() == reflect.Struct:
						structType = fieldValue.Type.Elem().Elem()
						ptr = true
					case eltKind == reflect.Struct:
						structType = fieldValue.Type.Elem()
					case eltKind == reflect.Ptr && fieldValue.Type.Elem().Elem().Kind() == reflect.String:
						fallthrough
					case eltKind == reflect.String:
						var field = valValue.FieldByName(fieldValue.Name)
						if field.IsNil() {
							field.Set(reflect.MakeMap(fieldValue.Type))
						}
						field.SetMapIndex(reflect.ValueOf(nK), reflect.ValueOf(v))
						set = true
						continue
					default:
						continue
					}

					var nVal = reflect.New(structType)
					var field = valValue.FieldByName(fieldValue.Name)

					// cut the key until the next sep
					var keyElt = strings.SplitN(nK, KeySep, 2)
					if len(keyElt) == 2 {

						var mapKey = keyElt[0]

						// check if map is nil, if yes create new one
						var mapVal = field
						if mapVal.IsNil() {
							mapVal = reflect.MakeMap(fieldValue.Type)
							field.Set(mapVal)
						}

						var mapKeyVal = reflect.ValueOf(mapKey)
						var ov = mapVal.MapIndex(mapKeyVal)

						// we copy the old value, to make sure we don't lose anything
						if ov.IsValid() {
							copier.Copy(nVal.Interface(), ov.Interface())
						}

						// we set the field with the new struct
						if ok := val.setStruct(
							keyElt[1],
							v,
							nVal,
						); ok {
							if !ptr {
								mapVal.SetMapIndex(mapKeyVal, nVal.Elem())
							} else {
								mapVal.SetMapIndex(mapKeyVal, nVal)
							}
							set = true
						}
					}
					continue
				}
			case reflect.Struct:
				var field = valValue.FieldByName(fieldValue.Name)
				// if field can be set
				if field.CanSet() {
					var structType = field.Type()
					var nVal = reflect.New(structType)

					// we copy it
					copier.Copy(nVal.Interface(), field.Interface())

					// we set the field with the new struct
					if ok := val.setStruct(nK, v, nVal); ok {
						field.Set(nVal.Elem())
						set = true
					}

					continue
				}
			case reflect.Ptr:
				if fieldValue.Type.Elem().Kind() == reflect.Struct {
					var field = valValue.FieldByName(fieldValue.Name)
					if field.CanSet() {
						var nVal = reflect.New(fieldValue.Type.Elem())

						// if field is not nil
						// we copy it
						if !field.IsNil() {
							copier.Copy(nVal.Interface(), field.Interface())
						}

						if ok := val.setStruct(nK, v, nVal); ok {
							field.Set(nVal)
							set = true
						}
						continue
					}
				}
			}
		}
	}

	if !set {
		val.s.cfg.Logger.Get().Debug(
			fmt.Sprintf(
				"Config key %s not found in bound value",
				k,
			),
		)
	}

	return set
}

func unmarshalText(f reflect.Value, v interface{}) bool {
	if !f.Type().Implements(reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()) {
		return false
	}
	if tu, ok := f.Interface().(encoding.TextUnmarshaler); ok {
		if f.Type().Kind() == reflect.Ptr && f.IsNil() {
			empty := reflect.New(f.Type().Elem())
			f.Set(empty)
			tu, _ = f.Interface().(encoding.TextUnmarshaler)
		}
		str := cast.ToString(v)
		err := tu.UnmarshalText([]byte(str))
		if err != nil {
			panic(err)
		}
		return err == nil
	}
	return false
}

func unmarshal(f reflect.Value, v interface{}) bool {
	if unmarshalText(f, v) {
		return true
	} else if f.CanAddr() && unmarshalText(f.Addr(), v) {
		return true
	} else {
		return false
	}
}

func castValue(f interface{}, v interface{}) interface{} {
	switch f.(type) {
	// string
	case *string:
		value := cast.ToString(v)
		return &value
	case string:
		return cast.ToString(v)
	// bool
	case *bool:
		value := cast.ToBool(v)
		return &value
	case bool:
		return cast.ToBool(v)
	// int
	case *int:
		value := cast.ToInt(v)
		return &value
	case int:
		return cast.ToInt(v)
	// uint
	case *uint:
		value := cast.ToUint(v)
		return &value
	case uint:
		return cast.ToUint(v)
	// int8
	case *int8:
		value := cast.ToInt8(v)
		return &value
	case int8:
		return cast.ToInt8(v)
	// unt8
	case *uint8:
		value := cast.ToUint8(v)
		return &value
	case uint8:
		return cast.ToUint8(v)
	// int16
	case *int16:
		value := cast.ToInt16(v)
		return &value
	case int16:
		return cast.ToInt16(v)
	// unit16
	case *uint16:
		value := cast.ToUint16(v)
		return &value
	case uint16:
		return cast.ToUint16(v)
	// int32
	case *int32:
		value := cast.ToInt32(v)
		return &value
	case int32:
		return cast.ToInt32(v)
	// uint32
	case *uint32:
		value := cast.ToUint32(v)
		return &value
	case uint32:
		return cast.ToUint32(v)
	// int64
	case *int64:
		value := cast.ToInt64(v)
		return &value
	case int64:
		return cast.ToInt64(v)
	// uint64
	case *uint64:
		value := cast.ToUint64(v)
		return &value
	case uint64:
		return cast.ToUint64(v)
	// float32
	case *float32:
		value := cast.ToFloat32(v)
		return &value
	case float32:
		return cast.ToFloat32(v)
	// float64
	case *float64:
		value := cast.ToFloat64(v)
		return &value
	case float64:
		return cast.ToFloat64(v)
	// time.Time
	case *time.Time:
		value := cast.ToTime(v)
		return &value
	case time.Time:
		return cast.ToTime(v)
	// time.Duration
	case *time.Duration:
		value := cast.ToDuration(v)
		return &value
	case time.Duration:
		return cast.ToDuration(v)
	// rest
	case []string:
		return cast.ToStringSlice(v)
	case []int:
		return cast.ToIntSlice(v)
	case map[string]string:
		return cast.ToStringMapString(v)
	}
	return v
}
