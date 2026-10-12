package tiimo

import (
	_ "embed"
	"encoding/json"
)

//go:embed schemas.json
var schemaBytes []byte
var schemas map[string]map[string]any

func init() {
	if err := json.Unmarshal(schemaBytes, &schemas); err != nil {
		panic(err)
	}
}

// Project preserves writable fields recursively while excluding response-only fields.
func Project(name string, value Object) Object { return project(schemas[name], value) }
func project(schema Object, value Object) Object {
	if ref, ok := schema["$ref"].(string); ok {
		return Project(ref[len("#/components/schemas/"):], value)
	}
	out := Object{}
	props, _ := schema["properties"].(map[string]any)
	for k, p := range props {
		field := p.(map[string]any)
		if field["readOnly"] == true {
			continue
		}
		v, ok := value[k]
		if !ok {
			continue
		}
		if obj, ok := v.(map[string]any); ok {
			v = project(field, obj)
		}
		if arr, ok := v.([]any); ok {
			items, _ := field["items"].(map[string]any)
			copyArr := make([]any, 0, len(arr))
			for _, item := range arr {
				if obj, ok := item.(map[string]any); ok {
					copyArr = append(copyArr, project(items, obj))
				} else {
					copyArr = append(copyArr, item)
				}
			}
			v = copyArr
		}
		out[k] = v
	}
	return out
}

func UpdateTaskBody(task Object, changes Object) Object {
	// API responses call the color field "color", while requests use backgroundColor.
	task = Clone(task)
	if v, ok := task["color"]; ok {
		task["backgroundColor"] = v
	}
	if subs, ok := task["subTasks"].([]any); ok {
		for _, sub := range subs {
			if o, ok := sub.(map[string]any); ok {
				if v, ok := o["color"]; ok {
					o["backgroundColor"] = v
				}
			}
		}
	}
	if grouping, ok := task["grouping"].(map[string]any); ok {
		task["currentGrouping"] = grouping["groupingType"]
	}
	for k, v := range changes {
		task[k] = v
	}
	return Project("WebApi.Todo.Models.UpdateTodoTaskRequest", task)
}
func Clone(o Object) Object {
	b, _ := json.Marshal(o)
	var n Object
	_ = json.Unmarshal(b, &n)
	return n
}
