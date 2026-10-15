package documentcore

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

var errPreparedParametersTooLarge = errors.New("prepared parameters exceed 1 MiB")

type projectDefaultBudget struct{ remaining int }

func (budget *projectDefaultBudget) reserveProperty(name string, value any, comma bool) bool {
	keySize, ok := projectJSONSize(name, budget.remaining)
	if !ok {
		return false
	}
	overhead := keySize + 1 // colon
	if comma {
		overhead++
	}
	if overhead > budget.remaining {
		return false
	}
	valueSize, ok := projectJSONSize(value, budget.remaining-overhead)
	if !ok {
		return false
	}
	budget.remaining -= overhead + valueSize
	return true
}

// Count the exact encoding/json byte length without allocating the encoding.
// Each addition is bounded before traversal continues. Defaults are reserved
// before cloning or insertion, so repeated defaults cannot build a huge tree.
func projectJSONSize(value any, limit int) (int, bool) {
	remaining := limit
	var visit func(any) bool
	add := func(size int) bool {
		if size > remaining {
			return false
		}
		remaining -= size
		return true
	}
	visit = func(value any) bool {
		switch value := value.(type) {
		case nil:
			return add(4)
		case bool:
			if value {
				return add(4)
			}
			return add(5)
		case json.Number:
			return add(len(value))
		case string:
			if !add(2) || len(value) > remaining {
				return false
			}
			for _, character := range value {
				size := utf8.RuneLen(character)
				switch character {
				case '\\', '"', '\b', '\f', '\n', '\r', '\t':
					size = 2
				case '<', '>', '&', '\u2028', '\u2029':
					size = 6
				default:
					if character < 0x20 {
						size = 6
					}
				}
				if !add(size) {
					return false
				}
			}
			return true
		case map[string]any:
			if !add(2) {
				return false
			}
			first := true
			for key, child := range value {
				if !first && !add(1) {
					return false
				}
				first = false
				if !visit(key) || !add(1) || !visit(child) {
					return false
				}
			}
			return true
		case []any:
			if !add(2) {
				return false
			}
			for index, child := range value {
				if index > 0 && !add(1) {
					return false
				}
				if !visit(child) {
					return false
				}
			}
			return true
		default:
			return false // Inputs and schema defaults use the strict JSON decoder.
		}
	}
	if limit < 0 || !visit(value) {
		return 0, false
	}
	return limit - remaining, true
}
