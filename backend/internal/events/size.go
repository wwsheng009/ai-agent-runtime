package events

import (
	"encoding/json"
	"reflect"
	"time"
)

// ApproximateEventBytes estimates retained heap pressure without serializing
// the payload into another full-size buffer.
func ApproximateEventBytes(event Event) int64 {
	return int64(len(event.Type)+len(event.TraceID)+len(event.AgentName)+len(event.SessionID)+len(event.ToolName)) +
		ApproximateValueBytes(event.Payload)
}

// ApproximateValueBytes estimates JSON-like values commonly used in events.
func ApproximateValueBytes(value interface{}) int64 {
	return approximateValueBytes(value, 0)
}

func approximateValueBytes(value interface{}, depth int) int64 {
	if value == nil || depth > 16 {
		return 0
	}
	switch typed := value.(type) {
	case string:
		return int64(len(typed))
	case json.RawMessage:
		return int64(len(typed))
	case []byte:
		return int64(len(typed))
	case map[string]interface{}:
		var size int64
		for key, item := range typed {
			size += int64(len(key)) + approximateValueBytes(item, depth+1)
		}
		return size
	case map[string]string:
		var size int64
		for key, item := range typed {
			size += int64(len(key) + len(item))
		}
		return size
	case []interface{}:
		var size int64
		for _, item := range typed {
			size += approximateValueBytes(item, depth+1)
		}
		return size
	case []string:
		var size int64
		for _, item := range typed {
			size += int64(len(item))
		}
		return size
	case time.Time:
		return 24
	default:
		return approximateReflectedValueBytes(reflect.ValueOf(value), depth+1)
	}
}

func approximateReflectedValueBytes(value reflect.Value, depth int) int64 {
	if !value.IsValid() || depth > 16 {
		return 0
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.String:
		return int64(value.Len())
	case reflect.Slice, reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return int64(value.Len())
		}
		var size int64
		for index := 0; index < value.Len(); index++ {
			size += approximateReflectedValueBytes(value.Index(index), depth+1)
		}
		return size
	case reflect.Map:
		var size int64
		iterator := value.MapRange()
		for iterator.Next() {
			size += approximateReflectedValueBytes(iterator.Key(), depth+1)
			size += approximateReflectedValueBytes(iterator.Value(), depth+1)
		}
		return size
	case reflect.Struct:
		var size int64
		for index := 0; index < value.NumField(); index++ {
			size += approximateReflectedValueBytes(value.Field(index), depth+1)
		}
		return size
	default:
		return 16
	}
}

func clonePayloadValue(value interface{}, depth int) interface{} {
	if value == nil || depth > 16 {
		return value
	}
	switch typed := value.(type) {
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	case map[string]interface{}:
		cloned := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			cloned[key] = clonePayloadValue(item, depth+1)
		}
		return cloned
	case map[string]string:
		cloned := make(map[string]string, len(typed))
		for key, item := range typed {
			cloned[key] = item
		}
		return cloned
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = clonePayloadValue(item, depth+1)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
