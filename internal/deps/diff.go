package deps

import "reflect"

// diff geeft per gewijzigd veld {from, to}, onder de naam uit de tag event.
// Een nil-pointer wordt nil.
func diff[T any](before, after T) map[string]any {
	bv, av := reflect.ValueOf(before), reflect.ValueOf(after)
	out := map[string]any{}
	for i := range bv.NumField() {
		b, a := deref(bv.Field(i)), deref(av.Field(i))
		if reflect.DeepEqual(b, a) {
			continue
		}
		out[bv.Type().Field(i).Tag.Get("event")] = map[string]any{"from": b, "to": a}
	}
	return out
}

func deref(v reflect.Value) any {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return v.Elem().Interface()
	}
	return v.Interface()
}
